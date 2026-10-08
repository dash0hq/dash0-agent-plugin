---
id: later-turns-are-traced-live
area: copilot-app/turns
runtime: copilot-app
status: draft
input: three prompts in one session, each running one bash command
duration: ~45s
settling: 25s
cleanup: keep
covers:
  - copilot-app/extension.mjs
  - internal/source/copilotapp/copilotapp.go
  - internal/source/copilot/emit.go
---

## Given

The runner procedure in [../README.md](../README.md), with a real model. Send each prompt only after the previous turn
has gone idle.

The first turn is rebuilt from history, because the extension starts after the first prompt has
been sent. Turns 2 and 3 arrive on the live stream. A defect in either path, or in their handover,
shows up from turn 2 on. That is why [first-turn-is-traced-with-its-subagent](../subagents/first-turn-is-traced-with-its-subagent.md)
cannot cover it.

## When

Send three prompts, each after the previous turn has finished:

```text
Run `echo turn-one` with the bash tool, then reply with the word one.
Run `echo turn-two` with the bash tool, then reply with the word two.
Run `echo turn-three` with the bash tool, then reply with the word three.
```

Wait out the settling time after the third.

## Expectation

**From `events.jsonl`, independently:**

- three main-agent `user.message` events with `delivery` `idle`;
- every main-agent `tool.execution_start` and the turn it falls in, splitting the log at those three
  messages. Each turn has its `echo` call, and the first turn may also have the app's own
  `rename_branch`.

Tokens have **no** independent record: `assistant.usage` is never written to the event log. So the
token figures below are read from the debug log and checked only for scoping, never for value.

## Oracle

- Dash0: `dash0-spans.json`, for counts, traces and parenting.
- The debug log, for `gen_ai.usage.input_tokens` on each `chat` span.
- The event log: `events.jsonl` in the run directory.

## Then

- Exactly three `chat` spans, each in its own trace.
- Every tool call in the event log is exactly one `execute_tool` span, parented on the `chat` span
  of the turn the event log puts it in. Match on `gen_ai.tool.call.id` = `toolCallId`.
- No `gen_ai.tool.call.id` appears on two spans.
- Every `chat` span carries `gen_ai.usage.input_tokens`.
- No `chat` span's input tokens equal the sum of two other turns' tokens, or come to more than
  twice the smallest turn's. That would be a turn carrying another turn's usage.

## Tolerance

**The token check is a heuristic.** Three near-identical turns should cost about the same, and a
growing context makes each slightly larger than the last. Growth of the order of the previous
turn's output is expected. Doubling is the defect. A bad result is reported as suspect and is not,
on its own, a failure.

**`rename_branch` and other app-added tools** are asserted only through the event log, never by
count.

**Ingest lag: 25 seconds, and up to five minutes in this runtime.** The debug log is complete at once. When Dash0 holds fewer spans than it, re-query for up to five minutes before calling one missing.
