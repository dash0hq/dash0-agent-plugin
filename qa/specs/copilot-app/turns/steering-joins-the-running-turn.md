---
id: steering-joins-the-running-turn
area: copilot-app/turns
runtime: copilot-app
status: draft
input: a turn running `sleep 20`, and a steering message sent while it runs
duration: ~30s
settling: 25s
cleanup: keep
covers:
  - copilot-app/extension.mjs
  - internal/source/copilotapp/copilotapp.go
---

## Given

The runner procedure in [../README.md](../README.md), with a real model and **without** `--omit-io`, because
the assertion is about the input text. The second message goes in with `delivery_mode: "immediate"`,
measured 2026-10-06 to arrive as `delivery: "steering"`.

## When

Send:

```text
Run `sleep 20` with the bash tool, then reply with the word done.
```

While `sleep` runs, send this with `delivery_mode: "immediate"`:

```text
Also say which shell you used.
```

Wait for the session to go idle, then wait out the settling time.

## Expectation

**From `events.jsonl`, independently:**

- one main-agent `user.message` with `delivery` `idle` (call its content *P1*);
- one main-agent `user.message` with `delivery` `steering` (call its content *P2*);
- no other main-agent `user.message`.

So the session had **one** turn, and its input was *P1* followed by *P2*.

## Oracle

- The debug log, for the `chat` span's `gen_ai.input.messages`. Dash0 masks content on read.
- Dash0: `dash0-spans.json`, for the span count and parenting.
- The event log: `events.jsonl` in the run directory.

## Then

- Exactly one `chat` span for the session.
- In the debug log, its input's text is *P1*, a newline, and *P2*.
- `execute_tool bash` is parented on that `chat` span.
- Every tool call in the event log is one `execute_tool` span, all in the one trace.

## Tolerance

**The run is inconclusive if the second message was not steering.** If the event log shows it with
`delivery: "queued"`, the app treated it as a new run. That is
[queued-message-is-its-own-turn](queued-message-is-its-own-turn.md), not a failure here. Re-run and
send the message sooner.

**The app may wrap a prompt.** The first prompt of a worktree session can carry an app-added block,
such as `<copilot_tauri_workspace>…`, in `data.content`. Compare with the event log's content,
never with the text that was sent.

**Ingest lag: 25 seconds, and up to five minutes in this runtime.** The debug log is complete at once. When Dash0 holds fewer spans than it, re-query for up to five minutes before calling one missing.
