---
id: first-turn-is-traced-with-its-subagent
area: copilot-app/subagents
runtime: copilot-app
status: draft
input: a new Copilot app session on a worktree with the extension in .github/extensions/, first prompt sent through send_session_message
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

A fresh worktree session in the Copilot app, before its first prompt. Copy `copilot-app/` into the
worktree as `.github/extensions/dash0-agent-plugin/`. Give the worktree a
`.copilot/dash0-agent-plugin.local.md` with the target's `otlp_url`, `auth_token`, and `dataset`,
plus `omit_io: false`, `debug: "true"` and a `debug_file`. The project file replaces the user file
entirely, so a user-level `omit_io: true` does not leak in.

If the extension is already installed for the user, in `~/.copilot/extensions/`, skip the copy. A
second copy risks two extensions joining one session and every span arriving twice. Check that the
installed folder matches `copilot-app/` with `diff -r` instead, and rebuild the binary into the
bootstrap's cache as `copilot-app/README.md` describes.

The spec has to use **the first turn**, because that turn is the hard one. The app starts the
extension when the first prompt is sent, not when the session is created. In the reference run the
extension joined 70 ms after the first `user.message`. That prompt is already in the history by
then and is never delivered live, so the turn exists only if the extension rebuilds it from
`session.getEvents()`. A second turn would pass even with that recovery deleted.

## When

From another session:

```text
send_session_message <session-id>:
  Run `ls cmd` with the bash tool, and in parallel use an explore sub-agent to report the first
  line of FEATURE_MATRIX.md. Then reply in one short sentence. Do not edit any files.
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

**The agent kind and the models belong to the app.** Assert that `gen_ai.agent.name` is present,
and that the `invoke_agent` model equals the one in `subagent.started`. Never assert the literal
values.

**Extra tools are the app's.** `rename_branch` is one the app adds to the first turn of a worktree
session. Assert that every tool in the event log is in Dash0, never a count.

**Ingest lag: 25 seconds.** See `## Settling` in [../../../setup.md](../../../setup.md).
