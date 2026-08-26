# claude

What a Claude Code session looks like in Dash0 once it ends. The suite is organised runtime-first —
one tree per coding agent, topics underneath — because a run is one driver, one credential and one
cost profile. Claude Code is the only runtime covered today.

| Topic | Covers |
| --- | --- |
| [session](session/README.md) | Spans, parenting, token counts, the attribute surface, sub-agents |
| [mcp](mcp/README.md) | MCP calls: the server attribute, the tool name, and a call that failed |
| [skills](skills/README.md) | Skill invocation, by the person and by the model |

Each topic keeps its own coverage map, and each records what is deliberately not written and why.

## What no spec here can cover

**The wire format.** The managed install cannot be reconfigured for one session, so the plugin's
debug payload log cannot be turned on and no run here sees the bytes on the wire. The content of
`gen_ai.tool.call.arguments`, `gen_ai.tool.call.result` and `exception.message` is therefore
unverifiable: the API returns all three redacted, so a spec checks presence and never value.
`test/e2e/` owns those against a mock.
