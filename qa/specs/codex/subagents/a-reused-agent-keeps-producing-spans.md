---
id: a-reused-agent-keeps-producing-spans
area: codex/subagents
runtime: codex
status: draft
input: qa/tools/qa-session-codex.sh with QA_CODEX_MULTI_AGENT=1, a follow-up task
duration: ~25s
settling: 8s
cleanup: keep
covers:
  - internal/pipeline/pipeline.go
  - internal/otlp/tracecontext.go
  - internal/source/codex/codex.go
---

## Given

The plugin provisioned by `qa-session-codex.sh`, plus the recorder, with
`QA_CODEX_MULTI_AGENT=1`.

**A Codex `SubagentStop` ends a task, not the agent.** The same `agent_id` stops, then
runs more tools, spawns agents of its own, and stops again — with no second
`SubagentStart` in between. That is the opposite of Claude Code, where an agent stops once
and anything arriving afterwards is stale, and the pipeline treats the two differently on
purpose: see
[../../learnings/hooks-a-codex-subagent-is-reusable-a-claude-one-is-not.md](../../../learnings/hooks-a-codex-subagent-is-reusable-a-claude-one-is-not.md).

This is the case that hides. A sub-agent probe that spawns once and finishes never touches
it, and the counts of such a run reconcile perfectly while a reusing session silently
loses spans.

## When

```sh
QA_CODEX_MULTI_AGENT=1 qa/tools/qa-session-codex.sh \
  'Spawn a sub-agent named worker that runs the shell command "echo alpha". Wait for it to finish. Then send that SAME agent a follow-up task to run the shell command "echo beta", and wait for that too. Then reply with exactly the word delegated.' \
  spec-codex-agent-reuse
sleep 8
```

Shape, measured on the working tree at 0.1.25 with codex-cli 0.149.1: 18 hook
invocations, 6 `PostToolUse`, **one** `SubagentStart` and **two** `SubagentStop` for the
same agent, 9 spans. The agent's second `Bash` call lands 5 seconds after its first stop.

The nested prompt in [README.md](README.md) reaches the same invariant by a different
route: the model puts the second spawn inside the first, so that spawn is itself work done
after a stop. Measured there: 23 hooks, 7 `PostToolUse`, 11 spans, two levels of nesting.
Either input satisfies this spec; run the follow-up one, which is the more direct.

## Expectation

From `record/index.jsonl` alone, which the plugin never reads.

**One `execute_tool` span per `PostToolUse`, with no exceptions for who made the call.**
The recording says how many tool calls happened and which agent made each. A call carrying
an `agent_id` whose `SubagentStop` has already been recorded is exactly the case at issue,
and it is still a tool call.

Partition the recording to make the assertion sharp:

- rows whose `hook_event_name` is `PostToolUse`, grouped by `agent_id` (absent means the
  main session);
- for each agent, the timestamp of its **first** `SubagentStop`;
- the rows for that agent recorded after it — on the reference run, one `Bash` call at
  `+5s`, and on the nested input a `spawn_agent` call.

**A spawn made after a stop still anchors.** If the post-stop call is itself a
`spawn_agent`, the agent it creates must parent under it, exactly as in
[subagent-work-is-anchored-under-its-spawn-call](subagent-work-is-anchored-under-its-spawn-call.md).
Those two failures are one symptom with two causes: the dropped span *is* the missing
anchor, so a run that loses it shows both a short tool count and orphans.

**Two stops mean two `invoke_agent` spans.** One per completed task, both carrying the
same `gen_ai.agent.id`. That repetition is correct and is the thing most likely to be
misread as a double export.

## Oracle

- Channel one, Dash0: `qa/tools/qa-compare.py qa/runs/spec-codex-agent-reuse`. Its tool
  table compares `execute_tool` spans against `PostToolUse` hooks, which is the count half,
  and its parenting line is the orphan half.
- Channel two, the plugin's debug log: `plugin-debug.log` holds every span the plugin
  emitted. It separates "never built" from "lost in transit", and on the original defect it
  proved the spans were never built — 5 emitted for 7 hooks.

## Then

- `qa-compare.py` exits `0`.
- The number of `execute_tool` spans equals the number of `PostToolUse` hooks. On the
  reference run, 6 and 6; on the nested input, 7 and 7.
- Every `PostToolUse` recorded **after** its agent's first `SubagentStop` has a span with
  the matching `gen_ai.tool.call.id`. At least one such call must exist, or the run did not
  exercise the spec and must be re-run.
- Each of those spans has a parent, and the parent is a span of this session.
- `Parenting: every span's parent is a span of this session.`
- There are as many `invoke_agent` spans as `SubagentStop` hooks, and on this input two of
  them carry the same `gen_ai.agent.id`.
- `plugin-debug.log` holds the same number of spans Dash0 does. A shortfall here means the
  spans were never built, which is this defect; a shortfall only in Dash0 is transport.

## Tolerance

**How the model reuses an agent is its choice.** It may send a follow-up, nest a spawn, or
spawn a fresh agent instead — the last of which does *not* exercise this spec. The check is
mechanical: at least one `PostToolUse` recorded after that agent's first `SubagentStop`. A
run with none is discarded and re-run, not reported as a pass.

**Counts vary between runs.** 6 and 7 are the measured shapes, not constants. Only the
equality between hooks and spans is asserted.

**A repeated `gen_ai.agent.id` across `invoke_agent` spans is not a duplicate.** Two
completed tasks, two spans. A consumer counting agents must count distinct ids; that is a
property of the runtime, not a defect, and it is the main thing this spec documents for
whoever reads a Codex trace next.

**Claude must keep the opposite behaviour**, where a call after `SubagentStop` is stale and
is dropped. That is asserted by unit tests rather than here, because provoking it live
would need Claude to reuse an agent, which it does not do.

**Ingest lag.** A few seconds, as everywhere in this suite.
