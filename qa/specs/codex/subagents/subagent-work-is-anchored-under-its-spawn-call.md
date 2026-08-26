---
id: subagent-work-is-anchored-under-its-spawn-call
area: codex/subagents
runtime: codex
status: draft
input: qa/tools/qa-session-codex.sh with QA_CODEX_MULTI_AGENT=1, one sub-agent
duration: ~20s
settling: 8s
cleanup: keep
covers:
  - internal/source/codex/codex.go
  - internal/source/codex/rollout.go
  - internal/pipeline/pipeline.go
  - internal/otlp/tracecontext.go
---

## Given

The plugin provisioned by `qa-session-codex.sh`, plus the recorder, with
`QA_CODEX_MULTI_AGENT=1`. Sub-agents are behind a feature flag in codex-cli 0.149.1 and
without it the model answers a delegation prompt itself — see
[../../learnings/deadend-codex-does-not-delegate-without-multi-agent-mode.md](../../../learnings/deadend-codex-does-not-delegate-without-multi-agent-mode.md).

**The spawn call's span is the sub-agent's anchor.** The plugin renames that
`execute_tool` span to `Agent` and derives its span id from the agent id, so the
`invoke_agent` span and every tool call the sub-agent makes can parent onto it. The link
between the two is the one thing Codex has moved twice: 0.142.5 put `agent_id` in the
spawn response, 0.149.1 returns only `{"task_name": ...}` and records the mapping in the
rollout instead, as a `SubAgentActivity` item keyed by the spawn call id.

One sub-agent, one task. The reuse and nesting cases have their own spec, because they
fail for a different reason.

## When

```sh
QA_CODEX_MULTI_AGENT=1 qa/tools/qa-session-codex.sh \
  'Spawn a sub-agent to run the shell command "echo from-the-subagent". Wait for it to finish, then reply with exactly the word delegated.' \
  spec-codex-subagent-anchor
sleep 8
```

Shape, measured on the working tree at 0.1.25 with codex-cli 0.149.1: 11 hook
invocations, 5 spans, one `SubagentStart` and one `SubagentStop`.

## Expectation

From `record/` and the rollouts, neither of which the plugin writes.

**Which agent the spawn call created.** The main rollout carries, at the moment the spawn
returns:

```json
{"type":"event_msg","payload":{"type":"item_completed","item":{
  "type":"SubAgentActivity","id":"<spawn call id>","kind":"started",
  "agent_thread_id":"<agent id>"}}}
```

The item's `id` equals the `tool_use_id` on the spawn call's `PostToolUse` payload, so the
record joins the two without any inference. `kind` must be `started`; Codex writes
`interacted` for an exchange with an agent already running, under a different call id.

**Which hooks belong to that agent.** Every row in `record/index.jsonl` whose payload
carries that `agent_id` is the sub-agent's own work — on the reference run, one `Bash`
`PostToolUse` — and `SubagentStop` carries it too.

So the expected tree, computed from the record alone:

```
chat
  execute_tool Agent                 <- the spawn call, renamed
      invoke_agent                   <- from SubagentStop
      execute_tool Bash              <- the one PostToolUse carrying the agent id
  execute_tool <the wait call>
```

**The rename is asserted, not assumed.** `Agent` is what the plugin calls the anchor, and
the payload says `collaborationspawn_agent`. That is a documented rule rather than an
independent fact, so the spec asserts the *parenting* from the record and the *name* as
the rule; a run where parenting holds under the raw name would be a pass with a note, not
a failure.

## Oracle

- Channel one, Dash0: `qa/tools/qa-compare.py qa/runs/spec-codex-subagent-anchor` for the
  counts and the orphan check, then `dash0 spans query` filtered to
  `gen_ai.conversation.id`, reading `spanId`, `parentSpanId` and `gen_ai.agent.id` to
  build the tree.
- Channel two, the record: `record/index.jsonl` for the hooks carrying the agent id, and
  the `SubAgentActivity` item in `rollout.jsonl` for the mapping.

## Then

- `qa-compare.py` exits `0` and prints `Parenting: every span's parent is a span of this
  session.` **No span has a parent that no other span carries.** This is the assertion the
  spec exists for: both ways of breaking the anchor show up here first.
- Exactly one `execute_tool` span is named `Agent`, and its `gen_ai.tool.call.id` is the
  spawn call's `tool_use_id`.
- The `invoke_agent` span's parent is that `Agent` span, and its `gen_ai.agent.id` is the
  `agent_thread_id` from the `SubAgentActivity` item.
- Every `execute_tool` span whose `gen_ai.agent.id` is that agent has the `Agent` span as
  its parent, not the `chat` span.
- The `Agent` span's own parent is the turn's `chat` span, which is the root.
- The number of `execute_tool` spans equals the number of `PostToolUse` hooks.

## Tolerance

**Whether the model delegates at all is the model's choice.** A run with no
`SubagentStart` did not delegate; discard it and re-run rather than reporting it. With
`QA_CODEX_MULTI_AGENT=1` and this prompt it delegated on every attempt, but that is a
tendency, not a guarantee.

**The wait call's name and count are not asserted.** `collaborationwait_agent`,
`collaborationfollowup_task` and `collaborationlist_agents` come and go with how the model
manages its agent; they are ordinary tool calls under the turn and none of them is the
anchor.

**The prefix on the spawn tool is not asserted.** It was bare `spawn_agent` in 0.142.5 and
`collaborationspawn_agent` in 0.149.1, with no separator. The plugin matches the suffix,
and so should any assertion about the raw name.

**A run that spawns more than one agent belongs to the other spec.** If the model nests or
reuses, the extra structure is correct, not a finding here — but re-run for a clean single
agent before asserting the tree above.

**Ingest lag.** A few seconds, as everywhere in this suite.
