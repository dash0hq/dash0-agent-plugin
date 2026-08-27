---
id: subagent-usage-is-counted-once
area: codex/subagents
runtime: codex
status: draft
input: qa/tools/qa-session-codex.sh with QA_CODEX_MULTI_AGENT=1, any delegating session
duration: ~25s
settling: 8s
cleanup: keep
covers:
  - internal/source/codex/rollout.go
  - internal/source/codex/codex.go
  - internal/otlp/otlp.go
---

## Given

The plugin provisioned by `qa-session-codex.sh`, plus the recorder, with
`QA_CODEX_MULTI_AGENT=1`.

A delegating Codex session writes one rollout per thread: the main session's, and one per
sub-agent. Each thread's tokens are recorded only in its own file — Codex never rolls a
sub-agent's usage up into the parent. The plugin follows that split, reading the sub-agent's
rollout through `agent_transcript_path` on `SubagentStop` and putting those counts on the
`invoke_agent` span, while the turn's `chat` span carries the main thread's.

So the invariant is a partition: every token appears on exactly one span, and the two
sides are separately checkable against two files neither of which the plugin wrote.

## When

```sh
QA_CODEX_MULTI_AGENT=1 qa/tools/qa-session-codex.sh \
  'Spawn a sub-agent named worker that runs the shell command "echo alpha". Wait for it to finish. Then send that SAME agent a follow-up task to run the shell command "echo beta", and wait for that too. Then reply with exactly the word delegated.' \
  spec-codex-subagent-usage
sleep 8
```

The driver keeps every rollout: `rollout.jsonl` for the session, selected by session id,
and `rollout-subagent-<thread id>.jsonl` for each agent.

> [!IMPORTANT]
> Selecting the session's rollout by *newest* picks the sub-agent's, because its file is
> created later. That bug made the usage channel read one sub-agent's turn as the whole
> session. If a run's `rollout.jsonl` has `thread_source: subagent` in its `session_meta`,
> the driver regressed and no number below means anything.

## Expectation

From the rollouts alone.

**Each thread's file total is that thread's usage.** Sum `info.last_token_usage` over every
`token_count` event in a file. The *file* total, not the last turn's: a `chat` span exists
per turn but an `invoke_agent` span exists per completed task, and the sum over spans for a
thread is the sum over that thread's whole file.

| what | expected from | reference run |
| --- | --- | --- |
| the `chat` span(s) | `rollout.jsonl` file total | 73538 in, 147 out, 60160 cache_read |
| the `invoke_agent` span(s) for agent A | `rollout-subagent-A.jsonl` file total | 62332 in, 120 out, 55296 cache_read |

On that run the agent completed two tasks, so its file total is split across two
`invoke_agent` spans — 31032 + 31300 input — and only the sum is asserted. Splitting it
per task means partitioning the sub-agent's `token_count` events by its `SubagentStop`
timestamps in `record/index.jsonl`, the same technique
[../session/turn-usage-is-scoped-to-its-own-turn](../session/turn-usage-is-scoped-to-its-own-turn.md) uses, and
it is worth doing only once a defect suggests it.

**Counted once means the parent does not repeat it.** The `chat` span's input must equal
the main rollout's total exactly — not the sum of both files. On the reference run the
difference is large and unmissable: 73538 against 135870.

**A single-agent run gives the cleanest form of the same check**, one `invoke_agent` against
one file: 31129 in, 121 out, 22016 cache_read, matching to the token.

## Oracle

- Channel one, Dash0: `dash0 spans query` filtered to `gen_ai.conversation.id`, reading
  `gen_ai.usage.*` off the `chat` and `invoke_agent` spans, grouped by `gen_ai.agent.id`.
- Channel two, the rollouts: `qa/tools/qa-rollout.py` per file gives each thread's totals.
  `qa-compare.py`'s Tokens table already sums the session's rollouts and compares them
  against Dash0, so it catches a total that is short or double — but not a total that is
  right while attributed to the wrong span, which is what the per-span assertions below are
  for.

## Then

- Every `invoke_agent` span carries `gen_ai.usage.input_tokens`, `output_tokens` and
  `cache_read.input_tokens`.
- For each agent, the sum of its `invoke_agent` spans' usage equals its own rollout's file
  total, to the token.
- The `chat` span's usage equals the main rollout's file total, to the token, and is
  strictly less than the session-wide sum.
- No token is counted twice: the sum over all `chat` and `invoke_agent` spans equals the
  sum over all rollout files.
- The number of `invoke_agent` spans equals the number of `SubagentStop` hooks.
- `qa-compare.py` exits `0`, with its Tokens table showing `dash0` and `transcript` equal.

## Tolerance

**A sub-agent that reaches no model reports nothing.** If its rollout has no `token_count`
event, its `invoke_agent` span carries no usage, and that is an absence rather than a zero.
Re-run with a sub-agent that does real work rather than asserting zeros.

**A compressed sub-agent rollout makes this unrunnable.** Neither the plugin nor
`qa-rollout.py` reads `.zst`; the plugin marks the span
`dash0.codex.rollout.compressed` instead. Codex 0.149.1 writes plain `.jsonl` and no
compressed rollout has been observed, so treat one as "cannot measure", not as a failure.

**Token counts vary between runs.** The table is the measured shape. Only the equality
between spans and files is asserted.

**Cache-creation is always zero here.** Codex reports no cache-creation count, so the
plugin sends none and both sides agree at zero for the absence of a concept rather than a
measurement. Do not read it as a finding either way.

**Ingest lag.** A few seconds, as everywhere in this suite.
