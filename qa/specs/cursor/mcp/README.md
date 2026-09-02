# cursor / mcp

An MCP call from a Cursor session. Needs `QA_CURSOR_MCP=1`, which registers the stub server from
`qa/mcp-fixture/` at project scope.

| Spec | Asserts |
| --- | --- |
| [mcp-call-strips-the-prefix-and-tags-a-placeholder-server](mcp-call-strips-the-prefix-and-tags-a-placeholder-server.md) | `MCP:echo_text` becomes `gen_ai.tool.name = echo_text` with `dash0.gen_ai.tool.mcp_server = cursor` |

## One server, not two

The claude driver registers two stub servers under two config keys, because Claude Code derives the
`mcp__<server>__<tool>` name from the key and two keys are what make "the server attribute is per
call, not per session" answerable.

Cursor cannot answer that question. It exposes an MCP call through the generic
`preToolUse`/`postToolUse` pair as `MCP:<tool>` and drops the specialized `beforeMCPExecution` and
`afterMCPExecution` hooks that carry the server name, so `internal/source/cursor` sets
`mcp_server` to the literal string `cursor`. A second server would produce spans identical to the
first's. So the fixture registers one, and the spec asserts the placeholder rather than an identity
it cannot see.

## The hazard this area carries

**Cursor has no `--strict-mcp-config`.** A user-scope `~/.cursor/mcp.json` loads alongside the
project file, and this runtime uses the developer's real `HOME`, so a QA prompt could reach a
production connector. `qa-session-cursor.sh` refuses to start when that file registers any server.
The claude driver solves the same problem with a flag; here the only safe answer was a refusal.

## Deliberately not written

**"A failed MCP call sets the span status."** The fixture's `always_fails` tool exists for it, and it
would assert nothing the shell-level failure path does not already assert — see
[../session/tool-failure-sets-the-span-status](../session/tool-failure-sets-the-span-status.md).
Worth adding if Cursor ever routes an MCP error differently from a tool error.

**"The MCP server name is correct."** It is a placeholder by design. `internal/source/cursor`
documents the v2 plan: cross-reference by tool name and `generation_id` within the session scratch
directory. Write the spec when that lands.
