---
id: subagent-tool-spans-are-dropped-after-stop
severity: high
status: unaddressed
filed: no
found: 2026-08-21
affects: claude-on-event 0.1.24, and every earlier release with this code path
spec: qa/specs/session/sub-agent-tool-call-produces-a-span.md
---

# Tool calls made inside a sub-agent produce no spans

Every tool call a sub-agent makes after the spawning turn's `Stop` is silently discarded. Because the
`Task` tool is asynchronous, that is nearly all of them.

## What happens

`Stop` exports the turn's `chat` span and then calls `otlp.ClearTraceContext(sessionDir)`
(`internal/pipeline/pipeline.go:207`). `sendToolTrace` opens with
`otlp.LoadTraceContext(dataDir)` and returns `no trace context available for tool span` when the
context is gone (`internal/pipeline/pipeline.go:253`), so no span is built and nothing is sent.

The `Agent` tool's own `PostToolUse` reports `duration_ms` of 2 or 3. It launches the sub-agent and
returns; it does not wait. The spawning turn's `Stop` then fires 2.2 to 3.1 seconds later, measured
over four runs. So a sub-agent has roughly two and a half seconds from launch in which its tool calls
are still exportable, and it keeps working long after that.

`sendLLMTrace` is immune to the same ordering, and the reason makes this a one-sided omission rather
than an oversight about async. `SubagentStart` already saves a per-agent snapshot of the trace
context, and its comment names this exact hazard: the `SubagentStop` "still finds the spawning turn's
trace even when it arrives after Stop (context cleared)". `sendLLMTrace` reads that snapshot through
`otlp.LoadAgentTraceContext(dataDir, agentID)` when the event carries an `agent_id`
(`internal/pipeline/pipeline.go:360`). `sendToolTrace` never reads it, although the sub-agent's
`PostToolUse` payload carries the same `agent_id` and `agent_type`.

## Reproduction

```sh
QA_MODEL=haiku QA_ALLOWED_TOOLS="Task Agent Bash" qa/tools/qa-session.sh \
  'Use the Task tool (subagent_type general-purpose) to ask a sub-agent to run these three bash commands one after another, each as a separate Bash call: "sleep 1; echo one", then "sleep 1; echo two", then "sleep 1; echo three". When it returns, reply with exactly the word done.' \
  finding-subagent-tools
sleep 10
qa/tools/qa-compare.py qa/runs/finding-subagent-tools
```

Reported as `tool Bash: Dash0 has 0, PostToolUse fired 3`, with `execute_tool` 1 against 4 and a
total of 4 against 7.

The recorded ordering, from `record/index.jsonl`:

```
+ 6.18s PostToolUse   Agent  duration_ms=3      Task returns, sub-agent starts
+ 8.40s PreToolUse    Bash [in sub-agent]
+ 8.79s Stop                                    trace context cleared
+10.35s PostToolUse   Bash [in sub-agent]       dropped
+13.23s PostToolUse   Bash [in sub-agent]       dropped
+16.49s PostToolUse   Bash [in sub-agent]       dropped
+18.58s SubagentStop
```

Four sessions, same prompt family:

| Sub-agent's work | Tool calls | Spans |
| --- | --- | --- |
| one `Bash`, finished 0.3 s before the turn's `Stop` | 1 | 1 |
| one `Bash`, finished 0.4 s before the turn's `Stop` | 1 | 1 |
| one `Bash`, finished 0.7 s after the turn's `Stop` | 1 | 0 |
| three `Bash` calls over 10 s, all after the turn's `Stop` | 3 | 0 |

## Why it matters

The loss scales with how much work is delegated, which is the wrong direction: the more a session
hands to sub-agents, the less of it is observable. A sub-agent doing real work keeps none of its tool
spans.

**It is invisible from the telemetry alone.** Both `chat` spans and the `invoke_agent` span still
arrive, and their token counts and cost are correct, because the dropped spans carry no usage. So no
total moves and no error surfaces. Nothing about the trace looks wrong except that a sub-agent
appears to have spent fifteen seconds doing nothing. Two of the four runs above pass, which is how
this survived earlier sub-agent testing.

## Suggested fix

Give `sendToolTrace` the fallback `sendLLMTrace` already has: when the event carries an `agent_id`,
prefer `otlp.LoadAgentTraceContext(dataDir, agentID)` over the cleared global context. The snapshot is
already written by `SubagentStart` and already cleared by `SubagentStop`, so no new state is needed.

Worth checking as part of the same change: a sub-agent's tool call that arrives after `SubagentStop`
has cleared the per-agent snapshot, and nested sub-agents, where the correct parent is the inner
agent's span rather than the outer one's.
