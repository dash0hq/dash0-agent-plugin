## Context

See proposal.md — Why. This section covers only what shapes the approach.

**The shared pipeline is a stdin-JSON contract.** All four existing runtimes are
`stdin (JSON) → <runtime>-on-event.sh → <runtime>-on-event binary → OTLP`. The
binary reads one event, normalizes it into a canonical vocabulary inherited from
Claude Code (`SessionStart`, `UserPromptSubmit`, `PreToolUse`, `PostToolUse`,
`PostToolUseFailure`, `Stop`, `StopFailure`, `SubagentStart`, `SubagentStop`,
`SessionEnd`), and hands it to `pipeline.Process`, which owns trace-context
lifecycle, span construction, and export.

**OpenCode has no such mechanism.** Plugins are in-process TypeScript modules
(`@opencode-ai/plugin`) that Bun loads from `~/.config/opencode/plugin/`,
`.opencode/plugin/`, or an npm package named in `opencode.json`'s `plugin`
array. A plugin returns a `Hooks` object and receives an event-bus stream
(`event`), lifecycle hooks (`chat.message`, `tool.execute.before/after`), an
authenticated SDK `client`, and the project's `directory` / `worktree`.

**OpenCode is the only runtime that runs headless.** `opencode run "<prompt>"`
executes a full turn non-interactively, and `provider.<id>.options.baseURL`
points a provider at an arbitrary OpenAI-compatible endpoint. Together those
make a real end-to-end test possible without a real model, which shapes the
testing strategy in Decision 10.

**Pipeline mechanics this design must respect** (verified in
`internal/pipeline/pipeline.go`):

- A trace id and the turn's chat span id are allocated on `UserPromptSubmit`
  when `agent_id` is empty, and persisted to `<dataDir>/<session_id>/`.
- `Stop` emits the chat span and then **clears** the trace context. Any tool
  span arriving afterwards has no parent and is dropped.
- Tool spans load the session context and parent to `ctx.SpanID`, except when
  the event carries `agent_id` — then they parent to
  `SpanIDFromAgentID(agent_id)`, which is deterministic.
- `SubagentStart` snapshots the session context under the agent id and records
  the start time; `SubagentStop` emits the `invoke_agent` span from that
  snapshot. `agent_type` is what turns a chat span into an `invoke_agent` span.
- Tool-span duration comes from `duration_ms` on the event, subtracted from the
  hook timestamp.
- `eventAttributes` passes any event key that is not in its skip list straight
  through to the span. A normalizer can therefore set
  `gen_ai.conversation.name`, `gen_ai.usage.*`, and
  `gen_ai.usage.reasoning.output_tokens` directly — as `cmd/copilot-on-event`
  already does — with no change to `internal/otlp`.
- Scratch state is keyed by `session_id`, which must be a filename-safe segment.

**OpenCode data available** (from `@opencode-ai/sdk` types, v1.14/1.18):
`AssistantMessage` carries `modelID`, `providerID`, `mode`, `cost`, `time`, and
`tokens: {input, output, reasoning, cache: {read, write}}`. `ToolPart` carries
`callID`, `tool`, and a `state` with `input`, `output`, `title`, `metadata`, and
`time: {start, end}`. `Session` carries `id`, `parentID`, `title`, `directory`.
`client.tui.showToast` renders a user-visible notification.

## Goals / Non-Goals

**Goals:**

- Byte-for-byte comparable span output with the other runtimes for the same
  logical session — one Go pipeline, no second emitter.
- Reuse the existing per-runtime scaffolding rather than inventing a parallel
  one: the wrapper, the capture harness, the golden tests, the consistency
  tests, the contract tests, and the mock OTLP server all already exist.
- The TS plugin is a translator and transport only. No span construction, no
  OTLP knowledge, no config parsing, no binary management.
- Fail open at every layer, including against OpenCode plugin-API drift.
- Prove the whole chain with a real agent process, not only with synthesized
  payloads.

