# Dash0 OpenCode plugin

Sends OpenTelemetry traces for OpenCode sessions to Dash0.

> Under construction — see `openspec/changes/open-code-plugin/`.

## Observed OpenCode behavior

Findings from the capture harness in `test/capture/opencode/`, recorded against
**OpenCode 1.18.0**. The reference fixture is
`internal/source/opencode/testdata/captured_events.jsonl`. Re-run the capture
after an OpenCode upgrade — every mapping below is version-sensitive.

### Sub-agent lifecycle

`session.idle` **does** fire for child sessions. A delegated sub-task produces a
`session.created` whose `properties.info.parentID` is the parent session id, and
a matching `session.idle` for the child that arrives *before* the parent's own
`session.idle`. So `SubagentStop` maps directly to `session.idle` for any
session with a non-null `parentID`; the design's fallback (the child's last
completed assistant message) is not needed.

Delegation surfaces as an ordinary tool part with `tool: "task"`, and the child
session id is carried on `state.metadata.sessionId` (with
`state.metadata.parentSessionId` alongside it) from the `running` state onward.
This is what the plugin must use to synthesize the `tool_name: "Agent"` /
`tool_response: {"agentId": …}` event the pipeline needs in order to allocate
the sub-agent's parent span.

### MCP tool naming

OpenCode names an MCP-provided tool `<mcpServerKey>_<toolName>` — flat, with a
single underscore. A server configured under the `mcp.capture` key exposing a
tool named `echo` arrives as `capture_echo`, in both `ToolPart.tool` and the
`tool.execute.before` / `tool.execute.after` hook inputs.

The key is the **config key**, not the server's advertised `serverInfo.name`
(which was `dash0-capture-mcp` in the capture and appears nowhere in the tool
name). Since `_` is legal in both halves, the split is ambiguous in general; the
normalizer must resolve the server prefix against the configured MCP server
keys rather than splitting on the first underscore, then rewrite to the
canonical `mcp__<server>__<tool>` form that `ExtractMCPServer` and
`NormalizeMCPToolName` expect.

### Terminal tool-part updates

A terminal `message.part.updated` arrives **exactly once per `callID`**. Each
call progresses `pending` → `running` → `completed` | `error`, and in the
capture every one of the four calls had a single terminal-status event. The
`task` call emitted `running` twice (an intermediate metadata update), so
non-terminal statuses do repeat and the plugin must key deduplication on the
status, not on the part id.

One asymmetry worth noting: the `tool.execute.after` hook does **not** fire for
a failing call. `tool.execute.before` fires for all four calls but
`tool.execute.after` only for the three that completed, so `PostToolUseFailure`
has to come from the `error` part update rather than from a hook.

### On-disk session storage

Sessions, messages and parts live in a SQLite database at
`$XDG_DATA_HOME/opencode/opencode.db` (default
`~/.local/share/opencode/opencode.db`). The capture dumps the sandbox schema and
per-table row counts to `captured/storage.txt` before the sandbox is wiped, so
this is re-derivable from a run rather than from a long-lived local database.

1.18.0 writes `session`, `message` and `part`. The schema also declares a
`session_message` table — same columns as `message` plus `type` and `seq` — but
after a full capture run it holds **0 rows** while `message` holds 8 and `part`
holds 24. It is a not-yet-live successor; the `audit-usage` port must read
`message`, and must re-check this on every OpenCode upgrade because the
migration is visibly in flight.

Each `message` and `part` row keys on `id` / `session_id` and holds the full
record as JSON in a `data` column. The assistant `data` blob carries `cost`,
`modelID`, `providerID` and a `tokens` object (`total`, `input`, `output`,
`reasoning`, `cache.read`, `cache.write`). The `session` table denormalizes the
same totals into `cost` and `tokens_*` columns and carries `parent_id` for
sub-agent sessions — in the capture, the child session row's `parent_id` is the
parent session id and its token counts are its own, not the parent's.

Older OpenCode versions kept per-message JSON files under `storage/message/`.
A 1.18.0 run against a clean `XDG_DATA_HOME` writes no `storage/` directory at
all: the data dir holds only `opencode.db`, `log/` and `repos/`.

### Custom OpenAI-compatible provider

Confirmed: OpenCode accepts a custom provider declared as
`provider.<id>.options.baseURL` with a dummy `apiKey`, selected with
`opencode run --model <id>/<model>`. Pointed at a throwaway localhost server it
sends `POST /v1/chat/completions` with `Authorization: Bearer <apiKey>`
verbatim. The working declaration is in `test/capture/opencode/opencode.json`:
alongside `options`, it carries `api: "openai"` and an explicit `models` map
with a `limit`, so the model never has to be resolved against models.dev. With
that declaration OpenCode never requests `/v1/models`: the capture's request log
holds `POST /v1/chat/completions` and nothing else.

This unblocks the live-test layer; the fallback in the change's design Risks
section is not needed.
