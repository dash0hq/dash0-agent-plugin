---
id: tool-call-produces-one-span-per-post-tool-use
area: session
status: draft
input: qa/tools/qa-session.sh, one prompt that forces two different tools
duration: ~15s
settling: 10s
cleanup: keep
covers:
  - internal/pipeline/pipeline.go
  - internal/otlp/tracecontext.go
  - claude/hooks.json
---

## Given

The plugin as installed, plus the recorder. `qa-session.sh` permits `Bash` and `Read` by default,
which is exactly the pair this spec needs.

Two tools rather than one, and two tools with very different durations. A single tool call cannot
distinguish "one span per call" from "one span per turn", and two calls that both took the same time
cannot distinguish a span's timing from a constant.

## When

```sh
QA_MODEL=haiku qa/tools/qa-session.sh \
  'Run the bash command: echo qa-tool-probe. Then read the file settings.json in the .claude directory. Then reply with exactly the word done.' \
  spec-tool-call
sleep 10
```

Shape, measured on release 0.1.24 with `claude` 2.1.238: 9 hook invocations, 2 `PostToolUse`, 1
`Stop`, and 3 spans. The two tools ran in one turn, so there is one `chat` span, not two.

## Expectation

From `qa/runs/spec-tool-call/record/` alone.

**One span per `PostToolUse`.** `record/index.jsonl` counts the invocations and
`record/events/*PostToolUse*.json` names each tool in `tool_name`. The mapping in
`internal/pipeline/pipeline.go` turns each into one `execute_tool`, so two payloads naming `Bash` and
`Read` expect exactly one span each. The count comes from the hooks, never from the spans.

**The tool call id.** Each payload carries `tool_use_id`. `internal/otlp/otlp.go` maps it to
`gen_ai.tool.call.id`, so the set of ids on the spans must equal the set in the payloads. This is
what makes the count an identity check rather than a tally: two spans with the same id would also
count as two.

**The span's duration.** Each payload carries `duration_ms`, and `sendToolTrace` subtracts it from
the hook's timestamp to get the start time. So a span's end minus its start equals the payload's
`duration_ms` exactly. Measured: `Bash` 900 ms and `Read` 8 ms, in both the payload and the span.
Two values two orders of magnitude apart, so a hardcoded or a constant duration cannot pass.

**The parent.** `sendToolTrace` reads the trace context the turn's `chat` span wrote, so every tool
span is a child of the `chat` span for the turn that called it.

**The bash command family.** `EnrichToolEvent` derives `dash0.gen_ai.tool.bash.command_family` from
`tool_input`, and the payload holds the command. `echo qa-tool-probe` expects `echo`. The rules are
in `internal/pipeline/pipeline.go`, so this expectation is deductive rather than independent, and it
is asserted only because the input is in the record.

## Oracle

- Channel one, Dash0: `qa/tools/qa-compare.py qa/runs/spec-tool-call`. Its "Tool spans" table is
  per-tool-name, span count against `PostToolUse` count.
- Channel two, per-span detail: `dash0 spans query` filtered to the session, reading
  `gen_ai.tool.call.id`, the timestamps, and `parentSpanId` off each span. `qa-compare.py` compares
  counts only.

## Then

- `qa-compare.py` exits `0` and prints `All three records agree.`
- Dash0 holds 3 spans: 1 `chat`, 2 `execute_tool`, 0 `invoke_agent`.
- The `execute_tool` span names are `execute_tool Bash` and `execute_tool Read`, one each, matching
  `tool_name` in the two payloads.
- The set of `gen_ai.tool.call.id` across the tool spans equals the set of `tool_use_id` across the
  payloads, with no duplicate.
- Each tool span's duration equals its payload's `duration_ms`. The two differ by more than two
  orders of magnitude, so assert both.
- Both tool spans have the `chat` span's span id as their parent, and the `chat` span is a root span.
- `gen_ai.operation.name` is `execute_tool`, `gen_ai.tool.type` is `function`, and
  `gen_ai.tool.name` matches the payload's `tool_name` on both.
- `dash0.gen_ai.tool.bash.command_family` on the `Bash` span is `echo`. The `Read` span has no such
  attribute.
- `gen_ai.tool.call.arguments` and `gen_ai.tool.call.result` are present on both spans.
- Every span's status code is unset. Both tools succeeded.

## Tolerance

**Content of the arguments and the result is not assertable through this oracle.** The API returns
`gen_ai.tool.call.arguments` and `gen_ai.tool.call.result` as `<REDACTED>`, as it does for
`gen_ai.input.messages`. Presence is checkable, value is not. A spec that needs the value belongs in
`test/e2e/`, which sees the payload before it leaves the machine.

**The model attribute is expected on every tool span, but is not asserted here.** An earlier run of
this prompt put `gen_ai.request.model` on the `Bash` span and not on the `Read` span, because
`sendToolTrace` re-read the transcript per call and the first call lost the race with its flush. The
model is now resolved once per turn and cached for that turn, and the re-run carried it on both
spans. It deserves its own spec rather than a clause here; see the coverage map.

**Which tools the model picks is the model's choice.** The prompt names both tools explicitly, but a
run that produces a different tool set, an extra `ToolSearch`, or two turns instead of one is not a
product finding. Discard it and re-run. Every assertion above compares the spans against that run's
own payloads, so a re-run with a different shape still either agrees or does not.

**Timing.** Span start is derived by subtracting `duration_ms`, an integer number of milliseconds,
from the hook's timestamp. Compare durations to the millisecond, not finer.

**Ingest lag.** As in [single-turn-no-tool-session](single-turn-no-tool-session.md): a few seconds,
and `qa-compare.py` widens the window at both ends. Zero spans right after a session is lag until a
re-run says otherwise.
