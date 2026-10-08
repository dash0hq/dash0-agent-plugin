---
id: mcp-call-names-the-server
area: copilot-app/mcp
runtime: copilot-app
status: draft
input: one call to the app's built-in github-mcp-server
duration: ~15s
settling: 25s
cleanup: keep
covers:
  - copilot-app/extension.mjs
  - internal/source/copilotapp/copilotapp.go
  - internal/source/copilot/emit.go
---

## Given

The runner procedure in [../README.md](../README.md), with a real model. No MCP setup is needed: the app
ships `github-mcp-server` and runs it under the app's own GitHub login.

Measured 2026-10-06 in this machine's app history: an MCP call is a `tool.execution_start` with
`toolName` `github-mcp-server-get_file_contents`, `mcpServerName` `github-mcp-server` and
`mcpToolName` `get_file_contents`. The `toolName` is a single flattened string, so a plugin that
forgot the two MCP fields would export a span that looks like an ordinary tool, with nothing to say
it came from a server.

## When

Send:

```text
Use the GitHub MCP server to read the first line of README.md in the public repository
dash0hq/dash0-agent-plugin, and reply with that line only. Use no other tool.
```

Wait for the session to go idle, then wait out the settling time.

## Expectation

**From `events.jsonl`, independently:** every main-agent `tool.execution_start` that has an
`mcpServerName`. For each one, the expected span name is `execute_tool ` + `mcpToolName`, the
expected `gen_ai.tool.name` is `mcpToolName`, and the expected `dash0.gen_ai.tool.mcp_server` is
`mcpServerName`.

## Oracle

- Dash0: `dash0-spans.json`.
- The event log: `events.jsonl` in the run directory.

## Then

- Every MCP call in the event log is exactly one `execute_tool` span, matched on
  `gen_ai.tool.call.id` = `toolCallId`.
- Its `gen_ai.tool.name` is `mcpToolName`, without the server's prefix.
- Its `dash0.gen_ai.tool.mcp_server` is `mcpServerName`.
- No span carries the flattened `toolName` (`github-mcp-server-…`) as its name or tool name.
- No non-MCP tool span carries `dash0.gen_ai.tool.mcp_server`.

## Tolerance

**The model may not use the MCP server.** It may fetch the file another way. With no
`mcpServerName` in the event log the run is inconclusive. Re-run and name the tool.

**The tool name belongs to the app.** Assert against the event log, never against
`get_file_contents` literally.

**Ingest lag: 25 seconds, and up to five minutes in this runtime.** The debug log is complete at once. When Dash0 holds fewer spans than it, re-query for up to five minutes before calling one missing.
