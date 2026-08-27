---
id: sub-agent-usage-is-counted-once
area: claude/session
runtime: claude
status: draft
input: qa/tools/qa-session.sh, one prompt that delegates to a sub-agent
duration: ~25s
settling: 10s
cleanup: keep
covers:
  - internal/pipeline/pipeline.go
  - internal/otlp/tracecontext.go
  - internal/transcript/transcript.go
---

## Given

The plugin as installed, plus the recorder, with `QA_ALLOWED_TOOLS="Task Agent Bash"` so the model
can delegate.

A sub-agent's usage lives in a transcript of its own, under
`<project>/<session-id>/subagents/agent-<id>.jsonl`. The main transcript does not contain it. So
there are two places the same tokens could be reported from, and double-counting a sub-agent is
invisible unless the two are checked apart.

## When

```sh
QA_MODEL=haiku QA_ALLOWED_TOOLS="Task Agent Bash" qa/tools/qa-session.sh \
  'Use the Task tool (subagent_type general-purpose) to ask a sub-agent to run the bash command: echo qa-sub-probe. When it returns, reply with exactly the word done.' \
  spec-subagent
sleep 10
```

Shape, measured on release 0.1.24 with `claude` 2.1.238: 15 hook invocations, 1 `SubagentStart`, 1
`SubagentStop`, 2 `Stop`, and 2 turns. The delegation itself costs a turn, so there are two `chat`
spans, not one.

## Expectation

Two records, kept apart on purpose. `claude/tools/claude-code-usage-audit.py <session-id> --json`
reports `main` and `subagents` separately, and `qa/tools/qa-cost.py` prices their sum.

**One `invoke_agent` span, from `SubagentStop`.** `record/index.jsonl` holds exactly one, so the
expectation is exactly one span. `agent_id` in that payload is the sub-agent's identity.

**The `invoke_agent` span carries the sub-agent's usage, and only that.** Its four token attributes
must equal the `subagents` block of the audit exactly. Measured: input 18, output 245, cache write
18,293, cache read 13,655.

**The `chat` spans carry the main session's usage, and only that.** Their four token attributes must
sum to the audit's `main` block, with the sub-agent's figures absent. Measured: 40 input over two
spans of 10 and 30, and 20,568 cache-write tokens over two spans of 579 and 19,989. This is the
assertion that catches double-counting: if the sub-agent's tokens were folded into the parent turn,
this sum would be too high by exactly the `invoke_agent` span's figures.

**The parent.** `SubagentStart` snapshots the spawning turn's trace context per agent, and
`otlp.SpanIDFromAgentID(agent_id)` derives the `execute_tool Agent` span's id from the same
`agent_id`. So the `invoke_agent` span's parent is computable from the payload: it is the
`execute_tool Agent` span, which is itself a child of the `chat` span for the turn that delegated.
Three levels, all derivable from the record.

**Cost is additive.** `dash0.gen_ai.usage.cost` on each span, summed, equals the price table over
main plus sub-agent. Measured: $0.0050909 plus $0.02547475 plus $0.0533733 against a total of
$0.083939. The sub-agent wrote 18,293 tokens at the 5-minute cache lifetime while the main session
wrote 20,568 at the 1-hour lifetime, so the sum only comes out right if the two lifetimes are priced
apart.

## Oracle

- Channel one, Dash0: `dash0 spans query` filtered to `gen_ai.conversation.id`, reading the token
  attributes and `parentSpanId` per span. `qa-compare.py` sums tokens across all spans, so it cannot
  see a split that is wrong in both directions; the per-span read is what this spec needs.
- Channel two, cost: `qa/tools/qa-cost.py qa/runs/spec-subagent`.
- The `subagents` block of `claude-code-usage-audit.py` is the independent record for the sub-agent
  half, and `record/index.jsonl` for the span count.

## Then

- Dash0 holds exactly 1 `invoke_agent` span, matching the single `SubagentStop` in the record.
- Its name is `invoke_agent general-purpose`, `gen_ai.agent.name` is the `subagent_type` from the
  `Task` call, and `gen_ai.agent.id` equals `agent_id` in the `SubagentStop` payload.
- Its four token attributes equal the audit's `subagents` usage exactly.
- The `chat` spans' token attributes sum to the audit's `main` usage exactly, with no contribution
  from the sub-agent.
- Adding the two gives the audit's `total`, so no token is counted twice and none is dropped.
- The `invoke_agent` span's parent is the `execute_tool Agent` span, whose span id equals
  `otlp.SpanIDFromAgentID(agent_id)`.
- The `execute_tool Agent` span's parent is a `chat` span, and that `chat` span is a root span.
- `dash0.gen_ai.usage.cost` summed over all spans equals the price table over the audit's `total`,
  and `qa-cost.py` exits `0`.
- The recorder captured `agent_transcript_sha256` for the `SubagentStop` invocation. Without it the
  sub-agent half of this spec has no independent record and every assertion above becomes
  single-channel.

## Tolerance

**`qa-compare.py` will report a difference on this run, and it is a different bug.** The sub-agent's
own `Bash` call fires a `PostToolUse` that produces no span, so the tool-span count disagrees with
the hooks. That is
[sub-agent-tool-call-produces-a-span](sub-agent-tool-call-produces-a-span.md), a known failure with a
known cause. It does not affect any assertion here: the missing span carries no usage, and the token
totals still reconcile.

**`claude-result.json` is not a channel for tokens on this spec.** Its `usage` block covers the main
session only, so it reports a fraction of the real figure and the gap grows with the sub-agent's
work. Its `total_cost_usd` is whole and can be compared. See
`qa/learnings/usage-claude-result-json-omits-subagent-usage.md`.

**Turn count is the model's choice.** Delegating costs a turn, so two `chat` spans is the normal
shape, but a run that produces three, or that answers without delegating at all, is not a finding.
No `SubagentStop` means the model did not delegate: reword the prompt and re-run rather than
concluding anything. Every assertion compares the spans against that run's own records.

**Token counts vary between runs.** The measured figures above are there to show the shape and to
prove the assertion can fail, not as constants to compare against. Only the agreement between the
channels is asserted.

**Ingest lag.** A few seconds, and this session is longer than the others, so `qa-compare.py`'s
window widening matters more here. Zero spans right after the session is lag until a re-run says
otherwise.
