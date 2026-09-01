---
id: tool-failure-sets-the-span-status
area: cursor/session
runtime: cursor
status: active
input: qa/tools/qa-session-cursor.sh, one turn whose only tool call is a shell command that exits 3
duration: ~40s
settling: 25s
cleanup: keep
covers:
  - internal/source/cursor/cursor.go
  - internal/pipeline/pipeline.go
  - internal/otlp/otlp.go
---

## Given

The same setup as
[single-turn-agrees-with-the-hooks-that-fed-it](single-turn-agrees-with-the-hooks-that-fed-it.md),
with one prompt: a shell command that exits non-zero.

**This spec exists because the equivalent one cannot be written for Copilot.** There, `exit 3`
produces a `postToolUse` with `"resultType": "success"` and a native span with no error status, so
the obvious probe reports a defect that is not one — see
[copilot-non-zero-shell-exit-is-not-a-failed-tool](../../../learnings/copilot-non-zero-shell-exit-is-not-a-failed-tool.md).
Cursor treats the same command as a failed tool: it fires `postToolUseFailure` and puts a human
message in `error_message`. So the failure path is reachable here with a one-line prompt, and this is
the runtime that covers it.

**The prompt must forbid a retry.** A model that investigates the failure runs more tools, and the
span set stops being one thing.

## When

```sh
QA_CURSOR_BINARY=working-tree qa/tools/qa-session-cursor.sh \
  'Run the shell command: exit 3. Do not retry it or investigate. Then reply with exactly the word done.' \
  spec-cursor-tool-failure
sleep 25
```

Shape, measured 2026-09-01 on `qa/runs/probe-cursor-tool-failure` against cursor-agent
2026.08.31-4057e58: 6 hook invocations, with `postToolUseFailure` where the healthy run has
`postToolUse`, and 2 spans in Dash0. The payload carried
`"error_message": "Command failed with exit code 3"`, `"failure_type": "error"` and
`"duration": 976.35`, and **no `tool_output`**.

## Expectation

**`postToolUseFailure` becomes `PostToolUseFailure`** through `hookNameMap`, and the pipeline sends
the tool trace with its failure flag set. One `execute_tool` span, not zero and not two.

**`error_message` becomes `error`** through `Normalize`'s rename, which is what the OTLP layer reads
to build the status and `exception.message`.

**The span status is ERROR (code 2), and its message is Cursor's text verbatim.** Measured:
`{"code": 2, "message": "Command failed with exit code 3"}` on the `execute_tool` span, with
`exception.message` carrying the same string.

**The turn's own `chat` span is not an error.** The tool failed; the turn did not. A model that
handles a failed tool and answers has produced a successful turn, and an ERROR status on the chat
span would make every recovered failure look like a broken session.

**`failure_type` must not reach the span.** It says `"error"`, which the status and
`exception.message` already say, and it arrives unnamespaced. It is the one of the five raw Cursor
leaks a healthy probe cannot reach, because it is on the failure payload only — which is why this
spec is the one that runs `qa-attrs.py` on the failure path as well.

## Oracle

- Channel one, Dash0: `qa/tools/qa-compare.py qa/runs/spec-cursor-tool-failure`, then
  `dash0 spans query` filtered to `gen_ai.conversation.id`, reading the status and
  `exception.message` off the `execute_tool` span.
- The attribute surface: `qa/tools/qa-attrs.py qa/runs/spec-cursor-tool-failure`. This is the half
  that catches `failure_type`, and no count-based check can.
- The hook record: the `postToolUseFailure` payload in `record/events/`, which holds the exact
  `error_message` the status must carry.
- `plugin-debug.log` holds the span as it was built, including its status, before the wire.

## Then

- Dash0 holds exactly 2 spans: 1 `chat`, 1 `execute_tool`.
- The `execute_tool` span's status code is `2` and its message is
  `Command failed with exit code 3`.
- `exception.message` on that span is the same string.
- `gen_ai.tool.name` is `Shell`.
- The `chat` span's status code is `0`.
- No span carries `failure_type`.
- Dash0 adds its own `dash0.error.fingerprint.*` family to the failed span at ingest. That is
  expected and `qa-attrs.py` lists it under "added at ingest"; it is not an export.
- `qa-compare.py` exits `0`, and `qa-attrs.py` exits `0`.

## Tolerance

**Cursor's wording is Cursor's.** `Command failed with exit code 3` is what 2026.08.31 writes. The
assertion is that the span's message equals whatever the payload's `error_message` held, not that
particular sentence.

**A retry breaks the count, not the claim.** If the model investigates, expect several
`execute_tool` spans and check that the failing one carries the ERROR status; the others must not.

**`tool_output` is absent on a failure payload**, so `gen_ai.tool.call.result` is absent on the span.
Do not read that as a lost field.

**A different failing input may not reach this path.** A tool that fails at the *tool* level and a
command that fails at the *shell* level are the same thing on Cursor and different things on
Copilot. Do not carry this spec across.
