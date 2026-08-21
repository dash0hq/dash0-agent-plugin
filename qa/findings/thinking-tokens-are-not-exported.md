---
id: thinking-tokens-are-not-exported
severity: low
status: unaddressed
filed: no
found: 2026-08-21
affects: claude-on-event 0.1.24
spec: none
---

# Thinking tokens are counted but not attributable

A `chat` span reports `gen_ai.usage.output_tokens` with thinking tokens included and nothing that
separates them. On the smallest session measured, 104 of 116 output tokens were thinking. No consumer
can tell a verbose answer from a long deliberation.

The number is available. Claude Code's transcript reports
`usage.output_tokens_details.thinking_tokens`, and `claude-result.json` carries the same figure.
`internal/transcript/transcript.go` does not read the field: the only mention of thinking in the
package is a comment about streaming splitting one call across entries.

Cost is unaffected. Thinking tokens are billed at the output rate, and they are already inside
`output_tokens`, so `dash0.gen_ai.usage.cost` is correct.

## Reproduction

```sh
QA_MODEL=haiku qa/tools/qa-session.sh \
  'In one sentence, and without using any tools, what is the capital of France?' finding-thinking
python3 -c "
import json
d = json.load(open('qa/runs/finding-thinking/claude-result.json'))
print(d['usage']['output_tokens'], d['usage']['output_tokens_details'])
"
```

Compare against `gen_ai.usage.output_tokens` on the session's `chat` span: the totals agree and the
breakdown is absent.

## Why it matters

Thinking is the largest share of output on short prompts and the easiest thing to tune, through the
effort level. Without the split, the one number a user would act on is the one they cannot see.

## Suggested fix

Read `output_tokens_details.thinking_tokens` in `internal/transcript` and emit it as a
`dash0.gen_ai.usage.*` attribute, alongside the existing cache-lifetime breakdown, which is the same
shape of problem already solved.
