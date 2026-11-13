---
id: child-that-exports-itself-is-counted-once
area: pi/subagents
runtime: pi
status: active
input: QA_PI_SUBAGENTS=1 QA_PI_SUBAGENTS_CHILD_EXPORT=1 qa/tools/qa-session-pi.sh, the foreground prompt
duration: ~30s
settling: none; qa-pi-compare.py polls for the parent and for the child
cleanup: keep
covers:
  - pi/index.ts
  - internal/source/pi/subagent.go
---

## Given

As in [foreground-child-is-an-agent-under-its-call](foreground-child-is-an-agent-under-its-call.md),
and `QA_PI_SUBAGENTS_CHILD_EXPORT=1`. The run's settings then set the
pi-subagents option `subagents.defaultSubagentOnlyExtensions` to the recorder
and the plugin. `pi/README.md` documents this option for users who want
per-call detail of a foreground child.

The child then loads its own copy of the plugin in the parent's process and
exports its own session. The parent still sees the tool result with the same
totals. The parent's copy must not count those tokens again.

## When

```sh
QA_PI_SUBAGENTS=1 QA_PI_SUBAGENTS_CHILD_EXPORT=1 qa/tools/qa-session-pi.sh \
  'Call the subagent tool exactly once with agent "delegate" and task "Read a.txt with the read tool and reply with its content only." Do not use async. Then reply with exactly the word done.' \
  spec-pi-subagent-child-export
qa/tools/qa-pi-compare.py qa/runs/spec-pi-subagent-child-export
```

## Expectation

- The `invoke_agent delegate` span is still under `execute_tool subagent`,
  so the trace shows that a child ran.
- It has no `chat` child with tokens: `tokens on chat under agent` is `(0, 0)`.
- The child's own session has one `chat` span per model call in its file, with
  the same tokens, linked to the parent session.

## Oracle

As in the foreground spec. The child file is the record of the child's tokens.

## Then

- `qa-pi-compare.py` exits `0` and prints `AGREEMENT`.
- `tokens on chat under agent` is `(0, 0)` on both sides.
- `child session model calls`, `tokens (in, out)` and `linked to parent
  session` agree with the child file.

## Tolerance

Exact. A sum of tokens over the parent's session and the child's session is
the child's tokens one time.

## Verified

2026-11-13 on `qa/runs/spec-pi-subagent-child-export`: AGREEMENT. The agent
span had no tokens; the child's session had 2 calls, 17292 in and 332 out, on
the child file and Dash0.
