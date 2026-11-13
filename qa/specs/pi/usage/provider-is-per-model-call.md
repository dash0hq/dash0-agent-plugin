---
id: provider-is-per-model-call
area: pi/usage
runtime: pi
status: active
input: qa/tools/qa-session-pi.sh with QA_PI_MODEL=openai-codex/<model>
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
QA_PI_MODEL=openai-codex/gpt-5.6-luna qa/tools/qa-session-pi.sh \
  'Run the bash command: echo qa-pi-openai. Then reply with exactly the word done.' \
  spec-pi-openai
qa/tools/qa-pi-compare.py qa/runs/spec-pi-openai
```

## Expectation

Every assistant `message_end` in `stream.jsonl` names `provider` and
`model`. Here every call has provider `openai-codex`, which is OpenAI.

## Oracle

- Channel one: Dash0, `dash0 spans query` filtered by `gen_ai.conversation.id`
  in `piDataset`, through `qa-pi-compare.py`.
- Channel two: Pi's own `--mode json` stream, `stream.jsonl` in the run.

## Then

- `qa-pi-compare.py` prints `AGREEMENT`.
- Every `chat` span has `gen_ai.provider.name=openai` and
  `dash0.pi.provider=openai-codex`. The tool span has the provider of the call
  that requested it.
- A run on the default `claude-code` model gives `anthropic` and `claude-code`
  instead. Together the two runs show that the provider comes from each call,
  not from a session or harness default.

## Tolerance

Exact. The run needs a Pi login for the provider. Without one, the run
fails before any span exists, which is a failed run, not a finding.

## Verified

2026-11-13 on `qa/runs/setup-probe-pi-openai`: 2 chat spans, all `openai` and `openai-codex`, `billing_mode=subscription`, `AGREEMENT`.
