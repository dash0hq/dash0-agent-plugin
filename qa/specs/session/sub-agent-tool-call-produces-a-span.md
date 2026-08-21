---
id: sub-agent-tool-call-produces-a-span
area: session
status: draft
input: qa/tools/qa-session.sh, one prompt that delegates a tool call to a sub-agent
duration: ~25s
settling: 10s
cleanup: keep
covers:
  - internal/pipeline/pipeline.go
  - internal/otlp/tracecontext.go
known_failure: not filed
---

## Given

The same session as [sub-agent-usage-is-counted-once](sub-agent-usage-is-counted-once.md), read for a
different invariant. A tool call made inside a sub-agent fires its own `PostToolUse`, carrying
`agent_id` and `agent_type` alongside the main session's `session_id`. It should produce an
`execute_tool` span like any other tool call.

It does not always. This spec documents the failure and the condition that triggers it, so a fix can
be recognized as a fix.

## When

```sh
QA_MODEL=haiku QA_ALLOWED_TOOLS="Task Agent Bash" qa/tools/qa-session.sh \
  'Use the Task tool (subagent_type general-purpose) to ask a sub-agent to run the bash command: echo qa-sub-probe. When it returns, reply with exactly the word done.' \
  spec-subagent
sleep 10
```

A second prompt makes the sub-agent do more than one thing, which is the representative case:

```sh
QA_MODEL=haiku QA_ALLOWED_TOOLS="Task Agent Bash" qa/tools/qa-session.sh \
  'Use the Task tool (subagent_type general-purpose) to ask a sub-agent to run these three bash commands one after another, each as a separate Bash call: "sleep 1; echo one", then "sleep 1; echo two", then "sleep 1; echo three". When it returns, reply with exactly the word done.' \
  spec-subagent-multi
```

**The Task tool is asynchronous.** Its `PostToolUse` reports `duration_ms` of 2 or 3, measured on
four runs: the call returns immediately and the sub-agent runs on in the background. The spawning
turn's `Stop` then fires 2.2 to 3.1 seconds later, and the sub-agent's result arrives afterwards as a
`<task-notification>` injected as a fresh `UserPromptSubmit`, which opens a second turn.

So the window in which a sub-agent's tool call can still find a trace context is about two and a half
seconds wide, and it opens the moment the sub-agent is launched. Measured across four sessions:

| Sub-agent's work | Tool calls | Spans |
| --- | --- | --- |
| one `Bash`, done 0.3 s before the turn's `Stop` | 1 | 1 |
| one `Bash`, done 0.4 s before the turn's `Stop` | 1 | 1 |
| one `Bash`, done 0.7 s after the turn's `Stop` | 1 | 0 |
| three `Bash` calls over 10 s, all after the turn's `Stop` | 3 | 0 |

This is not a coin flip. A sub-agent that finishes inside two and a half seconds is the only one that
keeps its spans, and a sub-agent exists to do work that takes longer. The two passing runs each had a
single one-second tool call that happened to land just inside the window.

## Expectation

From `record/` alone. `record/events/*PostToolUse*.json` names each tool in `tool_name`, and a call
made inside a sub-agent additionally carries `agent_id` and `agent_type`. The mapping in
`internal/pipeline/pipeline.go` gives one `execute_tool` per `PostToolUse` regardless of who made the
call, so the expectation is one span per payload, and each sub-agent call's span is parented under
the `invoke_agent` span. On `spec-subagent-multi` that is 4 tool spans: `Agent` from the main session
and three `Bash` from inside the sub-agent.

`record/index.jsonl` also gives the ordering, in wall-clock order, which is what decides whether the
run reproduces the failure.

**The mechanism.** `Stop` calls `otlp.ClearTraceContext(sessionDir)` after exporting the turn's
`chat` span. `sendToolTrace` opens with `otlp.LoadTraceContext(dataDir)` and returns `no trace
context available for tool span` when it is gone, so the span is never built. Because the Task tool
returns in milliseconds, a sub-agent always outlives the turn that spawned it, and everything it does
after that `Stop` is invisible.

`sendLLMTrace` does not have this problem, because `SubagentStart` snapshots the trace context per
agent and `sendLLMTrace` falls back to `otlp.LoadAgentTraceContext(dataDir, agentID)` when the event
carries an `agent_id`. The comment on that snapshot names this exact hazard: the `SubagentStop`
"still finds the spawning turn's trace even when it arrives after Stop (context cleared)". The
snapshot exists, `sendToolTrace` just never reads it. That asymmetry is the defect, and it is why
`invoke_agent` survives the ordering and `execute_tool` does not.

## Oracle

- Channel one, Dash0: `qa/tools/qa-compare.py qa/runs/spec-subagent-multi`. Its "Tool spans" table is
  span count against `PostToolUse` count per tool name, and it exits `1`.
- Channel two, ordering: `record/index.jsonl`, to establish whether this run reproduced the failure
  at all. A run whose sub-agent tool calls all preceded the `Stop` passes and proves nothing.

## Then

While the defect stands, on `spec-subagent-multi`:

- `record/events/` holds three `PostToolUse` payloads with `tool_name: Bash` and `agent_id` set.
- Dash0 holds no `execute_tool Bash` span at all for the session, and 1 `execute_tool` in total.
- `qa-compare.py` exits `1` and reports `tool Bash: Dash0 has 0, PostToolUse fired 3`.
- Every other assertion in [sub-agent-usage-is-counted-once](sub-agent-usage-is-counted-once.md)
  still holds. The dropped spans carry no usage, so no token count moves. Both `chat` spans and the
  `invoke_agent` span are present, so the loss is invisible in any total.

Once fixed, the same run must give:

- 4 `execute_tool` spans: `Agent`, and three `Bash`.
- Each `Bash` span's parent is the `invoke_agent` span, reached through
  `otlp.SpanIDFromAgentID(agent_id)`, which is the same derivation the `invoke_agent` span's own
  parent uses.
- Each `Bash` span's duration equals its payload's `duration_ms`, so the three are distinguishable
  rather than one span counted three times.
- `qa-compare.py` exits `0`.

## Tolerance

**A passing run does not clear the defect.** A sub-agent that finishes inside the window keeps its
spans today, so a green run on a single fast tool call proves nothing. Use the `spec-subagent-multi`
prompt, and read `record/index.jsonl` first to confirm at least one sub-agent `PostToolUse` landed
after the spawning turn's `Stop`. A green run without that ordering is silent, not negative.

**`known_failure` has no ticket id yet.** The field reads `not filed`, which keeps the runner from
re-reporting it every pass but is not a real reference. The finding is written up in
[../../findings/subagent-tool-spans-are-dropped-after-stop.md](../../findings/subagent-tool-spans-are-dropped-after-stop.md);
replace this field with the ticket id once one exists, and record it there too.

**The count in the summary line is expected to disagree.** `qa-compare.py` will report
`execute_tool: Dash0 has 1, the hooks imply 4`, `total: Dash0 has 4, the hooks imply 7`, and
`tool Bash: Dash0 has 0, PostToolUse fired 3` on the same run. Those are one finding counted three
ways, not three findings.

**How many spans are lost depends on how long the sub-agent works, so no absolute count is
asserted.** The assertion is span count against that run's own `PostToolUse` count. A longer
sub-agent loses more, which is the wrong direction for a product: the more work is delegated, the
less of it is observable.

**Scope.** This is about tool calls inside a sub-agent. A tool call in the main session that somehow
landed after its own turn's `Stop` would hit the same code path, but nothing observed so far produces
that ordering, and it is not asserted here.
