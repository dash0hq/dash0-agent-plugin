---
id: conversation-keeps-one-session-across-turns
area: pi/session
runtime: pi
status: active
input: qa/tools/qa-session-pi.sh with QA_PI_TURNS, four prompts on one session
duration: ~60s
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
cat > /tmp/qa-pi-turns.txt << 'EOF'
Run the bash command: echo qa-turn-two. Then reply with exactly the word done.
Run the bash command: qa-this-command-does-not-exist. Do not try an alternative and do not fix it. Then reply with exactly the word done.
Without running any command, reply with exactly the codeword I gave you in my first message, and nothing else.
EOF
QA_PI_TURNS=/tmp/qa-pi-turns.txt qa/tools/qa-session-pi.sh \
  'Remember the codeword: ZEPHYR-41. Run the bash command: echo qa-turn-one. Then reply with exactly the word done.' \
  spec-pi-conversation
qa/tools/qa-pi-compare.py qa/runs/spec-pi-conversation
```

## Expectation

From `stream.jsonl`: 4 `agent_settled` records, so 4 turns. 3
`tool_execution_end` records for `bash`, one of them with `isError: true`, for
the command that does not exist. The last assistant text is `ZEPHYR-41`, which
proves that the four prompts were one session.

## Oracle

- Channel one: Dash0, `dash0 spans query` filtered by `gen_ai.conversation.id`
  in `piDataset`, through `qa-pi-compare.py`.
- Channel two: Pi's own `--mode json` stream, `stream.jsonl` in the run.

## Then

- 4 parentless `chat` spans, all with the same `gen_ai.conversation.id`.
- One `chat` span per assistant `message_end`.
- `tool errors`: `{bash: 1}`. The failing call's span carries
  `exception.message` and status `ERROR`. The others do not.
- Token totals agree with the stream. See
  [every-model-call-carries-its-own-tokens](../usage/every-model-call-carries-its-own-tokens.md).

## Tolerance

Exact. Check the codeword first. If the model did not answer `ZEPHYR-41`,
the prompts did not share a session, and every count above would still agree.

## Verified

2026-11-13 on `qa/runs/setup-probe-pi-conversation`: 4 roots, 7 chat spans, 3 bash spans with 1 error, codeword recalled, `AGREEMENT`.
