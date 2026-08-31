---
id: subagent-gets-its-own-invoke-agent-span
area: copilot/subagents
runtime: copilot
status: active
input: qa/tools/qa-session-copilot.sh, one turn whose prompt asks the model to delegate
duration: ~25s
settling: 25s
cleanup: keep
covers:
  - internal/source/copilot/copilot.go
  - internal/source/copilot/otelfile.go
  - cmd/copilot-on-event/main.go
---

## Given

The plugin installed into a throwaway home by `qa-session-copilot.sh`, exporting to the target in
`qa/config.local.json`, plus the QA recorder. Copilot's `task` tool needs no flag, unlike Codex's
sub-agents.

**Copilot's hooks cannot describe a sub-agent at all.** It fires a *partial* lifecycle of its own —
`userPromptSubmitted`, any `postToolUse`, `agentStop`, and never `sessionStart` or `sessionEnd` —
under its own session id, with nothing linking back to the parent conversation. That missing
`sessionStart` is what the plugin keys on, because the id is not usable: `copilot -p` names it
`call_<toolCallId>` and an interactive session gives it a plain UUID. It keys on the native-OTel
file as well — a sub-agent has no spans under its own conversation id — so that a real session
whose `sessionStart` hook failed is not mistaken for one. Either way the sub-agent's session is
dropped rather than minting a standalone, token-less conversation.

The native-OTel file *can* describe it, and does. So this spec makes two claims that pull in
opposite directions, and both matter:

- **the sub-agent is a first-class span**, an `invoke_agent` between the tool that spawned it and
  the tools it ran, carrying the same `gen_ai.agent.*` attributes Claude and Codex produce;
- **the sub-agent's hook session mints nothing**, so no second `gen_ai.conversation.id` appears
  anywhere in the dataset for this run.

The second is the one the recorder can prove independently: it records the `call_` sessions the
plugin threw away, so the spec knows exactly what was offered and refused.

## When

```sh
QA_COPILOT_BINARY=working-tree qa/tools/qa-session-copilot.sh \
  'Use the task tool to delegate to a sub-agent: ask it to run the shell command "echo qa-sub" and report the output. When it returns, reply with exactly the word done.' \
  spec-copilot-subagent
sleep 25
```

Shape, measured on plugin 0.1.25 with Copilot CLI 1.0.80: 10 hook invocations of which 3 belong to a
`call_` session, one native-OTel file with 3 `chat`, 2 `execute_tool` and 2 `invoke_agent` spans, and
4 spans in Dash0.

## Expectation

**From the hook record, independently**: rows for the parent session, and rows for one or more
`call_`-prefixed sessions. `qa-compare.py` counts the latter separately and says what they are; they
are not a reused run id.

**From the native-OTel file**, read by `qa/tools/qa-otel.py`: two `execute_tool` spans, of which one
is the `task` call and one ran inside the sub-agent; and two `invoke_agent` spans, of which the
parentless one is the turn itself and the nested one is the sub-agent. The tool reports the nested
count as `subagents` and the inner tools as `subagent_tools`.

**The expected span tree in Dash0**, which is the assertion no shared input can launder:

```
chat  →  execute_tool task  →  invoke_agent task  →  execute_tool bash
```

Four spans, one trace, one conversation id. Measured on the reference run, ids elided:

| Span | Parent | Notes |
| --- | --- | --- |
| `chat gpt-5.3-codex` | none — the turn's root | |
| `execute_tool task` | the `chat` span | `gen_ai.tool.call.id` is the spawning call id |
| `invoke_agent task` | the `task` span | `gen_ai.agent.name` = `task`, `gen_ai.agent.id` = that same `call_…` id |
| `execute_tool bash` | the `invoke_agent` span | ran inside the sub-agent; **no hook fired for it** |

**The turn's root `invoke_agent` has no span of its own.** It is the turn, which the pipeline's
`chat` span already represents, so emitting it too would duplicate the turn.

## Oracle

- Channel one, Dash0: `qa/tools/qa-compare.py qa/runs/spec-copilot-subagent`, whose parenting check
  proves every span's parent exists, then `dash0 spans query` filtered to `gen_ai.conversation.id`
  reading each span's `spanId` and `parentSpanId` to reconstruct the tree. The parenting check alone
  is not enough here: it proves a parent *exists*, not that it is the right one, and this spec's
  whole content is which parent.
- Channel two, the OTel file: `qa/tools/qa-otel.py qa/runs/spec-copilot-subagent`.
- The hook record: `record/index.jsonl`, for the `call_` sessions and for the single `agentStop`.

## Then

- Dash0 holds exactly 1 `chat` span, 2 `execute_tool` spans and **1 `invoke_agent` span** for the
  session — one per nested native `invoke_agent`, and none for the turn's root.
- The `task` span's parent is the `chat` span.
- The `invoke_agent` span's parent is the **`task` span**.
- The `bash` span's parent is the **`invoke_agent` span**, not the `task` span and not the `chat`
  span. This is the assertion the spec exists for: the native tree is reproduced rather than
  flattened.
- The `invoke_agent` span carries `gen_ai.agent.name` naming the agent kind.
- The `invoke_agent` span carries `gen_ai.agent.id` equal to the `task` span's
  `gen_ai.tool.call.id`. Per invocation, not per kind: the native `builtin:<kind>` value is
  deliberately not used, and two sub-agents of one kind must differ here.
- The `invoke_agent` span carries **no** `gen_ai.usage.*`. Attribution is flat, so those tokens are
  already on the turn's `chat` span and repeating them would double any sum across the trace.
- **No span carries a `gen_ai.conversation.id` beginning `call_`**, even though the recording shows
  the plugin was offered such a session and refused it.
- The turn's `chat` span usage includes the sub-agent's round-trips, so it is larger than Copilot's
  own root-`invoke_agent` roll-up.
- `qa-compare.py` exits `0`, and its tool table shows Dash0 holding more tool spans than
  `postToolUse` fired.

## Tolerance

**The model may not delegate.** The prompt asks for the `task` tool by name and the reference run
obeyed, but a model that answers directly produces no `task` span and no sub-agent. That is not a
finding — reword the prompt and re-run. A run with no `execute_tool task` span in the OTel file
never exercised this spec at all.

**The agent kind is Copilot's, not ours.** `task` is what the reference run produced. Assert that
`gen_ai.agent.name` is present and non-empty, never its value.

**The instance name the model chose is not an attribute.** `echo-runner` and the like live in the
`task` span's `gen_ai.tool.call.arguments`, where Copilot puts them. Nothing lifts them onto a key
of their own: the `dash0.gen_ai.tool.task.name` that used to do that was removed on 2026-08-28,
because a custom key is invisible to every backend feature while `gen_ai.agent.id` is not.

**How many tools the sub-agent runs is not fixed.** One is what the prompt asks for; a sub-agent
that also lists a directory produces more. Assert that every tool in the file appears in Dash0 and
that each is parented under its agent, not a count.

**Fewer `postToolUse` hooks than tool spans is correct here**, and `qa-compare.py` prints them as a
second opinion rather than an expectation for exactly this reason. Sub-agent tools fire no hook.

**Copilot's roll-up disagreeing with the chat sum is expected on this workload**, and
`qa-compare.py` prints the gap with that explanation. Measured 2026-08-28: 29563/232 against
52038/298. It is Copilot's arithmetic, not the plugin's.

**Ingest lag: 25 seconds.** See `## Settling` in [../../../setup.md](../../../setup.md).