**Non-Goals:**

- Changing the shared span contract in `DEVELOPMENT.md#telemetry-attributes`.
- Backporting OpenCode-only signals (reasoning tokens, real MCP server names) to
  the other runtimes.
- Billing mode, rate-limit windows, or credits — OpenCode is BYO-key, so the
  Codex-only allowance attributes have no analogue.
- Deduplicating the four existing bash wrappers. See Decision 6.
- A long-lived exporter process. See Decision 2.

## Decisions

### 1. The TS plugin normalizes; the existing Go + bash chain does everything else

The full path is:

```
opencode event bus
  → TS plugin (filter + translate)
  → opencode/opencode-on-event.sh   (config, keychain, download, checksum)
  → opencode-on-event binary        (normalize → pipeline.Process)
  → OTLP
```

Only the first box is new in kind. Normalization that needs no live OpenCode
objects stays in Go, in `internal/source/opencode`, so it is unit-testable
against golden spans like the other runtimes; the TS side does only what
requires the in-process SDK types.

*Alternatives:* A pure-TS OTLP exporter was rejected — it would duplicate ~2000
lines of attribute, redaction, VCS, and identity logic and drift from the other
four the first time either side changed, which is exactly the parity the request
is about.

### 2. One process spawn per consumed event, not a sidecar

The plugin spawns the wrapper per canonical event, fire-and-forget, and never
awaits it. OpenCode's bus is chatty (`message.part.updated` fires on every
streaming delta), so **the plugin filters first**: only the events in the
mapping table below cause a spawn — roughly 3–8 per turn, the same order as
Claude Code's hook count.

*Alternatives:* A long-lived sidecar fed NDJSON would cost less per event and is
easier here than elsewhere (the plugin is itself long-lived), but it needs a new
streaming mode in the Go binary, process supervision, crash recovery, and a
shutdown flush — new failure modes in the path whose first requirement is not to
break the session. Deferred: if profiling shows spawn cost matters, the stdin
contract is unchanged and a sidecar is a drop-in replacement for the transport.
Batching until session end was rejected outright: it delays every span to the
end of the session and loses everything on a crash.

### 3. Event mapping

| OpenCode | Canonical | Notes |
|---|---|---|
| `session.created`, no `parentID` | `SessionStart` | Carries `model` when known. |
| `chat.message` hook, root session | `UserPromptSubmit` | Allocates the turn's trace. Carries prompt text and model. |
| `session.created`, has `parentID` | `SubagentStart` | `agent_id` = child session id. |
| `message.part.updated`, `part.type=tool`, `state.status` ∈ {`completed`,`error`} | `PostToolUse` / `PostToolUseFailure` | Deduped by `callID`. `duration_ms` from `state.time.end - state.time.start`. |
| `session.idle`, root session | `Stop` | Emits the chat span with the turn's aggregated usage. |
| `session.idle`, child session | `SubagentStop` | `agent_id` + `agent_type`. |
| `session.error`, root session | `StopFailure` | `error` from the reported message. |
| `session.error`, child session | `SubagentStop` | `error` on the sub-agent's span only; a `StopFailure` would clear the parent turn's trace context. |
| plugin shutdown | `SessionEnd` | Frees the scratch dir. |
| everything else | dropped | Including all streaming part updates. |

`PreToolUse` is not emitted: OpenCode reports real tool start and end times, so
the pipeline needs no reconstruction, and skipping it halves the spawn count.

*Alternative for tool spans:* the `tool.execute.before` / `after` hooks fire
exactly once and need no dedupe, but `after` does not run when a tool throws and
carries no timings — that would split success and failure across two different
sources with different fidelity. One source (`message.part.updated` at a
terminal status) keeps both paths identical; the dedupe cost is a `Set` of
call ids.

### 4. Child sessions collapse onto the root session id

