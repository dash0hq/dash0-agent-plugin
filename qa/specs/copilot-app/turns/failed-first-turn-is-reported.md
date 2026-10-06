---
id: failed-first-turn-is-reported
area: copilot-app/turns
runtime: copilot-app
status: draft
input: fake model in error mode (400, message carrying a fresh marker); one prompt
duration: ~10s
settling: 25s
cleanup: keep
covers:
  - copilot-app/extension.mjs
  - internal/source/copilotapp/copilotapp.go
  - cmd/copilot-app-on-event/main.go
---

## Given

The runner procedure in [../README.md](../README.md), with the fake model and `omit_io` off. Pick a
fresh marker *K* = `QA-ERR-<uuid>`, and start the fake model with:

```sh
qa/tools/qa-fake-model.py serve --port 8765 --log qa/runs/<run-id>/fake-model.jsonl \
  --mode error --status 400 --message "K for the request"
```

Prepare the target with `--fake-model` and without `--omit-io`.

**This is the regression spec for a fixed defect.** The app starts the extension only after the
first prompt is sent. A request that fails at once ends the turn in about 30 ms, before the
extension listens. Measured 2026-10-06 on session `5a4817ee`: the turn closed 190 ms before
`dash0: connected`, and Dash0 got no span at all for it. The fix recovers a new session's only turn
even when it has already closed.

## When

```text
send_session_message <target>: hi
```

Wait for the idle notification, then the settling time.

## Expectation

**From `fake-model.jsonl`, independently:** exactly one call, answered `400` with *K*. If there is
no call, the pin did not take. Stop and report a setup failure.

**From `events.jsonl`, independently:**
- one main-agent `user.message`;
- after it, one `session.error`. Its `data.message` (call it *M*) is the expected status message.
  Measured 2026-10-06: the app prefixes the status, so *M* is `400 K for the request`, with
  `errorType` `query`.

## Oracle

- The debug log, for each span's status.
- Dash0: `dash0-spans.json`, for the span count and the conversation id.
- The event log and `fake-model.jsonl` in the run directory.

## Then

- Exactly one `chat` span for the session, in Dash0 and in the debug log.
- Its status code is `2`, and its status message equals *M* byte for byte.
- Its `gen_ai.input.messages`, in the debug log, holds the prompt `hi`.

## Tolerance

**No model round means no model in the span name.** The turn has no `assistant.usage`, so the span
is named `chat ` (trailing space) with no `gen_ai.request.model`. That is the current shape, noted
under `## Observe` in setup.md.

**The prefix belongs to the app.** Compare with *M* from the event log, never with a literal.

**Ingest lag: 25 seconds.**
