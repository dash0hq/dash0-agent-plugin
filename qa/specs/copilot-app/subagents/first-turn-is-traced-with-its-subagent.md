---
id: first-turn-is-traced-with-its-subagent
area: copilot-app/subagents
runtime: copilot-app
status: draft
input: a new target session, first prompt sent through send_session_message
duration: ~15s
settling: 25s
cleanup: keep
covers:
  - copilot-app/extension.mjs
  - internal/source/copilotapp/copilotapp.go
  - internal/source/copilot/emit.go
  - cmd/copilot-app-on-event/main.go
---

## Given

The runner procedure in [../README.md](../README.md), with a real model and without `--omit-io`.

The spec has to use **the first turn**, because that turn is the hard one. The app starts the
extension when the first prompt is sent, not when the session is created. In the reference run the
extension joined 70 ms after the first `user.message`. That prompt is already in the history by
then and is never delivered live, so the turn exists only if the extension rebuilds it from
`session.getEvents()`. A second turn would pass even with that recovery deleted.

## When

```text
send_session_message <session-id>:
  Run `ls cmd` with the bash tool, and in parallel use an explore sub-agent that opens
  FEATURE_MATRIX.md with its view tool and reports the file's first line verbatim. Then reply in
  one short sentence. Do not edit any files.
```

Wait for the session to go idle, then wait out the settling time.

Shape, measured on 2026-10-02 against the working tree: one `chat` span, four `execute_tool` spans
(`bash`, `task`, `view`, and the app's own `rename_branch`), and one `invoke_agent` span.

## Expectation

**From `events.jsonl`, independently:** one main-agent `user.message`, and one `subagent.started`
whose `toolCallId` is the `task` call's id and whose `model` names the sub-agent's model. Every
`tool.execution_start` the sub-agent ran carries `parentToolCallId` equal to that id.

**The span tree in Dash0**, ids elided:

| Span | Parent | Notes |
| --- | --- | --- |
| `chat claude-sonnet-5` | none | `gen_ai.input.messages` is the prompt above |
| `execute_tool bash` | the `chat` span | |
| `execute_tool task` | the `chat` span | `gen_ai.tool.call.id` is the spawning call id |
| `invoke_agent explore` | the `task` span | `gen_ai.request.model` is `gpt-5.6-luna`, the sub-agent's model, not the parent's |
| `execute_tool view` | the `invoke_agent` span | ran inside the sub-agent |

## Oracle

- Dash0: `dash0 spans query` filtered to `gen_ai.conversation.id` = the session id. Read each span's
  `spanId` and `parentSpanId` to rebuild the tree. The API returns `gen_ai.input.messages` as
  `<REDACTED>`, so it proves that the key exists and nothing about its content.
- The event log: `~/.copilot/session-state/<session-id>/events.jsonl`, for the tool calls, the
  sub-agent and its model.
- The debug log, for tokens and for the `chat` span's input. It is the plugin's own output, so it
  shows what was sent, not whether it was right.

## Then

- Dash0 holds **exactly one** `chat` span for the session, and in the debug log its input is the
  first prompt. Zero
  means the late join was not recovered. Two means the recovered events were buffered twice.
- Every tool call in `events.jsonl` appears as an `execute_tool` span, once.
- `execute_tool task` is parented on the `chat` span.
- `invoke_agent` is parented on the `task` span, and `execute_tool view` is parented on the
  `invoke_agent` span.
- `invoke_agent` carries `gen_ai.agent.name` (the agent kind) and `gen_ai.agent.id` equal to the
  `task` span's `gen_ai.tool.call.id`.
- `invoke_agent` carries the model from `subagent.started`, which here differs from the `chat`
  span's model.
- `invoke_agent` carries `gen_ai.usage.*`: the sub-agent's own tokens, at its own model. They are
  not also in the `chat` span, so summing the trace counts each token once. Read the figures from
  the debug log. No record outside the plugin carries tokens, so this checks where they were put
  and not whether they are right.
- **No span** has a `gen_ai.conversation.id` other than the session id. The sub-agent's hooks fire
  into the extension under the sub-agent's own id, and the extension ignores them.

## Tolerance

**The model may not delegate.** Without a `task` call there is no sub-agent, and the run tests
nothing. Reword the prompt and re-run.

**The sub-agent may answer without a tool.** Measured 2026-10-06: asked only to "report the first
line", the explore agent guessed `# Feature Matrix` with no tool call, so the nested
`execute_tool` assertion had nothing to check. That run is inconclusive, not a pass. The prompt
above names the view tool for that reason. If `events.jsonl` shows no sub-agent tool call, re-run
in a fresh session.

**The agent kind and the models belong to the app.** Assert that `gen_ai.agent.name` is present,
and that the `invoke_agent` model equals the one in `subagent.started`. Never assert the literal
values.

**Extra tools are the app's.** `rename_branch` is one the app adds to the first turn of a worktree
session. Assert that every tool in the event log is in Dash0, never a count.

**Ingest lag: 25 seconds, and up to five minutes in this runtime.** The debug log is complete at once. When Dash0 holds fewer spans than it, re-query for up to five minutes before calling one missing. See `## Settling` in [../../../setup.md](../../../setup.md).
