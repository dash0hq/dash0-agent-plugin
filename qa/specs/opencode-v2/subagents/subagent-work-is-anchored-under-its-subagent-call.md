---
id: subagent-work-is-anchored-under-its-subagent-call
area: opencode-v2/subagents
runtime: opencode-v2
status: active
input: qa/tools/qa-session-opencode-v2.sh, one turn that delegates one file read to a sub-agent
duration: ~60s
settling: 25s
cleanup: keep
covers:
  - opencode-v2/index.js
  - internal/source/opencodev2/opencodev2.go
---

## Given

The driver's defaults. The driver exports every child session it finds linked from the parent's
tool parts into `session-export-<id>.json`, and records their ids in the manifest's
`subagent_sessions`.

## When

```sh
qa/tools/qa-session-opencode-v2.sh \
  'Use the subagent tool to delegate this task to a general sub-agent: read README.md and report its first line. Then reply with exactly the word done.' \
  spec-opencode-subagent
sleep 25
```

## Expectation

From `internal/source/opencodev2/opencodev2.go`:

- The parent's `subagent` tool call emits `session.tool.progress` with the child's session id in
  its metadata. That records an anchor: the parent's trace, the tool span's id and the parent's
  conversation id.
- The child session's turn joins that anchor, even if the progress event arrives after the child
  started. It is exported as `invoke_agent <agent>` rather than `chat`, parented on the parent's
  `execute_tool subagent` span, in the parent's trace, with the parent's `gen_ai.conversation.id`.
- The child's tool calls are `execute_tool` spans under the child's span.
- The child's tokens stay on the child's span. The parent's `chat` span counts only the parent's
  own steps.

## Oracle

- `qa/tools/qa-compare.py qa/runs/spec-opencode-subagent`. It expects one `invoke_agent` per child
  turn from the child exports, adds the child's tool parts to the tool expectation, and compares the
  child's per-turn tokens with the `invoke_agent` spans and the parent's with the `chat` span.
- `dash0 spans query` filtered to the session, for trace ids and parents.

## Then

- The manifest lists at least one `subagent_sessions` id. If it lists none, the model did not
  delegate; re-run rather than reporting.
- `qa-compare.py` exits `0`: the `chat`, `invoke_agent` and `execute_tool` rows and both per-turn
  token rows agree.
- Every span shares one trace id.
- The `invoke_agent` span's parent is the `execute_tool subagent` span, and its
  `gen_ai.conversation.id` is the parent session's id.
- The child's `execute_tool read` span is parented on the `invoke_agent` span.
- The child's `execute_tool read` span carries `gen_ai.agent.name` equal to the `invoke_agent`
  span's agent (`general`) and `gen_ai.agent.id` equal to the child session id. The parent's spans
  carry `opencode`. A tool span with no `agent_type` falls back to the configured name, which would
  make the sub-agent's work read as the parent's.

## Tolerance

**The tool may not be called `subagent`.** If OpenCode names it differently, the anchor code, which
matches the name `subagent`, never fires. Then the child's turn would be exported as an unanchored
`chat`, which is a defect, not a tolerance.

**The free model may refuse to delegate.** Use `QA_MODEL` with a stronger model before concluding
anything.
