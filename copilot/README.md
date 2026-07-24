# Dash0 for GitHub Copilot CLI

OpenTelemetry observability for [GitHub Copilot CLI](https://github.com/github/copilot-cli) sessions, emitted to [Dash0](https://dash0.com) as **canonical per-turn spans** — the same span shape as the Claude Code and Cursor runtimes (tool spans, chat spans with per-turn token/cost/model usage). No backend correlation required.

## How it works

Copilot CLI ships its own native OpenTelemetry, which carries the full token/cost/model detail. This plugin uses that as a **local file** token source rather than rebuilding it:

```
launch function ─ enables native OTel → per-session file (local only)
        │
   copilot hooks (camelCase) ─▶ copilot-on-event ─▶ internal/pipeline ─▶ Dash0
        sessionStart / userPromptSubmitted / postToolUse / agentStop / sessionEnd
        at each agentStop: read the file for this turn's chat spans → attach
        gen_ai.usage.* + cost + model → emit a canonical chat span
```

The hooks build the canonical span tree; the native-OTel file supplies per-turn tokens. Dash0 sees one uniform schema across all three runtimes.

## Install

```bash
copilot plugin install dash0hq/dash0-agent-plugin:copilot
```

> Use the `:copilot` subpath. A bare `copilot plugin install dash0hq/dash0-agent-plugin`
> loads only the Claude Code skills/commands (the root manifest declares no
> hooks) — no telemetry.

Restart `copilot` after installing (hooks load at startup).

## Configure

```
/dash0-configure
```
This writes your Dash0 credentials to `~/.copilot/dash0-agent-plugin.local.md` **and** installs a launch shell function that shadows `copilot` to enable native OTel into a per-session file. Open a new shell afterward.

**Per-turn token/cost/model requires the launch function.** A `copilot` started from a shell without it still emits canonical spans — just without usage data (graceful).

## Team rollout

Enable for a repo's contributors via `.github/copilot/settings.json`:
```json
{ "enabledPlugins": ["dash0-agent-plugin"] }
```
Distribute the launch shell function through your team's shell provisioning (dotfiles, devcontainer).

## Notes

- **Prompt mode** (`copilot -p`) fires the hooks for user-installed plugins, so headless runs are instrumented (when launched via the function).
- **Updating**: after a version bump, `copilot plugin update dash0-agent-plugin`.
- Telemetry is fail-open: a broken exporter, missing config, or absent OTel file never breaks your Copilot session.

## Development

### Lcal e2e tests with a PAT

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

## Sub-agent handling & limitations

Copilot spawns sub-agents via the `task` tool. Each sub-agent runs a **full,
independent hook lifecycle** (`userPromptSubmitted` → `postToolUse` → `agentStop`)
under a **synthetic `session_id` = `call_<toolCallId>`** — distinct per sub-agent,
with **no field in the hook payload linking back to the parent conversation**
(verified against captured payloads).

Because the plugin keys traces on `session_id`, each `call_` session would
otherwise mint its own trace — a spurious, token-less "conversation" per
sub-agent. So the normalizer (`internal/source/copilot/copilot.go`) **drops every
`call_`-prefixed session** wholesale. Net behavior:

- **One conversation per real turn** — no per-sub-agent conversations.
- **Sub-agent tokens roll into the parent turn** (flat attribution): their
  native-OTel `chat` spans share the parent's `gen_ai.conversation.id`, so the
  parent's `agentStop` sums them via the OTel reader.
- **Each spawn shows as a `task` `execute_tool` span** on the parent, labeled with
  the instance name (`dash0.gen_ai.tool.task.name`, e.g. `echo-runner`) and the
  sub-agent's result summary (`gen_ai.tool.call.result`).

### Shortcoming: sub-agent internals are not nested

A sub-agent's **own inner spans** (its `chat` turns and tool calls) are **not
emitted** — you see the parent's `task` tool span and its result, not the work
inside. True nesting is **not achievable from the hook stream**:

- The only parent↔sub-agent link lives in Copilot's native-OTel file: the
  `execute_tool task` span's `gen_ai.tool.call.id` equals the sub-agent's hook
  `session_id` (`call_X`), and that span's tree ancestor carries the parent
  `gen_ai.conversation.id`.
- **Timing blocks real-time re-parenting:** the `execute_tool task` span is only
  written when the task *returns* — after the sub-agent's own hooks have already
  fired. At hook time the linking span does not yet exist.

Nested sub-agent traces would therefore require **reconstructing the sub-agent
OTel subtree at parent-task-completion** and re-emitting it (a new capability — the
plugin currently reads only usage/model/messages from OTel, never whole span
trees), plus a fuzzy hook↔OTel match (the parent's `task` hook carries no
`call_X`; it would match the OTel span by `toolArgs.name` + timing). Judged not
worth the cost/fragility and **deferred**.

