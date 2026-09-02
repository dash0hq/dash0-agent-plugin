---
id: subagent-work-reaches-no-span
area: cursor/subagents
runtime: cursor
status: active
known_failure: cursor-subagent-work-produces-no-span
input: qa/tools/qa-session-cursor.sh, one turn whose prompt asks for delegation
duration: ~50s
settling: 25s
cleanup: keep
covers:
  - internal/source/cursor/cursor.go
  - internal/pipeline/pipeline.go
---

## Given

The same setup as
[../session/single-turn-agrees-with-the-hooks-that-fed-it](../session/single-turn-agrees-with-the-hooks-that-fed-it.md),
with a prompt that asks for the work to be delegated.

**This spec records a gap.** It is written as an assertion of what happens today so that a change in
either direction is caught: Cursor starting to fire `subagentStop`, or the plugin starting to lose
the parent's turn as well. The gap itself is
[../../../findings/cursor-subagent-work-produces-no-span.md](../../../findings/cursor-subagent-work-produces-no-span.md),
and `## What Cursor gives a hook, measured` in [README.md](README.md) has the recording that
explains it.

**Delegation is not guaranteed.** Cursor answers a delegable prompt directly often enough that the
prompt has to insist, and a run whose recording holds one session only did not delegate. That is a
re-run, not a result.

## When

```sh
QA_CURSOR_BINARY=working-tree qa/tools/qa-session-cursor.sh \
  'Use a subagent to run the shell command: echo qa-sub. Delegate it, do not run it yourself. When the subagent returns, reply with exactly the word done.' \
  spec-cursor-subagent
sleep 25
qa/tools/qa-compare.py qa/runs/spec-cursor-subagent
qa/tools/qa-transcript-cursor.py qa/runs/spec-cursor-subagent
```

The driver prints `<uuid> ran as a sub-agent — its own id, no sessionStart` when delegation
happened. That line is the precondition for reading anything below.

Shape, measured 2026-09-01 on `qa/runs/probe-cursor-subagent`: 7 hook invocations across **two**
session ids — 5 in the parent, 2 in the sub-agent's — 1 span in Dash0, and `"spans_logged": 1` in
the manifest.

## Expectation

**The parent's turn is complete and correct.** One `chat` span, carrying the parent turn's usage
from its `afterAgentResponse` payload. The delegation costs the parent nothing in correctness, which
is what makes the gap invisible to a count.

**No `execute_tool` span exists for the `Task` call.** Cursor fires `preToolUse` for it and no
`postToolUse`, and `internal/pipeline` acts on the post events only. So the delegation has no anchor
span — the thing `anchorSpawnAgent` provides on Codex and the `task` tool's own native span provides
on Copilot.

**No `invoke_agent` span exists.** `subagentStop` never fires, so the pipeline's `SubagentStop`
branch is never reached.

**No `execute_tool` span exists for the sub-agent's own tool call**, even though its `postToolUse`
fired and the recorder captured it. Its session has no `sessionStart` and no `UserPromptSubmit`, so
`sendToolTrace` finds no trace context and returns an error to stderr, which the TUI swallows.

**The transcript records that tool call anyway.** It is Cursor's own file and it logs the sub-agent's
work inline, so the transcript reports a tool call the spans do not have. That is the independent
detection of the gap, and the only one this runtime offers.

**`qa-compare.py` exits `1`, and that is the pass signal for this spec.** The finding it prints is
`sub-agent: N hook invocation(s) ran under a sub-agent session and produced no span`. An exit of `0`
on a run whose driver reported a sub-agent session means the finding stopped being reported, which is
a harness regression, not a fix.

## Oracle

- The hook record: `record/index.jsonl`, grouped by `session_id`. Two ids, one of which fired no
  `sessionStart`. This is the whole evidence and it owes the plugin nothing.
- Channel one, Dash0: `qa/tools/qa-compare.py qa/runs/spec-cursor-subagent`, whose span counts are
  the parent turn's alone and whose sub-agent finding is the assertion.
- Channel two, the transcript: `qa/tools/qa-transcript-cursor.py`, which reports the sub-agent's
  tool call.
- `plugin-debug.log` proves the span was never *built* rather than built and lost: it holds exactly
  the one `chat` span.
- The manifest's `subagent_sessions` field, which is how `qa-compare.py` tells a sub-agent's session
  from a reused run id.

## Then

- The driver printed a `ran as a sub-agent` line. Without it, re-run.
- The recording holds exactly two session ids, and the sub-agent's fired only `preToolUse` and
  `postToolUse`.
- No recorded event is `subagentStart` or `subagentStop`.
- The parent's `preToolUse` for `Task` has no matching `postToolUse` with the same `tool_use_id`.
- Dash0 holds exactly 1 span: the parent's `chat`. No `execute_tool`, no `invoke_agent`.
- `plugin-debug.log` holds exactly 1 span.
- The transcript reports at least one `tool_use` block for the delegated command.
- `qa-compare.py` exits `1` with the sub-agent finding, and `qa-attrs.py` exits `0` — the one span
  present is fully within the contract.

## Tolerance

**Which sub-agent type Cursor picks is not fixed.** The reference run got `subagent_type: shell`. A
different type changes nothing asserted here.

**The parent may run tools of its own** before or after delegating, adding `execute_tool` spans to
the parent's trace. Those are legitimate; the claim is that none of them belongs to the sub-agent's
`tool_use_id`.

**A run that did not delegate looks like a pass of the session spec, not a failure of this one.** One
session id, `qa-compare.py` exits `0`, and nothing about delegation was tested. Check the driver's
output before recording a result.

**This is measured on the interactive TUI, which is the only mode the driver has.** Cursor's
background-agent and worker modes drive hooks through a different dispatcher — `index.js` fires
`subagentStart` and `subagentStop` from a server-driven request stream — so the gap may not exist
there. Nothing here speaks for those modes.
