---
id: sub-agent-tool-call-produces-a-span
area: session
status: draft
input: qa/tools/qa-session.sh, one prompt that delegates several tool calls to a sub-agent
duration: ~25s
settling: 10s
cleanup: keep
covers:
  - internal/pipeline/pipeline.go
  - internal/otlp/tracecontext.go
---

## Given

The same session as [sub-agent-usage-is-counted-once](sub-agent-usage-is-counted-once.md), read for a
different invariant. A tool call made inside a sub-agent fires its own `PostToolUse`, carrying
`agent_id` and `agent_type` alongside the main session's `session_id`. It must produce an
`execute_tool` span like any other tool call, parented under the `Agent` tool span that launched the
sub-agent.

It did not, for every call that landed after the spawning turn's `Stop`, which is most of them. That
is fixed; this spec keeps the ordering that broke it in the run, because a run that does not
reproduce the ordering proves nothing.

## When

```sh
QA_SWAP_BINARY=1 QA_MODEL=haiku QA_ALLOWED_TOOLS="Task Agent Bash" qa/tools/qa-session.sh \
  'Use the Task tool (subagent_type general-purpose) to ask a sub-agent to run these three bash commands one after another, each as a separate Bash call: "sleep 1; echo one", then "sleep 1; echo two", then "sleep 1; echo three". When it returns, reply with exactly the word done.' \
  fix-subagent-tools
sleep 10
```

**The Task tool is asynchronous.** Its `PostToolUse` reports `duration_ms` of 2 or 3: the call
returns immediately and the sub-agent runs on in the background. The spawning turn's `Stop` fires 2.2
to 3.1 seconds later, and the sub-agent's result arrives afterwards as a `<task-notification>`
injected as a fresh `UserPromptSubmit`, which opens a second turn. So a sub-agent doing real work
always outlives the turn that spawned it.

The three one-second `Bash` calls put every sub-agent tool call on the far side of that `Stop`. From
`record/index.jsonl` of the verifying run:

```
+12.23s SubagentStart
+14.31s Stop                 session trace context cleared here
+16.29s PostToolUse   Bash [in sub-agent]
+19.26s PostToolUse   Bash [in sub-agent]
+21.64s PostToolUse   Bash [in sub-agent]
+23.51s SubagentStop
```

A single fast tool call is not a substitute. It can finish before the `Stop` and pass without
exercising anything.

## Expectation

From `record/` alone. `record/events/*PostToolUse*.json` names each tool in `tool_name`, and a call
made inside a sub-agent additionally carries `agent_id` and `agent_type`. The mapping in
`internal/pipeline/pipeline.go` gives one `execute_tool` per `PostToolUse` regardless of who made the
call, so the expectation is one span per payload. On this prompt that is 4 tool spans at minimum:
`Agent` from the main session and three `Bash` from inside the sub-agent. The model may also call
`ToolSearch` or `TaskCreate` first, which is why the assertion is per-tool-name against that run's
own payload count, never an absolute total.

`record/index.jsonl` also gives the ordering, in wall-clock order, which is what decides whether the
run exercised the fix.

**The mechanism.** `Stop` calls `otlp.ClearTraceContext(sessionDir)` after exporting the turn's
`chat` span, so by the time a sub-agent's `PostToolUse` arrives the session context is gone.
`SubagentStart` snapshots the trace context per agent for exactly this reason, and `sendLLMTrace`
reads that snapshot through `otlp.LoadAgentTraceContext(dataDir, agentID)` when the event carries an
`agent_id`. `sendToolTrace` now does the same, which is why `execute_tool` survives the ordering that
`invoke_agent` always survived.

Parenting comes from `otlp.SpanIDFromAgentID(agent_id)`, the same derivation the `Agent` tool span
uses for its own span id, so the two meet without shared state.

## Oracle

- Channel one, Dash0: `qa/tools/qa-compare.py qa/runs/fix-subagent-tools`. Its "Tool spans" table is
  span count against `PostToolUse` count per tool name.
- Channel two, ordering: `record/index.jsonl`, to establish that the run put sub-agent tool calls
  after the turn's `Stop`. A run without that ordering is silent, not passing.
- Channel three, parenting: the span tree read back from Dash0, since `qa-compare.py` counts spans
  and does not check who their parent is.

## Then

Measured on the verifying run:

- `record/events/` holds three `PostToolUse` payloads with `tool_name: Bash` and `agent_id` set, all
  after the turn's `Stop`.
- Dash0 holds three `execute_tool Bash` spans, and `qa-compare.py` reports `9 in Dash0` against `9`
  from the hooks and `9` from the transcript, exiting `0`.
- Each `Bash` span's parent is the `execute_tool Agent` span, making them siblings of the
  `invoke_agent` span rather than children of it.
- Each `Bash` span's duration matches its own payload: 2043 ms, 1030 ms, 1024 ms. Three distinct
  spans, not one counted three times.
- Every other assertion in [sub-agent-usage-is-counted-once](sub-agent-usage-is-counted-once.md)
  still holds, and no token count moved: the previously dropped spans carry no usage.

## Tolerance

**A passing run means nothing without the ordering.** Read `record/index.jsonl` first and confirm at
least one sub-agent `PostToolUse` landed after the spawning turn's `Stop`. A sub-agent that finished
inside the window would have passed before the fix too.

**The tool inventory varies.** The model may reach for `ToolSearch` or `TaskCreate` before
delegating, so the span total moves between runs. Assert per tool name against that run's own
payloads, which is what `qa-compare.py` does.

**A tool call arriving after `SubagentStop` is still dropped, deliberately.** `SubagentStop` clears
the per-agent snapshot, and nothing observed produces a tool call after it. The alternative —
falling back to whatever turn is current — would attach a sub-agent's work to an unrelated trace,
which is worse than losing it. Covered by a unit test, not by this spec.

**Nested sub-agents.** An `Agent` call made by a sub-agent keeps its derived span id and parents
under the outer agent. A top-level `Agent` call carries no `agent_id` at all, so it parents under the
turn's `chat` span. Both are unit-tested; no live prompt in this suite produces nesting.

**Scope.** This is about tool calls inside a sub-agent. A main-session tool call that somehow landed
after its own turn's `Stop` hits the same code path but nothing observed produces that ordering, so
it is not asserted here.
