# pi/subagents

Delegation in Pi. Pi has no built-in sub-agent. The
[pi-subagents](https://github.com/nicobailon/pi-subagents) extension adds a
`subagent` tool, and it runs a child in one of two places. Each place reaches
Dash0 differently, so each has its own spec.

**Every spec here needs `QA_PI_SUBAGENTS=1`.** The driver then gives Pi an
agent directory inside the run, whose settings load the recorder, the plugin,
the `claude-code` provider and pi-subagents, and nothing else. A background
child runs in a detached runner process that reads Pi's settings, not the
parent's `-e` paths, so `--no-extensions` cannot isolate it. The pi-subagents
package comes from the caller's agent directory and is read, not changed. Its
version is in `meta.json` as `pi_subagents`.

| Invariant | Spec | Status |
| --- | --- | --- |
| A foreground child is an `invoke_agent` span under its `subagent` call, in the parent trace, with its total tokens counted one time | [foreground-child-is-an-agent-under-its-call](foreground-child-is-an-agent-under-its-call.md) | active, passing |
| A background child is its own session, linked to the parent session, with every model call and token | [background-child-is-a-linked-session](background-child-is-a-linked-session.md) | active, passing |
| With the plugin also loaded into foreground children, the child exports itself and the parent's agent span carries no tokens | [child-that-exports-itself-is-counted-once](child-that-exports-itself-is-counted-once.md) | active, passing |

## What pi-subagents does with extensions, measured

Measured on 2026-11-13 with pi-subagents 0.88.0 and Pi 1.5.0:

| Child | Runs in | Loads this plugin | Before this change |
| --- | --- | --- | --- |
| Foreground (default) | the parent's Pi process | no, never the parent's ambient extensions | only `execute_tool subagent`. The child's model calls, tools and tokens were lost |
| Background (`async: true`) | a detached runner process | yes, from Pi's settings | its own session, linked by `dash0.pi.session.parent.id` |
| Foreground with `subagents.defaultSubagentOnlyExtensions` naming the plugin | the parent's Pi process | yes | its own session, linked |

The foreground `subagent` tool result has one row per child with `agent`,
`model`, `usage` (`input`, `output`, `cacheRead`, `cacheWrite`, `cost`,
`turns`) and `sessionFile`. The plugin copies the totals. It does not read the
child's session file. The specs read that file as the independent record.

## Not written, with the reason

| Invariant | Why not |
| --- | --- |
| A background child is inside the parent's trace | It is not, by design: the `subagent` call returns before the child works, and pi-subagents does not give the child the parent's tool call. The link is the session parent. `pi/README.md` records it |
| Per-call timing of a foreground child | The plugin reports totals on one aggregate `chat` span (`dash0.pi.subagent.timing=aggregate`). Per-call detail is the opt-in that the third spec covers |
| Parallel and chain calls | The same rows, several per call. Unit tests cover several children per call. Worth a spec once a prompt makes the model fan out reliably |
| A failed child is marked | No way to make a child fail on demand. Unit tests cover `exitCode` other than 0 |