OpenCode gives a delegated sub-agent run its own `Session` with its own id and a
`parentID`. The pipeline keys all scratch state — trace context, agent
snapshots, the event log — by one `session_id` per conversation, and expresses
sub-agent structure through `agent_id` instead. So the plugin resolves each
event's **root** session by walking `parentID` and emits:

- `session_id` = root session id (so all state and spans share one trace),
- `agent_id` = the child session id (so `SpanIDFromAgentID` parents the
  sub-agent's chat and tool spans correctly),
- `agent_type` = the child's agent mode, taken from its assistant messages'
  `mode` field.

The parent chain is resolved once per session id and cached in the plugin, so
the common case costs no API call. OpenCode session ids are already
filename-safe, satisfying the pipeline's path-segment check.

*Alternative:* letting each child session be its own `session_id` was rejected —
it produces one disconnected trace per sub-agent, which is the Cursor limitation
this port is meant to avoid.

### 5. One chat span per turn, with usage summed across steps

`Stop` clears the trace context, so exactly one `Stop` may be emitted per turn.
A turn can contain several assistant messages (tool-use steps), each with its
own usage. The plugin accumulates per-session usage from `message.updated`
events with `role=assistant` and a set `time.completed`, and on `session.idle`
emits one `Stop` carrying the sums, the last assistant text, and the model.

*Alternative:* one chat span per assistant message maps more literally onto "one
LLM call", but the first `Stop` would clear the context and orphan the turn's
remaining tool spans, and multi-step turns would fragment against how the other
four runtimes report the same work. Copilot already folds its rounds into the
parent turn, so summing is the established behavior.

The accumulator is in-memory. If OpenCode exits mid-turn the counts are lost and
no chat span is emitted for that turn — consistent with the fail-open rule, and
the `SessionEnd` fallback still closes an open trace.

### 6. Config, keychain, and binary bootstrap stay in a shell wrapper

`opencode/opencode-on-event.sh` is a clone of `claude/claude-on-event.sh` — the
one wrapper that already implements keychain resolution — and owns frontmatter
parsing, project-over-user precedence, `security find-generic-password` lookup,
OS/arch detection, release download, checksum verification, and fail-open. The
plugin writes canonical JSON to its stdin and knows none of this.

This is a fifth near-duplicate of a script that already exists four times
(`cursor-on-event.sh` and `codex-on-event.sh` differ by 39 diff lines out of
133, mostly comments), and that duplication is real. It is still the better
option: the alternative was a fifth copy **in a second language**, which would
put config precedence, secret handling, and checksum verification outside
`make shellcheck-lint` and outside `test/contracts/*.sh`, the harness that
already pins exactly this behavior for the other three runtimes. Reusing the
wrapper means `test/contracts/opencode.sh` is a clone too, and the TS plugin
shrinks to event translation.

*Alternative:* centralizing the wrappers removes the duplication for real and is
the right eventual answer, but it is a cross-runtime refactor touching four
shipping plugins and their install contracts, so it belongs on its own change
rather than riding along with a new runtime. Two viable shapes, both scoped out
here: a sourced `scripts/on-event-lib.sh` holding the ~90 shared lines
(frontmatter parsing, OS/arch, download, checksum, fail-open) — the wrappers are
always invoked by absolute path so `$(dirname "${BASH_SOURCE[0]}")` resolves it,
but `install-codex.sh` downloads a single raw file today and would have to
deliver two; or build-time generation from one template, which keeps every
delivered wrapper self-contained and changes no install contract at the cost of
a codegen step. What stays per-runtime either way: config paths, the secure env
var name, the data-dir chain, the binary name, Claude's legacy-asset fallback and
keychain lookup, and the fail-open-versus-exit-1 difference.

Config paths are `.opencode/dash0-agent-plugin.local.md` (project) and
`~/.config/opencode/dash0-agent-plugin.local.md` (user).

### 7. Harness value

`harness.OpenCode = {Name: "opencode", EnvPrefix: "OPENCODE", DataSubdir:
"opencode", Provider: ""}`. `Provider` is empty like Cursor and Copilot:
OpenCode is BYO-key across many vendors, so the provider is resolved per event
from the model id and must not be forced to a default.

### 8. Distribution: one plugin artifact, two paths

The release publishes `dash0-opencode-plugin.js` (bundled, no runtime deps)
alongside the five Go binaries. The npm package `@dash0/opencode-plugin`
contains that file plus `opencode-on-event.sh`; `install-opencode.sh` downloads
both, putting the plugin in `~/.config/opencode/plugin/` where OpenCode
auto-loads it. `uninstall-opencode.sh` removes the plugin, the wrapper, and the
cached binaries and leaves the user's config file alone.

The binary version the wrapper requests is stamped at build time from the
release tag, exactly as `VERSION="0.1.24"` is stamped into the other four, and
the npm package version equals the Go release version.

### 9. User notification via TUI toast

`client.tui.showToast` renders the `dash0: connected → <session link>` banner
that `pipeline.Process` already produces on `SessionStart`. In headless mode
(`opencode run`) the call fails and is swallowed; the session is unaffected.

### 10. Testing: reuse the four existing layers, add two OpenCode-only ones

The repo already has four test layers, and OpenCode slots into all of them
unchanged:

| Layer | Existing pattern | OpenCode instance |
|---|---|---|
| Unit | per-package `*_test.go` | normalizer, harness value |
| Golden | `internal/source/codex/golden_test.go` | replay `captured_events.jsonl` → `golden_spans.json` |
| Consistency | `test/consistency/{codex,copilot}_test.go` | attribute-key parity against Claude Code |
| E2E (mock OTLP) | `test/e2e/copilot_e2e_test.go`, tag `e2e` | canonical events → binary → `httptest` collector |
| Contract (install/config) | `test/contracts/{claude,cursor,codex}.sh` + `lib.sh` | `opencode.sh`: creds → OTLP, install layout, uninstall strip |

Those five prove the *pipeline* is right. None of them prove the *plugin loads
and the events actually fire*, because until now no runtime could be driven
without a human. OpenCode can, so two layers are added:

**(a) Hermetic live session, in CI.** `opencode run "<prompt>"` under a
throwaway `HOME`/`XDG_STATE_HOME`, with:

- a **mock OpenAI-compatible LLM** on localhost, wired in via
  `provider.<id>.options.baseURL`, scripting one deterministic turn: assistant
  text, a tool call, a failing tool call, a delegated sub-agent, and exact
  usage numbers including cache read/write and reasoning;
- the **existing mock OTLP server** (`test/e2e/mock-otlp-server`, already
  started by `test/contracts/lib.sh`'s `start_mock_otlp`).

Because the model response is scripted, the expected spans are exact: the test
asserts span names, parent/child structure, and **token values**, not just
presence. It needs no API key, no cost, and no network beyond localhost. It is
the only layer that exercises the real plugin, the real wrapper, and the real
binary together in one process tree. `opencode` on the runner is a precondition;
absent, it follows the existing rule — skip locally, fail in CI.

**(b) Live Dash0 verification, in the dev loop and on release.** The same
scripted session, exported to the Dash0 dev ingress with a real token and a
dedicated dataset, then read back through the Dash0 MCP tools:

1. `listDatasets` — confirm the target dataset.
2. `getSpans` filtered `gen_ai.harness.name is opencode` and
   `gen_ai.conversation.id is <session id>`, over the run's time range —
   confirm the expected span set arrived.
3. `getTraceDetails` on the returned trace id — confirm the hierarchy: chat
   span at the root, tool spans beneath it, `invoke_agent` with the sub-agent's
   own tool spans beneath that.
4. `sql` (D0QL) — assert token sums and the presence of the identity, VCS, and
   redaction attributes across the session's spans.
5. `getAttributeKeys` scoped to spans — diff OpenCode's attribute key set
   against a Claude Code session's in the same dataset.

Step 5 is the point of doing this against a real backend at all. The golden and
consistency tests compare our output against our own expectations, so they
cannot catch a mapping that is wrong in the same way in both places; a key that
Dash0 never receives, or receives under the wrong name, only shows up here. The
recipe lives in `opencode/README.md` so it is repeatable, and the session id,
dataset, and time range go into the PR evidence.

*Alternative:* driving a real model in CI was rejected — non-deterministic
output means the test can only assert presence, not values, and it costs money
and a secret per run. Asserting only against the mock collector was rejected as
insufficient on its own for the reason in step 5.

## Risks / Trade-offs

- **OpenCode's plugin API is pre-1.0 and its `Hooks` surface moves between minor
  versions.** → Pin a supported version range, treat every field as optional at
  runtime, wrap each handler so a shape mismatch drops one event instead of
  breaking the plugin, and cover the mapping with golden tests over captured
  real events.
- **Three mapping assumptions are inferred from types, not observed.** Whether
  `session.idle` fires for child sessions, how OpenCode names MCP-provided
  tools, and whether `message.part.updated` can deliver a terminal status more
  than once. → The first implementation task is capturing a real session's event
  stream to `testdata` (as `internal/source/codex` already does) and confirming
  all three before the normalizer is written. Each has a cheap fallback:
  `session.idle` → the child's last assistant message; MCP naming → rewrite to
  the canonical `mcp__<server>__<tool>` form the shared extractor expects;
  repeated terminal states → the call-id dedupe set already handles it.
- **The mock-LLM approach assumes OpenCode accepts an OpenAI-compatible provider
  with a `baseURL` and no real key.** The config types carry
  `provider.<id>.options.{baseURL,apiKey}`, but this is unconfirmed in practice.
  → Confirmed in the capture task before the live harness is built. If it does
  not hold, the live layer falls back to the cheapest real model behind a
  repo secret and asserts structure rather than exact token values, and the
  exact-value assertions move to the E2E layer.
- **Per-event spawn on a chatty bus.** → The filter is the mitigation, and it is
  testable: a golden test asserts the exact spawn count for a recorded session.
- **In-memory usage accumulator loses a turn's tokens on a crash.** → Accepted.
  Persisting it would mean a second state store beside the pipeline's own.
- **A fifth near-duplicate wrapper.** → Accepted over a fifth copy in a second
  language (Decision 6); the Go `bootstrap` extraction is the follow-up.
- **npm publishing adds supply-chain surface and an `NPM_TOKEN` secret.** →
  Publish from the existing tagged release workflow only, with provenance, and
  keep the script path working so npm is never the single point of failure.
- **`audit-usage` does not port.** It reads Claude's JSONL transcripts through a
  Python tool; OpenCode keeps messages in its own storage. → It is rewritten
  against OpenCode's storage, which is the largest non-telemetry item in this
  change and the one most likely to be cut if scope needs trimming. Its data
  source must be confirmed during the capture task.
- **The live Dash0 verification needs credentials and pollutes a dataset.** →
  It runs in the dev loop and before a release, not on every CI run, against a
  dedicated dataset that can be dropped.

## Migration Plan

No existing installs, so there is nothing to migrate. Rollout is: capture, then
the Go side (harness value, entrypoint, normalizer) with tests, then the wrapper,
then the plugin, then the install paths, then the live layers, then docs. The
first release that carries an `opencode-on-event` asset is the first one the
wrapper can pin.

Rollback: deprecate the npm version and revert the installer's pinned version.
Existing installs keep working against the release they pinned, exactly as the
other runtimes do.

## Open Questions

- Minimum supported OpenCode version. Resolve against the oldest release whose
  `@opencode-ai/plugin` types carry the fields the mapping needs; it changes the
  `peerDependencies` range and a README line, nothing else.
- Whether the toast should also carry the "telemetry is not active" message the
  pipeline produces when no OTLP URL is configured, or whether that is noise for
  a user who has not opted in. Affects one branch in the notification handler.
