---
id: background-child-is-a-linked-session
area: pi/subagents
runtime: pi
status: active
input: QA_PI_SUBAGENTS=1 qa/tools/qa-session-pi.sh, one turn that delegates one file read with async true and waits for it
duration: ~40s
settling: none; qa-pi-compare.py polls for the parent and for the child
cleanup: keep
covers:
  - pi/index.ts
  - internal/source/pi/pi.go
---

## Given

As in [foreground-child-is-an-agent-under-its-call](foreground-child-is-an-agent-under-its-call.md),
with `QA_PI_SUBAGENTS=1`. A background child is a Pi session in a detached
runner process. That process loads the extensions in the run's own settings,
so the plugin runs in the child and exports it as a separate session.

## When

```sh
QA_PI_SUBAGENTS=1 qa/tools/qa-session-pi.sh \
  'Call the subagent tool exactly once with agent "delegate", async true, and task "Read a.txt with the read tool and reply with its content only." Then call bg_wait with that run id. Then reply with exactly the word done.' \
  spec-pi-subagent-background
qa/tools/qa-pi-compare.py qa/runs/spec-pi-subagent-background
```

## Expectation

From `stream.jsonl`: the `subagent` call returns no result rows, and the
`bg_wait` result lists the completed child with its `sessionFile`. The child's
session file has the child's session ID, its model calls and their usage.

- The parent's turn has `execute_tool subagent` and `execute_tool bg_wait`, and
  no `invoke_agent` span.
- The child's session, read by its own session ID, has one `chat` span per
  model call in its file, with the same tokens.
- Every child span has `dash0.pi.session.parent.id` equal to the parent's
  session ID.

## Oracle

- Channel one: Dash0, through `qa-pi-compare.py`, which also polls for the
  child's session.
- Channel two: Pi's stream for the parent, and the child's session file.

## Then

- `qa-pi-compare.py` exits `0` and prints `AGREEMENT`.
- `foreground children -> invoke_agent` is `0`.
- `background <id> model calls` and `tokens (in, out)` agree with the child
  file.
- `linked to parent session` agrees.

If the stream has no `bg_wait` completion, the model did not wait. Run it
again: without the completion the run has no record of the child file.

## Tolerance

Exact. The child is not in the parent's trace. That is the design, see
`pi/README.md`.

## Verified

2026-11-13 on `qa/runs/spec-pi-subagent-background`: AGREEMENT. Parent 3 calls,
13240 in and 373 out; child 2 calls, 17284 in and 337 out on the child file
and Dash0, linked to the parent session.
