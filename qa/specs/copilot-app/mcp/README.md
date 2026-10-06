# copilot-app/mcp

MCP calls in a Copilot app session. The app ships `github-mcp-server`, so a run needs no MCP setup.

| Invariant | Spec | Status |
| --- | --- | --- |
| An MCP call's span is named by the tool and carries the server | [mcp-call-names-the-server](mcp-call-names-the-server.md) | draft — passed 9/9 on 2026-10-06 |

## Deliberately not written

- **A failing MCP call.** No reliable way to make the built-in server fail is known.

