---
id: resumed-turn-is-scoped-to-itself
area: cursor/turns
runtime: cursor
status: active
input: qa/tools/qa-session-cursor.sh with QA_CURSOR_RESUME, two turns each with one tool call
duration: ~70s
settling: 25s
cleanup: keep
covers:
  - internal/pipeline/pipeline.go
  - internal/source/cursor/cursor.go
---

## Given

The same setup as
[../session/single-turn-agrees-with-the-hooks-that-fed-it](../session/single-turn-agrees-with-the-hooks-that-fed-it.md),
with a second prompt typed into the session that is already open.

**Each turn must run a tool.** Without one, a turn produces a single span and the "turn 1's tool span
was not re-emitted" half of this spec has nothing to assert. That half is not hypothetical: it is
exactly what a stale read cursor did on Copilot.

**One session, two trace cycles.** `internal/pipeline` mints a trace and a chat span id at
`UserPromptSubmit`, parents the turn's tool spans on that id, sends the `chat` span at `Stop`, and
then clears the context. A second turn runs the cycle again, and the two turns must land in two
traces rather than one.

## When

```sh
QA_CURSOR_BINARY=working-tree \
QA_CURSOR_RESUME='Now run the shell command: echo qa-second. Then reply with exactly the word done.' \
  qa/tools/qa-session-cursor.sh \
  'Run the shell command: echo qa-first. Then reply with exactly the word done.' \
  spec-cursor-two-turns
sleep 25
qa/tools/qa-compare.py qa/runs/spec-cursor-two-turns
qa/tools/qa-transcript-cursor.py qa/runs/spec-cursor-two-turns
```

Shape, measured 2026-09-01 on `qa/runs/setup-probe-cursor-turns` against cursor-agent
2026.08.31-4057e58: 10 hook invocations in this order —

```
sessionStart beforeSubmitPrompt preToolUse postToolUse afterAgentResponse
             beforeSubmitPrompt preToolUse postToolUse afterAgentResponse sessionEnd
```

— 4 spans in Dash0, `"turns": 2` and `"spans_logged": 4` in the manifest.

## Expectation

**Two `chat` spans and two `execute_tool` spans**, one pair per turn, in **two different traces**.
Measured on the reference run: trace `83ad234059…` holding the first pair, trace `67f0d24ef0…`
holding the second, each `chat` span parentless and each `execute_tool` parented on its own turn's
chat span.

**Each turn's usage is that turn's.** The `afterAgentResponse` payloads carried:

| | input | output | cache_read | cache_write |
| --- | --- | --- | --- | --- |
| turn 1 | 32283 | 84 | 3456 | 0 |
| turn 2 | 32650 | 57 | 25856 | 0 |
| session | 64933 | 141 | 29312 | 0 |

Turn 2's `chat` span must carry turn 2's row. **The session row is there to be contradicted**: a
span carrying 64933 input is the double-count this spec exists for, and it is what a single-turn
probe cannot distinguish from correct behaviour.

**Turn 1's tool span is not emitted twice.** Exactly one `execute_tool` span carries turn 1's
`tool_use_id`, and it lives in turn 1's trace. Three `execute_tool` spans for two tool calls is the
Copilot regression's shape.

**Cursor scopes the usage, not the plugin.** This is the honest reading of the numbers above: each
`afterAgentResponse` payload arrives already scoped to its turn, so the plugin has nothing to
reconstruct and no read cursor to keep. The mechanism the Codex and Copilot equivalents guard does
not exist here. What this spec proves is that the plugin does not *un*-scope it — by summing, by
failing to clear the trace context, or by re-reading a payload.

## Oracle

- Channel one, Dash0: `qa/tools/qa-compare.py qa/runs/spec-cursor-two-turns`, then
  `dash0 spans query` filtered to `gen_ai.conversation.id`, reading `gen_ai.usage.*` and the trace
  ids off both `chat` spans.
- The hook record: the two `afterAgentResponse` payloads in `record/events/`, which hold the per-turn
  figures above. Written by the recorder, a separate process.
- Channel two, the transcript: `qa/tools/qa-transcript-cursor.py`, which reports 2 submitted prompts
  and 1 loop end. Two prompts is the independent turn count; the single loop end is normal and
  reading it as a turn count reported this exact run as one `chat` span too many, which is why
  [cursor-turn-ended-is-per-loop-not-per-turn](../../../learnings/cursor-turn-ended-is-per-loop-not-per-turn.md)
  exists.
- `plugin-debug.log` holds all four spans with their trace and parent ids, which is where the two
  traces are easiest to read.

## Then

- Dash0 holds exactly 4 spans: 2 `chat`, 2 `execute_tool`.
- The two `chat` spans have different `traceId`s, and neither has a parent.
- Each `execute_tool` span's `parentSpanId` is its own turn's `chat` span id, and its `traceId`
  matches. `qa-compare.py` prints `every span's parent is a span of this session`.
- Turn 2's `chat` span input tokens are close to turn 1's rather than roughly double, and equal turn
  2's payload exactly.
- The sum of the two `chat` spans' input tokens equals 64933, and neither span alone does.
- Both `execute_tool` spans carry distinct `gen_ai.tool.call.id` values.
- The transcript reports 2 submitted prompts.
- `qa-compare.py` exits `0`, and `qa-attrs.py` exits `0`.

## Tolerance

**Token counts vary between runs**, and turn 2's input is legitimately close to turn 1's rather than
equal: the second prompt adds to the context. The fixed claim is the inequality against the session
total, not any particular number.

**Cache-read grows sharply between turns** — 3456 to 25856 on the reference run — because turn 1's
context is cached by the time turn 2 runs. That is not a double-count; check `input_tokens`, which is
the figure a double-count inflates.

**The turn order in the recording is wall-clock, and the two turns interleave nothing.** If a
`beforeSubmitPrompt` appears before the previous turn's `afterAgentResponse`, the driver typed too
early; `qa-cursor-drive.py` waits for the completion row before it sends the next prompt, so this
should not happen, and a run where it did proves nothing about scoping.

**A turn that ran no tool is a failed run of this spec, not a passing one.** Re-run rather than
asserting over 2 spans.
