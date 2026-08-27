---
id: billing-and-allowance-come-from-the-rollout
area: codex/session
runtime: codex
status: active
input: qa/tools/qa-session-codex.sh, any session that reaches the model
duration: ~12s
settling: 8s
cleanup: keep
covers:
  - internal/source/codex/rollout.go
  - internal/otlp/otlp.go
  - DEVELOPMENT.md
---

## Given

The plugin provisioned by `qa-session-codex.sh`, plus the recorder.

This is the one attribute family no other runtime has. Codex reports the account's plan and
allowance state in a `rate_limits` block alongside token usage, and the plugin puts it on the `chat`
span as `dash0.gen_ai.billing_mode`, `plan_type`, `rate_limit.*` and `credits.*`. Those labels are
what a cost consumer uses to tell subscription spend from per-token spend, so a wrong one misprices
a whole account rather than one span.

The account's plan is whatever the credential used for the run has. The spec asserts that the span
agrees with the rollout, never that the plan is any particular value.

## When

```sh
qa/tools/qa-session-codex.sh \
  'Run the shell command: echo qa-probe. Then reply with exactly the word done.' \
  spec-codex-billing
sleep 8
```

Shape, measured on plugin 0.1.25 with codex-cli 0.149.1, on a `free` plan: `plan_type: free`,
primary window 43200 minutes, no secondary window, no credits. The reference run's last block read
29% used and its span carried 29; an earlier run the same hour read 26.

## Expectation

From `rollout.jsonl` alone. Codex writes it; the plugin only reads it.

**Take the LAST `rate_limits` block in the file, not the first.** Every `token_count` payload carries
one, and they differ: a two-turn run's first block said `used_percent: 25.0` and its last said
`26.0`, because the allowance moved while the session ran. Usage is per-turn and resets, but the allowance
describes the account and only ever carries the most recent value seen. Comparing the span against
the first block reports a difference that is really the account being used.

From that block, the expected span attributes are:

| span attribute | rollout field |
| --- | --- |
| `dash0.gen_ai.plan_type` | `plan_type` |
| `dash0.gen_ai.rate_limit.primary.used_percent` | `primary.used_percent` |
| `dash0.gen_ai.rate_limit.primary.window_minutes` | `primary.window_minutes` |
| `dash0.gen_ai.rate_limit.primary.resets_at` | `primary.resets_at` |
| `dash0.gen_ai.credits.available` | `credits.has_credits` |
| `dash0.gen_ai.credits.unlimited` | `credits.unlimited` |

**`billing_mode` is derived, and the rule is the one thing here that is deductive.** A present
`plan_type` means `subscription`; an absent one means `unknown`. There is deliberately no `api`
value — see `DEVELOPMENT.md`. That rule lives in the product, so this clause asserts the code does
what the doc says rather than what the record says, and it is worth having only because the doc is
hand-maintained and would have to be changed to make it pass wrongly.

**Which slot holds which window is not fixed.** Codex models `primary` and `secondary` as the same
type, so read `window_minutes` to tell a five-hour window from a monthly one. 43200 minutes is 30
days. Never assume an ordering.

**Absence is distinct from zero.** A null `secondary` means the plan reports one window, and the
span must carry no `secondary.*` attribute at all, rather than a zero. `credits.balance` is likewise
null on this account, so no balance attribute is expected. A rollout from before ~14 Jul 2026 has no
`credits` block at all.

## Oracle

- Channel one, Dash0: `dash0 spans query` filtered to `gen_ai.conversation.id`, reading the
  `dash0.gen_ai.*` attributes off the `chat` spans.
- Channel two, the rollout: the last `rate_limits` block in `qa/runs/spec-codex-billing/rollout.jsonl`.
  `qa-rollout.py` does not print it; read it with the snippet in `## Expectation`.

## Then

- Every `chat` span carries `dash0.gen_ai.billing_mode`, and its value is `subscription` when the
  rollout names a `plan_type` and `unknown` when it does not.
- `dash0.gen_ai.plan_type` equals the last block's `plan_type`.
- The three `rate_limit.primary.*` attributes equal the last block's `primary` fields.
- `dash0.gen_ai.credits.available` and `.unlimited` equal `has_credits` and `unlimited`.
- No `rate_limit.secondary.*` attribute is present, because `secondary` is null.
- No credits balance attribute is present, because `balance` is null.
- On a session with two turns, both `chat` spans carry the same allowance values, because the
  allowance describes the account and does not reset with the turn.
- `execute_tool` spans carry none of these keys. They are properties of the account's model usage,
  not of a shell command.

## Tolerance

**`used_percent` moves during the run.** It is a live figure and the session itself consumes
allowance. Assert the span against the last block in the same rollout, and expect that number to
differ from an earlier one in the same file. A difference between the span and a *later* reading
taken outside the run is not a finding.

**A different account changes every value.** `free`, one window, no credits is this credential. An
account with a five-hour window populates `secondary` and the spec then has more to assert, not
less. Nothing about the plan is asserted, only the agreement.

**`resets_at` is a unix second, not a duration.** Compare it as an integer. It does not change
within a session.

**A rollout with no `rate_limits` at all** means either a CLI older than the field or a session that
never reached the model. Both make this spec unrunnable rather than failing: re-run against a
session that produced `token_count` events.

**Ingest lag.** A few seconds, as everywhere in this suite.
