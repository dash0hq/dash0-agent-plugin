---
id: every-model-call-carries-its-own-tokens
area: pi/usage
runtime: pi
status: active
input: any qa-session-pi.sh run; the conversation run covers the most calls
duration: ~15s
settling: none; qa-pi-compare.py polls
cleanup: keep
covers:
  - pi/index.ts
  - internal/source/pi/pi.go
---

## Given

The Pi extension and helper **from this working tree**, loaded by
`qa/tools/qa-session-pi.sh` with `--no-extensions -e pi/index.ts` and a helper
built into the run directory. No installed copy is read. The plugin exports to
`piDataset` in `qa/config.local.json` through `PI_PLUGIN_OPTION_*`. See
`### Pi` under `## Runtimes` in [setup.md](../../../setup.md).

## When

```sh
qa/tools/qa-session-pi.sh \
  'Run the bash command: echo qa-pi-usage. Then reply with exactly the word done.' \
  spec-pi-usage
qa/tools/qa-pi-compare.py qa/runs/spec-pi-usage
```

## Expectation

For each assistant `message_end` in `stream.jsonl`, take Pi's normalized
usage. The expected input tokens are `usage.input + usage.cacheRead +
usage.cacheWrite`, and the expected output tokens are `usage.output`. Sum over
all calls. Pi reports `input` without cache for every provider. The GenAI
conventions, and Dash0's cost processor for every harness except Claude Code,
expect input with cache.

## Oracle

- Channel one: Dash0, `dash0 spans query` filtered by `gen_ai.conversation.id`
  in `piDataset`, through `qa-pi-compare.py`.
- Channel two: Pi's own `--mode json` stream, `stream.jsonl` in the run.

## Then

- `input tokens`: the sum of `gen_ai.usage.input_tokens` over all `chat`
  spans equals the expected input.
- `output tokens`: the sum of `gen_ai.usage.output_tokens` equals the sum of
  `usage.output`.
- `models`: the multiset of models agrees. The comparer reads
  `dash0.gen_ai.request.model.original`, because Dash0 normalizes
  `gen_ai.request.model` at ingest: `claude-haiku-4-5-20251001` is stored as
  `claude-haiku-4-5`.

## Tolerance

Exact. Each call's tokens are on exactly one span, so a sum over the trace
is the turn. A call that reported no usage at all, for example one aborted
before the response, has no token attributes on either side.

## Verified

2026-11-13: exact on all six probe runs, for example 17123 in and 207 out on `qa/runs/setup-probe-pi-conversation`, 1810 and 32 on `qa/runs/setup-probe-pi-openai`.
