# claude

What a Claude Code session looks like in Dash0 once it ends. One area per runtime, because a run is
one driver, one credential and one cost profile — `## Runtimes` in [../../setup.md](../../setup.md)
is the table, and [../codex](../codex/README.md) is the other half.

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
`test/e2e/` owns those against a mock. The Codex runtime *can* see what was sent, through the
plugin's debug log, which is one of the few things it can do that this one cannot.

**Anything about Codex.** The two runtimes share the pipeline but not the payloads, and this year's
defects were each invisible from the other side.
