---
id: single-turn-agrees-with-its-session-export
area: opencode-v2/session
runtime: opencode-v2
status: active
input: qa/tools/qa-session-opencode-v2.sh, one turn that reads one file
duration: ~30s
settling: 25s
cleanup: keep
covers:
  - opencode-v2/index.js
  - internal/source/opencodev2/opencodev2.go
  - internal/otlp/otlp.go
---

## Given

`qa/tools/qa-session-opencode-v2.sh` with its defaults: a private `opencode serve`, this checkout's
plugin with the working-tree exporter, `omit_io` at its default of `true`, and the free model
`opencode/muse-spark-1.3-contributor-free`. The scratch project holds one `README.md`.

## When

```sh
qa/tools/qa-session-opencode-v2.sh \
  'Read README.md with the read tool. Then reply with exactly the word done.' \
  spec-opencode-single-turn
sleep 25
```

## Expectation

Derived from `internal/source/opencodev2/opencodev2.go`, not from a run:

- `session.execution.started` opens a turn and `session.execution.succeeded` closes it, emitting
  one span named `chat <model id>`.
- Each `session.tool.input.started` … `session.tool.success` pair emits one span named
  `execute_tool <tool name>`, parented on the turn's `chat` span, in the same trace.
- The `chat` span's input tokens are `input + cache.read + cache.write` summed over the turn's
  `session.step.ended` events, and its output tokens `output + reasoning`. The export's assistant
  messages carry the same per-step counts, so the sums must agree to the token.
- `gen_ai.conversation.id` is the OpenCode session id, `gen_ai.harness.name` is `opencode-v2`,
  `gen_ai.agent.name` is `opencode`, `gen_ai.provider.name` is the model's `providerID`.

## Oracle

- Channel one, Dash0: `qa/tools/qa-compare.py qa/runs/spec-opencode-single-turn`.
- Channel two, OpenCode's own record: `session-export.json`, read by the same tool.
- `plugin-debug.log` for a span missing from Dash0.

## Then

- `qa-compare.py` exits `0`: the `chat`, `execute_tool` and per-turn token rows agree across
  Dash0, the export and the debug log.
- Dash0 holds exactly 1 `chat` span and at least 1 `execute_tool read` span.
- Every span shares one trace id, and each `execute_tool` span's parent is the `chat` span.
- `gen_ai.conversation.id`, `gen_ai.harness.name`, `gen_ai.agent.name` and `gen_ai.provider.name`
  carry the values above. `dash0.team.name` is `dash0-qa`.

## Tolerance

**The model may read more than once.** The count must match the export's tool parts, not the
number 1.

**`gen_ai.provider.name` for the free model is `opencode`.** That is OpenCode's provider id for its
own gateway, not a missing value.
