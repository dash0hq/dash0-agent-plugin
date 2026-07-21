# GitHub Copilot CLI source — developer reference

This directory is the self-contained Copilot CLI plugin package — manifest
(`plugin.json`), camelCase hooks (`hooks.json`), the configure skill (`skills/`),
and the vendored bootstrap (`copilot-on-event.sh`). This file is the developer
reference: how the runtime works and how to build and run local changes.

End-user install / configure / uninstall docs live in
[.github/plugin/README.md](../.github/plugin/README.md). Releasing is shared
across runtimes — see [DEVELOPMENT.md](../DEVELOPMENT.md#releasing).

## How it works

Copilot CLI ships its own native OpenTelemetry, which carries the full token/cost/model and tool-execution detail. This
plugin uses that as a **local file** source rather than rebuilding it:

```
launch function ─ enables native OTel → per-session file (local only)
        │
   copilot hooks ─▶ copilot-on-event ─▶ internal/pipeline ─▶ Dash0
        sessionStart / userPromptSubmitted / agentStop / sessionEnd
        at each agentStop: read the file for this turn
```

The hooks drive the session/turn lifecycle; the native-OTel file supplies everything quantitative — including tool
spans, which hooks can't provide with real timings (and never fire inside sub-agents at all). The launch function is
installed by the `dash0-configure` skill as a shell function that shadows `copilot`; without it, a `copilot` session
still emits a chat span per turn — just without usage, response, or tool detail (graceful).

## Build & run locally

Use the `copilot-local-dev` skill — no GitHub push or release needed.

It registers a throwaway local marketplace (the repo's real `.github/plugin/marketplace.json`, renamed so it can't clash
with production), `marketplace add` + `plugin install`s it, and drops a locally built binary so hooks run your code
instead of a downloaded release.

Pass `--rebuild` to rebuild the Go binary after a change; the teardown removes the plugin, the marketplace, the
plugin-data + OTel dirs, and the config.

### Local e2e tests with a PAT

Deterministic Copilot tests (no auth):

```bash
go test ./internal/source/copilot/ ./test/consistency/
go test -tags=e2e -run 'TestE2ECopilot' ./test/e2e/          # L2 + fail-open + L3 credential contracts
```

The live canary `TestE2EFullFlowWithCopilot` runs the real `copilot` CLI and
**fails** unless `COPILOT_GITHUB_TOKEN` is set (loud, like the Claude/Codex
canaries), so scope the `-run` filter above to the deterministic tests when you
have no token. It installs the camelCase hooks
into a hermetic `COPILOT_HOME`, enables native OTel into a per-session file, runs
`copilot -p`, and asserts the emitted canonical `chat` span carries per-turn
`gen_ai.usage.*` sourced from that file. To run it:

```bash
npm install -g @github/copilot   # if needed
COPILOT_GITHUB_TOKEN=<pat-with-Copilot-Requests> \
  go test -tags=e2e -run TestE2EFullFlowWithCopilot ./test/e2e/ -v
```

To also exercise the real `:copilot` subpath install + the `dash0-configure`
launch function (not just the test's hook injection), after pushing this branch:
`copilot plugin install dash0hq/dash0-agent-plugin:copilot`, run `/dash0-configure`,
open a new shell, and confirm per-turn spans reach your Dash0 dataset.

## Tool spans & sub-agent handling

Copilot's hooks are used **only for the session/turn lifecycle** (`sessionStart`,
`userPromptSubmitted`, `agentStop`, `sessionEnd`). Tool spans are NOT built from
`postToolUse` hooks — those carry no duration (the spans would be zero-length
instants) and never fire inside sub-agents. Instead, at each `agentStop` the
plugin reads the turn's `execute_tool` spans from the **native-OTel file** and
re-emits them onto the turn's trace: native span ids and real start/end times
are kept, tool args/results flow through the same `omit_io` redaction as the
other runtimes, and a failed tool carries the native error status.

Sub-agents (spawned via the `task` tool) fire their own hook lifecycles under a
**synthetic `session_id` = `call_<toolCallId>`**, with no field linking back to
the parent conversation (verified against captured payloads). The normalizer
(`internal/source/copilot/copilot.go`) **drops every `call_`-prefixed session**
so they never mint spurious, token-less conversations. Sub-agent work still
lands in the parent conversation via the OTel file:

- **Sub-agent tokens roll into the parent turn** (flat attribution): their
  native `chat` spans share the parent's `gen_ai.conversation.id`, so the
  parent's `agentStop` sums them.
- **Sub-agent tool calls ARE emitted**, nested under their spawning `task`
  span (the native `invoke_agent` layers are collapsed): the OTel span tree is
  `execute_tool task → invoke_agent task → execute_tool bash/…`, and the plugin
  re-parents the inner tools to the `task` span. Membership is resolved via the
  shared native `traceId` (execute_tool spans carry no conversation.id).
- The `task` span itself is labeled with the instance name
  (`dash0.gen_ai.tool.task.name`, e.g. `echo-runner`) and the sub-agent's result
  summary (`gen_ai.tool.call.result`).
- **Sub-agent completion notices** arrive back in the parent as a synthetic
  `userPromptSubmitted` wrapped in `<system_notification>` (e.g. `Agent "x" (task)
has finished processing…`). The normalizer tags these `prompt_role: assistant`
  so the chat span renders them as an assistant-role input message, not as
  something the user typed.

## Remaining limitations

- **Sub-agent chat rounds are not separate spans** — their usage is summed into
  the parent turn's chat span (flat attribution), not shown per sub-agent.
- **Late flushes fold into the next turn**: a native span written after the
  `agentStop` read lands in the next turn's window and is emitted there (parented
  to that turn's chat span). Graceful, slightly misattributed, rare — tool spans
  normally flush before the turn's final chat round-trip.
- **No native-OTel file → no tool spans**: without the launch function (native
  OTel disabled), only lifecycle chat spans are emitted, without usage or tools.
- **No line-count metrics for Copilot file edits**: `dash0.gen_ai.code.lines_added`
  / `lines_removed` are derived by `ExtractLinesCounts` from the `structuredPatch`
  Claude's `Edit`/`Write`/`MultiEdit` responses carry. Copilot's `apply_patch`
  (and Codex, same format) emits no such field — only the raw `*** Begin Patch`
  text in `gen_ai.tool.call.arguments` and a plain `"Modified N file(s): …"`
  result — so its edits are traced without line counts. A general fix (a
  patch-text extractor covering `apply_patch` and any other file-mutating tools)
  is deferred.
