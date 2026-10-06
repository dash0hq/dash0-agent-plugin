---
id: queued-message-is-its-own-turn
area: copilot-app/turns
runtime: copilot-app
status: draft
input: a turn running `sleep 20`, and a second message queued while it runs
duration: ~35s
settling: 25s
cleanup: keep
covers:
  - copilot-app/extension.mjs
  - internal/source/copilotapp/copilotapp.go
---

## Given

The runner procedure in [../README.md](../README.md), with a real model. The second message goes in with
`delivery_mode: "enqueue"`, measured 2026-10-06 to arrive as `delivery: "queued"`.

**What is already measured.** Two queued messages in this machine's app history, from 2026-10-05:
each `user.message` with `delivery: "queued"` was written when its own run started, immediately
before that run's `assistant.turn_start` and after the previous run's last `assistant.turn_end`. So
a queued message should never fall inside the turn before it. This spec confirms that against a
live run of the plugin.

## When

Send:

```text
Run `sleep 20` with the bash tool, then reply with the word first.
```

While `sleep` runs, send a second message with `delivery_mode: "enqueue"`:

```text
Run `echo queued-turn` with the bash tool, then reply with the word second.
```

Wait for both runs to finish and the session to go idle, then wait out the settling time.

## Expectation

**From `events.jsonl`, independently:**

- one main-agent `user.message` with `delivery` `idle`, and one with `delivery` `queued`;
- two turns, split at the queued `user.message`: the `sleep` call before it, and the `echo` call
  after it.

## Oracle

- Dash0: `dash0-spans.json`, for span count, traces and parenting.
- The debug log, for each `chat` span's input.
- The event log: `events.jsonl` in the run directory.

## Then

- Exactly two `chat` spans, in two traces.
- `execute_tool bash` running `sleep` is parented on the first, and the one running `echo` on the
  second. Tell them apart by `gen_ai.tool.call.id`, matched with `toolCallId` in the event log.
- In the debug log, the second `chat` span's input is the queued message's content alone, not both
  messages joined.
- No span appears twice.

## Tolerance

**The run is inconclusive if the second message was not queued.** If the event log shows
`delivery: "steering"`, that is
[steering-joins-the-running-turn](steering-joins-the-running-turn.md). Re-run.

**Ingest lag: 25 seconds.**
