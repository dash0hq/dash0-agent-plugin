---
id: failed-later-turn-is-reported
area: copilot-app/turns
runtime: copilot-app
status: draft
input: fake model in ok mode for turn 1, restarted in error mode for turn 2
duration: ~30s
settling: 25s
cleanup: keep
covers:
  - copilot-app/extension.mjs
  - internal/source/copilotapp/copilotapp.go
  - cmd/copilot-app-on-event/main.go
---

## Given

The runner procedure in [../README.md](../README.md), with the fake model and `omit_io` off. Start
the fake model in `ok` mode. Prepare the target with `--fake-model`.

A later turn's error arrives on the live stream, through a different path from
[failed-first-turn-is-reported](failed-first-turn-is-reported.md), which is rebuilt from history.
And the healthy turn before it must stay healthy: the error must not leak into the wrong turn's
span.

## When

1. `send_session_message <target>: hi`, and wait for idle.
2. Stop the fake model and start it again on the same port and log, with a fresh marker *K*:
   `--mode error --status 400 --message "K for the request"`.
3. `send_session_message <target>: hi again`, and wait for idle. Then wait out the settling time.

## Expectation

**From `fake-model.jsonl`, independently:** one call answered `200`, then one answered `400` with
*K*. Anything else is a setup failure.

**From `events.jsonl`, independently:** two main-agent `user.message` events, and one
`session.error`, after the second. Its `data.message` is *M*.

## Oracle

- The debug log, for each span's status.
- Dash0: `dash0-spans.json`, for counts and traces.
- The event log and `fake-model.jsonl` in the run directory.

## Then

- Exactly two `chat` spans, in two traces.
- The first has no error status.
- The second has status code `2` and status message *M*.

## Tolerance

**Ingest lag: 25 seconds.**
