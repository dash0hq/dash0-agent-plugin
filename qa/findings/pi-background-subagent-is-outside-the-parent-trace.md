# A background pi-subagents child is not in the parent's trace

- **spec.** [pi/subagents/background-child-is-a-linked-session](../specs/pi/subagents/background-child-is-a-linked-session.md)
- **found.** 2026-11-13, on `qa/runs/spec-pi-subagent-background`
- **against.** Pi 1.5.0, pi-subagents 0.88.0, plugin working tree at 0.1.32
- **status.** open, by design. The child is reported in full, as a separate session.

## What happens

A `subagent` call with `async: true` returns at once. The child then runs in a
detached runner process, and that process loads this plugin from Pi's settings.
Dash0 holds two sessions:

```
parent  chat (root)
          execute_tool subagent   89 ms, no invoke_agent under it
          execute_tool bg_wait
child   chat (root)               dash0.pi.session.parent.id = parent session
          chat
          execute_tool read
```

The child's model calls, tools and tokens are all there and agree with its
session file. They are not under the parent's `subagent` span, and no
`invoke_agent` span names the child agent.

## Why

1. The parent's `subagent` tool result for a background launch has no result
   rows, only a run ID. The parent has nothing to put under the tool span.
2. The child's turn starts after the parent's turn has moved on. To join the
   parent's trace, the child would need the parent's trace ID and the tool
   span's ID. pi-subagents passes the parent session file to the child
   (`parentSession` in the child header), but not the tool call.
3. A `bg_wait` result carries the child's totals, but only when the model
   calls `bg_wait`. A completion that arrives as a wake message is a custom
   message that the extension does not see as a tool result.

## What would fix it

pi-subagents passing the launching tool call ID to the child, for example in
the child's session header or an environment variable of the runner. The child
would then put its root under `invoke_agent` in the parent's trace, as OpenCode
V2 does with its anchor. That is a change to pi-subagents, not to this plugin.

## Until then

`dash0.pi.session.parent.id` links the child session to the parent session.
`pi/README.md` states the limit.
