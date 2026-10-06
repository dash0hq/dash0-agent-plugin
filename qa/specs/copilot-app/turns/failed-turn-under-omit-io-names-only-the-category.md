---
id: failed-turn-under-omit-io-names-only-the-category
area: copilot-app/turns
runtime: copilot-app
status: draft
input: fake model in error mode (400, message carrying a fresh marker); one prompt; omit_io on
duration: ~10s
settling: 25s
cleanup: keep
covers:
  - internal/source/copilotapp/copilotapp.go
  - cmd/copilot-app-on-event/main.go
---

## Given

As in [failed-first-turn-is-reported](failed-first-turn-is-reported.md), with a fresh marker *K*,
but pass `--omit-io` to both `swap-in` and `prepare`.

An error message can quote the request, so under `omit_io` the span status must carry only the
error's category. The fake model's message stands in for that quote: *K* is known before the run,
so its absence can be proved.

## When

```text
kickoff: hi
```

Wait for the idle notification, then the settling time.

## Expectation

**From `fake-model.jsonl`, independently:** exactly one call, answered `400` with *K*. No call means
a setup failure.

**From `events.jsonl`, independently:** the main-agent `session.error`'s `data.errorType` (call it
*T*), which is the expected status message. Measured 2026-10-06 as `query`.

**Known before the run:** *K*, which must appear nowhere the plugin sent.

## Oracle

- The debug log: every attribute and status the plugin sent. Dash0 masks message content on read,
  so the debug log is where an absence is proved.
- Dash0: `dash0-spans.json`, and the raw query output, as a second reading.
- The event log and `fake-model.jsonl` in the run directory.

## Then

- Exactly one `chat` span, with status code `2` and status message *T*.
- *K* occurs nowhere in `plugin-debug.log`, nor in Dash0's answer for the session.
- `gen_ai.input.messages` is absent or `<REDACTED>` in the debug log.

## Tolerance

**A *T* of `error`** is the plugin's default for a `session.error` with no `errorType`. It passes
only if the event log's error has none.

**Measured once already.** On 2026-10-06, session `9cfe90f7` ran this exact input under a user
config with `omit_io` on. Its one `chat` span had status 2, message `query`, and no *K*. That run
was not prepared by the harness, so it does not count as a pass of this spec.

**Ingest lag: 25 seconds, and up to five minutes in this runtime.** The debug log is complete at once. When Dash0 holds fewer spans than it, re-query for up to five minutes before calling one missing.
