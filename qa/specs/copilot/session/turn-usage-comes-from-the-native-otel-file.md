---
id: turn-usage-comes-from-the-native-otel-file
area: copilot/session
runtime: copilot
status: active
input: qa/tools/qa-session-copilot.sh, one turn with one tool call; then the same prompt with QA_COPILOT_NO_OTEL=1
duration: ~40s for both runs
settling: 25s
cleanup: keep
covers:
  - internal/source/copilot/otelfile.go
  - cmd/copilot-on-event/main.go
  - internal/pipeline/pipeline.go
---

## Given

The plugin installed into a throwaway home by `qa-session-copilot.sh` through the real marketplace
path, exporting to the target in `qa/config.local.json`, plus the QA recorder. Nothing on the
machine is configured or mutated; see `## Runtimes` in [../../../setup.md](../../../setup.md).

**Copilot's hooks carry no tokens.** Not in the `agentStop` payload, not anywhere else. The turn's
usage is recovered at `agentStop` from Copilot's native-OpenTelemetry file, which the driver enables
at launch exactly as the `dash0-configure` launch function does in production. So this spec asserts
two different things with two different strengths:

- **that a `chat` span exists per turn** — from the hook record, which is independent;
- **that its numbers equal the file's** — from the file, which is also the plugin's input, so this
  is a faithful-copy claim and nothing more.

**One turn, one tool call.** The tool call matters: it forces a second model round-trip, so the file
holds two `chat` spans whose usage must be *summed* into the turn's one span. Without it a summing
error is invisible, because one round-trip's usage equals the turn's.

**The negative case needs a second run.** `QA_COPILOT_NO_OTEL=1` launches Copilot with native OTel
off, which is what a user who never ran `/dash0-configure` gets. The plugin's documented degradation
is a `chat` span with no usage at all, and asserting that is what stops "no numbers" from being read
as a broken export.

## When

```sh
QA_COPILOT_BINARY=working-tree qa/tools/qa-session-copilot.sh \
  'Run the shell command: echo qa-probe. Then reply with exactly the word done.' \
  spec-copilot-turn-usage
QA_COPILOT_BINARY=working-tree QA_COPILOT_NO_OTEL=1 qa/tools/qa-session-copilot.sh \
  'Reply with exactly the word done.' \
  spec-copilot-turn-usage-no-otel
sleep 25
```

Shape, measured on plugin 0.1.25 with Copilot CLI 1.0.80. First run: 5 hook invocations
(`sessionStart`, `userPromptSubmitted`, `postToolUse`, `agentStop`, `sessionEnd`), one native-OTel
file holding 1 `invoke_agent`, 2 `chat` and 1 `execute_tool` span, 2 spans in Dash0, and
`"spans_logged": 2` in the manifest. Second run: 4 hook invocations, no OTel file, 1 span in Dash0,
`"native_otel": false` in the manifest.

## Expectation

**One `chat` span per `agentStop`, from `record/index.jsonl` alone.** That row is written by the QA
recorder, a separate process that reads no OTel file, so this half of the expectation owes the
plugin nothing.

**The turn's usage is the sum of the file's `chat` spans**, computed by `qa/tools/qa-otel.py`, which
re-reads the format rather than importing `internal/source/copilot`. Measured on the reference run:

| | input | output | cache_read | reasoning |
| --- | --- | --- | --- | --- |
| round-trip 1 | 14652 | 72 | 6656 | 31 |
| round-trip 2 | 14761 | 5 | 13824 | 0 |
| turn | 29413 | 77 | 20480 | 31 |

The per-round-trip rows are there to be *contradicted*: a `chat` span carrying either of them alone
is a summing failure, and it is the failure a single-round-trip probe cannot see.

**A second, independent-ish figure exists on this runtime and nowhere else.** Copilot writes its own
per-turn roll-up on the turn's top-level `invoke_agent` span — the parentless one, which the plugin
represents with the turn's `chat` span rather than a span of its own. On a turn with no sub-agent it
must equal the sum above, measured 29413/77 against 29413/77, and the plugin never reads it, so it
is Copilot checking its own arithmetic rather than the plugin's. On a delegating turn the two
diverge by design; see [../subagents](../subagents/README.md).

**With no OTel file, the expectation is a `chat` span carrying no usage keys at all.** Not zeros:
`attachUsage` in `cmd/copilot-on-event/main.go` is never reached, so nothing is set.

## Oracle

- Channel one, Dash0: `qa/tools/qa-compare.py qa/runs/spec-copilot-turn-usage`, then
  `dash0 spans query` filtered to `gen_ai.conversation.id`, reading `gen_ai.usage.*` off the `chat`
  span.
- Channel two, the OTel file: `qa/tools/qa-otel.py qa/runs/spec-copilot-turn-usage`, which prints
  the `agent` and `chat` columns side by side.
- The hook record: `record/index.jsonl`, counting `agentStop` rows.
- `plugin-debug.log` splits one failure in two. A span there but not in Dash0 was built and lost in
  transport or ingest; a span in neither was never built.

## Then

- Dash0 holds exactly 1 `chat` span for the session, matching the single `agentStop` row.
- That span's `gen_ai.usage.input_tokens`, `output_tokens`, `cache_read.input_tokens` and
  `reasoning.output_tokens` equal the file's summed `chat` totals.
- Its input tokens do **not** equal either single round-trip's. This is the assertion the spec
  exists for: `29413`, not `14652` and not `14761`.
- `gen_ai.request.model` is the model the file's `chat` spans name.
- **No span carries `github.copilot.cost`.** The file's `chat` spans do carry it, in AI credits,
  and the plugin deliberately leaves it there: exporting it would put two attributes ending in
  `cost` on one span, in two units, next to the `dash0.gen_ai.usage.cost` Dash0 derives from
  tokens at ingest in money. `qa-otel.py` still prints the credits figure, because that is the
  channel reporting what Copilot measured rather than what the plugin shipped.
- Copilot's own `invoke_agent` roll-up equals the summed `chat` totals.
- In the no-OTel run, Dash0 holds exactly 1 `chat` span and it carries **no** `gen_ai.usage.*` key,
  and no `execute_tool` span exists for the session.
- `qa-compare.py` exits `0` on both runs.

## Tolerance

**Token counts vary between runs.** The table is the measured shape, not a constant. Only the
agreement between the file and the span is asserted, and only the inequality against a single
round-trip is a fixed claim.

**Which model Copilot picks is not fixed.** The reference runs used `gpt-5.3-codex`, whichever the
account defaults to. A different model changes every number and no assertion here.

**How many round-trips a turn makes is not fixed.** Two is what one tool call produced; a model that
retries or narrates more makes more. The assertion is the sum, not the count.

**Copilot reports no cache-write count anywhere**, so `gen_ai.usage.cache_creation.input_tokens` is
absent rather than zero, and `qa-compare.py` omits the row on this runtime. Do not assert it.

**Ingest lag is longer here than the other runtimes' 8 seconds.** Measured 2026-08-28: at 8 seconds
Dash0 held one turn's spans and not the next turn's, which reads exactly like a product bug. Allow
25 seconds, and re-run the comparison rather than believing a short count.

**The no-OTel run is the only legitimate way to get a span with no usage.** If a run with OTel *on*
produces one, the file was not written — check `qa/runs/<id>/otel.jsonl` exists before concluding
anything about the plugin.
