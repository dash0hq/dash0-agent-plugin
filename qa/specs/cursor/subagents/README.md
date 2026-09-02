# cursor / subagents

Delegation on Cursor. This area has one spec and it records a gap rather than an invariant.

| Spec | Asserts |
| --- | --- |
| [subagent-work-reaches-no-span](subagent-work-reaches-no-span.md) | What a delegating session produces today: the parent's turn, complete and correct, and nothing at all for the sub-agent |

## What Cursor gives a hook, measured

Measured 2026-09-01 on `qa/runs/probe-cursor-subagent` against cursor-agent 2026.08.31-4057e58. The
prompt asked for delegation and got it, through a `Task` tool with `subagent_type: shell`. The
recording:

```
sessionStart          parent session
beforeSubmitPrompt    parent session
preToolUse   Task     parent session   tool_use_id call_…      <- no postToolUse follows
preToolUse   Shell    SUB-AGENT session, its own fresh UUID
postToolUse  Shell    SUB-AGENT session
afterAgentResponse    parent session
sessionEnd            parent session
```

Three facts, each of which breaks something:

- **`subagentStart` and `subagentStop` never fired.** The plugin's whole sub-agent path —
  `SubagentStart`'s context snapshot, `SubagentStop`'s `invoke_agent` span — is unreachable in this
  mode. `internal/source/cursor` already drops `subagentStart` on purpose; it is `subagentStop` that
  is missing and that would have produced the span.
- **The `Task` call fires `preToolUse` and no `postToolUse`.** So the delegation itself produces no
  `execute_tool` span. There is no anchor for a sub-agent's work to hang under, which is what
  `internal/source/codex.anchorSpawnAgent` provides on Codex.
- **The sub-agent runs under a session id of its own**, a plain UUID with no `parent_conversation_id`
  and no field of any kind linking it to the parent. It fires no `sessionStart`, so
  `internal/pipeline` has no trace context for it, and `sendToolTrace` returns
  `no trace context available for tool span`. That error goes to stderr, which the TUI swallows.

**The consequence: every delegated tool call is absent from telemetry, silently.** The parent's turn
is complete and its counts reconcile perfectly, so nothing count-based fails. See
[../../../findings/cursor-subagent-work-produces-no-span.md](../../../findings/cursor-subagent-work-produces-no-span.md).

## How the harness makes it visible

Two ways, and neither is the span comparison.

`qa/tools/qa-session-cursor.sh` identifies the main session by `sessionStart` and lists every other
recorded session in the manifest as `subagent_sessions`. `qa-compare.py` reads that list and reports
a **finding** when it is non-empty, because the counts alone would pass. Without the list a
delegating run reported "the run id was reused", which sends the reader after a driver bug that is
not there — Cursor's sub-agent id is indistinguishable from a real session, unlike Copilot's
recognisable `call_` prefix.

The transcript is the second way. It records the sub-agent's `Shell` call inline, so
`qa-transcript-cursor.py` sees work no span exists for. That is a real independent detection, and it
is the reason the transcript's tool table is still printed even though it cannot serve as an
expectation.

## Deliberately not written

**"An `invoke_agent` span is parented on the spawning tool call."** That is the invariant the other
three runtimes' sub-agent specs assert, and there is no `invoke_agent` span to assert it about. Write
it when `subagentStop` fires.

**"A sub-agent's usage is counted once."** Same reason. The sub-agent's turn produces no
`afterAgentResponse` in the parent session and no span anywhere, so there is no number to count once.

**"Delegation always happens."** It does not. Cursor answers a delegable prompt directly often
enough that a run which recorded one session is a re-run, not a finding. The spec says so.
