# opencode-v2/subagents

Delegation. OpenCode runs a sub-agent as a child session, linked from the parent's `subagent` tool
call.

| Spec | Asserts |
| --- | --- |
| [subagent-work-is-anchored-under-its-subagent-call](subagent-work-is-anchored-under-its-subagent-call.md) | The child's turn is an `invoke_agent` span under the parent's `subagent` tool span, with its own tokens |
