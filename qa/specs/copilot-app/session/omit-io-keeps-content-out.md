---
id: omit-io-keeps-content-out
area: copilot-app/session
runtime: copilot-app
status: draft
input: one turn with omit_io on, whose prompt, tool arguments and tool output each carry a unique marker
duration: ~15s
settling: 25s
cleanup: keep
covers:
  - copilot-app/extension.mjs
  - internal/source/copilotapp/copilotapp.go
  - internal/source/copilot/emit.go
  - internal/otlp/otlp.go
---

## Given

The runner procedure in [../README.md](../README.md), with a real model, prepared with `--omit-io`. Pick three fresh
markers for the run, for example `QA-PROMPT-<uuid>`, `QA-ARG-<uuid>` and `QA-RESULT-<uuid>`, so no
earlier run can supply them.

## When

Send, with the markers substituted:

```text
QA-PROMPT-<uuid>. Run `echo QA-ARG-<uuid> | sed s/ARG/RESULT/` with the bash tool, then repeat its
output back to me.
```

The command's output is `QA-RESULT-<uuid>`, which appears in no argument, so it can only reach a span
through the tool's result or the model's reply. Wait for the session to go idle, then wait out the
settling time.

## Expectation

**From the run's own inputs and `events.jsonl`, independently:**

- the three markers, known before the run;
- the `bash` call's `tool.execution_complete` result, which should contain `QA-RESULT-<uuid>`;
- the main-agent `assistant.message`, which should repeat it.

Each of those strings must be absent from everything the plugin sent.

## Oracle

- The debug log: every span the plugin sent, before the wire. Dash0 masks message content on read,
  so a Dash0 read cannot prove an absence there. For the tool keys it can, so it is read too.
- Dash0: `dash0-spans.json`, for the tool keys and as a second reading of the span count.
- The event log: `events.jsonl` in the run directory.

## Then

- None of the three markers occurs anywhere in `plugin-debug.log`.
- None of them occurs anywhere in `dash0-spans.json`.
- `gen_ai.input.messages` and `gen_ai.output.messages` on the `chat` span, and
  `gen_ai.tool.call.arguments` and `gen_ai.tool.call.result` on every `execute_tool` span, are
  either absent or exactly `<REDACTED>` in the debug log.
- The spans exist: one `chat` span and one `execute_tool bash` span. A run that sent nothing would
  pass every absence check above.

## Tolerance

**The model may not repeat the output, or may rewrite the command.** Then `QA-RESULT-<uuid>` is only
in the tool result. The check still stands, because the result must not leak either. If the event
log shows the `bash` command without `QA-ARG-<uuid>`, the run is inconclusive for that marker. Say
so.

**`process.working_directory` and the VCS keys are not content.** They follow `omit_user_info`, not
`omit_io`, and are not asserted here.

**Ingest lag: 25 seconds, and up to five minutes in this runtime.** The debug log is complete at once. When Dash0 holds fewer spans than it, re-query for up to five minutes before calling one missing.
