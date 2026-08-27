---
id: single-turn-no-tool-session
area: claude/session
runtime: claude
status: draft
input: qa/tools/qa-session.sh, one prompt that needs no tool
duration: ~10s
settling: 10s
cleanup: keep
covers:
  - internal/pipeline/pipeline.go
  - internal/transcript/transcript.go
  - internal/otlp/otlp.go
  - claude/hooks.json
---

## Given

The plugin as the machine actually has it installed. Nothing about it is configured by this spec:
its endpoint, dataset, and token come from the managed install, and `qa/config.local.json` supplies
only the read side. `qa/tools/qa-session.sh` adds one thing to the session, a second hook handler
that records what the plugin was fed.

The smallest session that produces telemetry at all. One user turn, one model reply, no tool call,
no sub-agent. That makes every count in this spec a small integer, so a wrong one is unambiguous
rather than a rounding argument.

## When

```sh
QA_MODEL=haiku qa/tools/qa-session.sh \
  'In one sentence, and without using any tools, what is the capital of France?' spec-single-turn
sleep 10
```

The prompt forbids tools in words rather than by flag. `qa-session.sh` still permits `Bash` and
`Read`, so a session that reaches for one is a real observation about the model, and the
`execute_tool` assertion below is what catches it. A run where the model does call a tool is not a
product finding: discard it and re-run.

Shape, measured on release 0.1.24 with `claude` 2.1.238: 5 hook invocations, 1 model request, 2
assistant transcript entries (one of them a thinking block), and 1 span.

## Expectation

Every expected value comes from `qa/runs/spec-single-turn/record/`, which the plugin does not write,
and from Claude Code's own transcript.

**Span counts,** from `record/index.jsonl` and the mapping in `internal/pipeline/pipeline.go`.
`Stop` implies one `chat`, `PostToolUse` one `execute_tool`, `SubagentStop` one `invoke_agent`. A
no-tool single turn records exactly `SessionStart`, `InstructionsLoaded`, `UserPromptSubmit`,
`Stop`, `SessionEnd`, so the expectation is 1 `chat` and nothing else.

**Token counts,** from the `usage` block of the assistant entries in the final transcript.
`claude/tools/claude-code-usage-audit.py <session-id> --json` sums them per model. One model request
means one `usage` block, so the span's four token attributes must equal it exactly rather than
approximately.

**Cost.** No span carries a cost; Dash0 derives `dash0.gen_ai.usage.cost` at ingest. The expectation
is the transcript's token counts times Anthropic's published list prices, computed by
`qa/tools/qa-cost.py`, which holds the price table. For `claude-haiku-4-5` that is $1.00 input,
$5.00 output, and $0.10 cache read per million tokens, with a cache write at 1.25x the input rate
for a 5-minute lifetime and 2x for an hour. Claude Code prices the same request itself and reports
`total_cost_usd` in `claude-result.json`, which is a second independent figure for the same value.

**Session visibility.** `gen_ai.conversation.id` carries the session id from the manifest, so one
query on that id alone returns the session and nothing else. The `chat` span is a root span, which
is what makes the session the top of a trace rather than an orphan.

## Oracle

- Channel one, Dash0: `qa/tools/qa-compare.py qa/runs/spec-single-turn`, which queries
  `gen_ai.conversation.id is <session-id>` with `--precision disabled` and lines the spans up against
  the recording, the transcript, and `claude-result.json`. Exit `0` means all four agree.
- Channel two, cost: `qa/tools/qa-cost.py qa/runs/spec-single-turn`. Exit `0` means Dash0's cost
  matches the price table over the transcript's tokens.
- Neither channel may be replaced by reading the plugin's own output. The recording under
  `record/` is the pipeline's input, not a third observation.

## Then

- `qa-compare.py` exits `0` and prints `All three records agree.`
- Dash0 holds exactly 1 span for the session: `chat`, with `execute_tool` and `invoke_agent` both 0,
  and all three columns agree.
- The `chat` span is a root span, and its status code is unset. A single successful turn produces no
  error, so a set status is a finding.
- `gen_ai.usage.input_tokens`, `gen_ai.usage.output_tokens`, `gen_ai.usage.cache_read.input_tokens`,
  and `gen_ai.usage.cache_creation.input_tokens` each equal the transcript's figure exactly.
- `dash0.gen_ai.usage.cache_creation.ephemeral_5m.input_tokens` plus
  `dash0.gen_ai.usage.cache_creation.ephemeral_1h.input_tokens` equals
  `gen_ai.usage.cache_creation.input_tokens`.
- `qa-cost.py` exits `0`: `dash0.gen_ai.usage.cost` equals the price table over the transcript's
  tokens, and `claude-result.json`'s `total_cost_usd` equals the same value.
- `service.name` is the installed plugin's `AGENT_NAME`, and `service.version` is the plugin version
  in the manifest. A `service.name` of `claude-code` means the run picked up the default instead of
  the managed install, so it tested something other than what this machine runs.
- `gen_ai.harness.name` is `claude-code`, `gen_ai.provider.name` is `anthropic`, and
  `gen_ai.operation.name` is `chat`.
- `process.working_directory` is the run's own `project/` directory, which proves the span came from
  this session rather than from a developer's live session in the same dataset.

## Tolerance

**Cost, to the microdollar.** Not a tolerance so much as an observation: across every `chat` span in
a 24-hour window, 82 of them over two models, the price table above reproduced
`dash0.gen_ai.usage.cost` exactly, including a session whose cache writes were split across both
lifetimes. Compare with a slack of `1e-6` dollars for float
accumulation and treat anything larger as a finding.

**The cache-write lifetime is the one place that could move.** Only the Claude transcript path
carries the 5-minute and 1-hour breakdown, and a release that stopped sending it would leave the
cost to be computed at a single rate. `qa-cost.py` therefore also prints a bracket: the same tokens
priced entirely at the 5-minute rate and entirely at the 1-hour rate. A cost inside that bracket but
outside the exact figure means the breakdown stopped arriving, which is a finding about the
attribute rather than about the arithmetic.

**A model name that differs in length is not a delta.** The plugin sends the dated snapshot id, so
the span name reads `chat claude-haiku-4-5-20251001`. Dash0 stores the canonical
`claude-haiku-4-5` in `gen_ai.request.model` and keeps the dated id in
`dash0.gen_ai.request.model.original`. Compare model *sets* after dropping a trailing `-YYYYMMDD`.

**Token counts vary between runs; their agreement does not.** `cache_read` and cache-write totals
depend on how warm the prompt cache was, so no absolute number is asserted here. Every assertion
compares the span against the transcript for the same run.

**The thinking breakdown is present but not asserted here.** The reply contains a thinking block, and
`gen_ai.usage.reasoning.output_tokens` now reports its share of `gen_ai.usage.output_tokens` — 191 of
238 on the verifying run, matching `output_tokens_details.thinking_tokens` in `claude-result.json`
exactly. It is a subset, so it moves no total and changes no cost. The key is emitted only when the
turn did some thinking, matching what the Copilot source does with the same key, so a spec asserting
it must drive a prompt that actually thinks — absence is the correct output for a turn that did not.
Asserting it needs its own spec; see the coverage map.

**No conversation name.** A headless `claude -p` session gets no title, so the span has no
`gen_ai.conversation.name`. An interactive session does. Do not assert a name here.

**Ingest lag.** Spans are queryable a few seconds after the session ends, and `qa-compare.py` widens
the window by 60 seconds before the start and 120 after the end. A comparison that reports zero
spans immediately after a session is lag until a re-run says otherwise.
