---
id: foreground-child-is-an-agent-under-its-call
area: pi/subagents
runtime: pi
status: active
input: QA_PI_SUBAGENTS=1 qa/tools/qa-session-pi.sh, one turn that delegates one file read in the foreground
duration: ~30s
settling: none; qa-pi-compare.py polls
cleanup: keep
covers:
  - pi/index.ts
  - internal/source/pi/subagent.go
  - internal/source/pi/pi.go
---

## Given

The Pi extension and helper **from this working tree**, with
`QA_PI_SUBAGENTS=1`: pi-subagents is loaded, and the run has its own agent
directory, so no other extension, agent or setting of the machine is read. The
plugin exports to `piDataset`. See `### Pi` under `## Runtimes` in
[setup.md](../../../setup.md).

A foreground child is a Pi session inside the parent's process. It does not
load this plugin. The parent's `subagent` tool result is the only record that
the plugin gets.

## When

```sh
QA_PI_SUBAGENTS=1 qa/tools/qa-session-pi.sh \
  'Call the subagent tool exactly once with agent "delegate" and task "Read a.txt with the read tool and reply with its content only." Do not use async. Then reply with exactly the word done.' \
  spec-pi-subagent-foreground
qa/tools/qa-pi-compare.py qa/runs/spec-pi-subagent-foreground
```

## Expectation

From `stream.jsonl`, Pi's own stream: the `subagent` `tool_execution_end`
event has one row in `result.details.results`, with agent `delegate`, a
`sessionFile` and `usage`. The child's own session file is the second record
of the child: its header names the parent's session file, and the sum of its
assistant messages' usage is the child's tokens. The plugin never reads that
file.

- The parent's own model calls and tool calls are as in any turn, and their
  tokens are only the parent's.
- One `invoke_agent delegate` span, parented on the `execute_tool subagent`
  span, in the same trace.
- Under it, one `chat` span with the child's tokens: input is `input +
  cacheRead + cacheWrite`, output is `output`. The `invoke_agent` span has no
  tokens.

## Oracle

- Channel one: Dash0, through `qa-pi-compare.py`.
- Channel two: Pi's stream for the parent and the result row, and the child's
  session file for the child's tokens.

## Then

- `qa-pi-compare.py` exits `0` and prints `AGREEMENT`.
- `foreground children -> invoke_agent` is `1`, and the agent under the
  `subagent` call is `delegate`.
- `child file names the parent` agrees: the child file is a child of this run.
- `child file -> tool result` agrees: the tool result reports what the child
  file holds.
- `tokens on chat under agent` agrees with both.
- `same trace as parent` agrees.
- The parent's `input tokens` and `output tokens` rows agree without the child,
  so no token is on two spans.

If the stream has no `subagent` call, the model did not delegate. Run it again.

## Tolerance

Exact. The aggregate `chat` span is not one model call: its time range is
the whole child run, `dash0.pi.subagent.timing=aggregate` marks it, and the
comparer leaves it out of the parent's rows.

## Verified

2026-11-13 on `qa/runs/spec-pi-subagent-foreground`: AGREEMENT. Parent 8174 in
and 120 out; child 17292 in and 335 out on the child file, the tool result and
Dash0.
