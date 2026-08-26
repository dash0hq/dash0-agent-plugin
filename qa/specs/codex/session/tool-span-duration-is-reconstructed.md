---
id: tool-span-duration-is-reconstructed
area: codex/session
runtime: codex
status: active
input: qa/tools/qa-session-codex.sh, one prompt per tool call to be timed
duration: ~12s
settling: 8s
cleanup: keep
covers:
  - internal/source/codex/codex.go
  - internal/pipeline/pipeline.go
  - internal/filelog
---

## Given

The plugin provisioned by `qa-session-codex.sh`, plus the recorder.

**Codex does not time its tool calls.** A Claude `PostToolUse` payload carries `duration_ms` and the
plugin subtracts it from the hook's timestamp to get a start time. A Codex payload carries no such
field — verified on every recorded Codex payload to date. So `internal/source/codex` reconstructs it:
on `PostToolUse` it looks up the matching `PreToolUse` by `tool_use_id` in the session's own
`events.jsonl`, diffs the timestamps, and writes `duration_ms` onto the event before the pipeline
builds the span.

That reconstruction is Codex-only machinery with no Claude equivalent, and nothing outside a live run
exercises the file round-trip it depends on. If it breaks, every Codex tool span gets a wrong or
zero duration and every other assertion in this suite still passes.

## When

```sh
qa/tools/qa-session-codex.sh \
  'Run the shell command: echo turn-one. Then reply with exactly the word one.' \
  spec-codex-tool-duration
sleep 8
```

Shape, measured on plugin 0.1.25 with codex-cli 0.149.1: 5 hook invocations, one `PreToolUse` and
one `PostToolUse` for `Bash`, one `execute_tool Bash` span of 112ms.

A prompt that runs a slow command as well as a fast one is better input where the model will comply:
two durations an order of magnitude apart prove the number is measured rather than constant. The
model's shell tends to collapse a `sleep` into one call, so the reference run asserts the weaker
form and the tolerance says so.

## Expectation

From `record/index.jsonl` alone, which the plugin never reads.

**The recorder saw the same two hooks.** Every `PreToolUse` and `PostToolUse` row carries
`recorded_at` and its payload carries `tool_use_id`. The gap between the pair with the same
`tool_use_id` is an independent measurement of how long the tool took, taken by a different process
from the one that built the span.

Measured on the reference run, and on the two-turn run of
[turn-usage-is-scoped-to-its-own-turn](turn-usage-is-scoped-to-its-own-turn.md) for a second and
third pair:

| run | `tool_use_id` | recorder gap | span duration | skew |
| --- | --- | --- | --- | --- |
| `spec-codex-tool-duration` | `exec-0a4118e0-…` | 87ms | 86ms | −1ms |
| `spec-codex-turn-usage` | `exec-eeb8fbef-…` | 104ms | 112ms | +8ms |
| `spec-codex-turn-usage` | `exec-27bbdc89-…` | 96ms | 106ms | +10ms |

**The identity half matters more than the millisecond half.** `gen_ai.tool.call.id` on the span must
equal the `tool_use_id` in the payload, because a duration attached to the wrong call is worse than
no duration: the reconstruction keys on exactly that id, and a mismatch means it paired the wrong
two hooks.

**Absence is a failure, not a tolerance.** A span whose duration is 0ms, or equal to the whole turn,
means the lookup missed and the pipeline fell back — which is the failure this spec exists to catch.

## Oracle

- Channel one, Dash0: `dash0 spans query` filtered to `gen_ai.conversation.id`, reading
  `startTimeUnixNano`, `endTimeUnixNano` and `gen_ai.tool.call.id` off each `execute_tool` span.
  `qa-compare.py`'s tool table compares counts only, so it cannot see this.
- Channel two, the hook record: `record/index.jsonl` for the `recorded_at` pair and
  `record/events/*PostToolUse*.json` for the `tool_use_id`.

## Then

- Every `execute_tool` span carries a `gen_ai.tool.call.id`, and the set of ids equals the set of
  `tool_use_id` values across the recorded `PostToolUse` payloads, with no duplicate.
- Every `execute_tool` span's duration is greater than zero.
- Each span's duration is within tolerance of its own pair's recorder gap.
- No span's duration equals its `chat` span's duration, which is what a failed lookup would produce.
- Each `execute_tool` span is a child of the turn's `chat` span.
- The recorded `PostToolUse` payloads carry **no** `duration_ms`. If one ever does, Codex started
  reporting it, the reconstruction is dead code, and this spec should be rewritten rather than fixed.

## Tolerance

**Within 20ms of the recorder gap, and the cause is two processes.** The recorder and the plugin are
separate hook handlers for the same two events, each reading the clock when it runs. The span is
built from the plugin's own two reads, the expectation from the recorder's, and the four reads
interleave. Measured skew across three pairs was −1ms, +8ms and +10ms, so it runs in both directions
and the span is not reliably the longer of the two. A difference of hundreds of milliseconds is not
skew.

**Do not assert absolute durations.** `echo` takes as long as the machine takes. Only the agreement
between the two measurements is asserted.

**Which tools the model picks is the model's choice.** A run that uses `apply_patch` instead of
`Bash`, or makes three calls instead of one, still either agrees with its own record or does not. A
run with no tool call at all is discarded and re-run, not reported.

**One order of magnitude is the stronger form of this spec.** The reference run's two calls were
both around 100ms, so they do not by themselves rule out a constant. The id and the non-zero
assertions carry the weight until a run produces a genuinely slow call; a run that does produce one
should assert both durations.
