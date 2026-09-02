# A Cursor sub-agent's session id is indistinguishable from a real session

Both Cursor and Copilot run a sub-agent's hook lifecycle under a session id of its own, and
they are told apart differently.

Copilot mints a synthetic `call_<toolCallId>`, recognisable from the id alone, which is what
`qa-compare.py` matched on. Cursor mints a plain UUID with no `parent_conversation_id` and no
field of any kind linking it to the parent. Measured 2026-09-01 on
`qa/runs/probe-cursor-subagent`: the parent ran under `308e3c66-…` and the sub-agent under
`3289cf19-…`, and nothing in either payload connects them.

So the `call_` rule found nothing, the extra id landed in the report's "invocation(s) from an
earlier session in this directory" bucket, and both the driver and `qa-compare.py` said the run
id had been reused. It had not. That sends the reader after a driver bug that is not there, and
it hides the real finding —
[cursor-subagent-work-produces-no-span](../findings/cursor-subagent-work-produces-no-span.md).

**How to apply:** the distinguishing fact is `sessionStart`, not the id. A Cursor sub-agent
session fires only `preToolUse` and `postToolUse`; the real session fires `sessionStart` and
`sessionEnd`. `qa-session-cursor.sh` picks the main session by `sessionStart`, lists the rest in
the manifest as `subagent_sessions`, and `qa-compare.py` reads that list rather than
re-deriving the rule. `is_subagent_session` in `qa-compare.py` holds both rules with the reason
for each.

A run whose recording holds two ids that **both** fired `sessionStart` is a different thing:
`cursor-agent` started twice, which the driver does exactly once, and the driver warns about it
separately. That is not a reused run id either. Reuse alone is handled before the launch, by
`INDEX_BASELINE` — the row count taken just before `cursor-agent` starts. Every figure the run
reads back is scoped to the rows appended after it, so an earlier run under the same run id
cannot supply this run's `sessionStart`. Keep the three messages apart: the sub-agent id is
normal on a delegating session, two `sessionStart`s is a driver bug, and reuse is a note.
