---
id: turn-produces-one-chat-span-per-model-call-and-one-span-per-tool
area: pi/session
runtime: pi
status: active
input: qa/tools/qa-session-pi.sh, one turn with one tool call
duration: ~15s
settling: none; qa-pi-compare.py polls
cleanup: keep
covers:
  - pi/index.ts
  - internal/source/pi/pi.go
  - internal/otlp/trace.go
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
  'Run the bash command: echo qa-pi-first. Then reply with exactly the word done.' \
  spec-pi-session
qa/tools/qa-pi-compare.py qa/runs/spec-pi-session
```

## Expectation

From Pi's `--mode json` stream in `stream.jsonl`, which the plugin never reads:

- turns: the number of `agent_settled` records. 1 here.
- model calls: the number of `message_end` records whose message has role
  `assistant`. 2 here: the call that requested the tool, and the answer.
- tools: one per `tool_execution_end`, by `toolName`. `{bash: 1}` here.

## Oracle

- Channel one: Dash0, `dash0 spans query` filtered by `gen_ai.conversation.id`
  in `piDataset`, through `qa-pi-compare.py`.
- Channel two: Pi's own `--mode json` stream, `stream.jsonl` in the run.

## Then

- `turns -> chat roots`: Dash0 holds one parentless `chat` span per turn.
- `model calls -> chat spans`: one `chat` span per model call. The root is the
  last call of the turn. Each earlier call is a child of the root. A `chat`
  span with `dash0.pi.usage.source=tool` carries a tool's own model usage and
  is not a model call, so this row does not count it.
- `aborted turns -> turn.status`: each `agent_settled` with `aborted: true` is
  a root with `dash0.pi.turn.status=aborted`. 0 here.
- `tools`: one `execute_tool` span per tool call, with the same name.
- `harness`: every span carries `gen_ai.harness.name=pi`.

## Tolerance

Exact. Dash0 can take minutes to make a batch queryable. `qa-pi-compare.py`
polls for up to six minutes, so a slow read is a wait, not a finding. A run in
which the model did not call the tool is a failed run, not a failing spec.

## Verified

2026-11-13 on `qa/runs/setup-probe-pi`: 1 root, 2 chat spans, `bash: 1`, `AGREEMENT`.
