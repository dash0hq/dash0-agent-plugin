---
id: mcp-call-strips-the-prefix-and-tags-a-placeholder-server
area: cursor/mcp
runtime: cursor
status: active
input: qa/tools/qa-session-cursor.sh with QA_CURSOR_MCP=1, one turn calling the stub server's echo_text
duration: ~60s
settling: 25s
cleanup: keep
covers:
  - internal/source/cursor/cursor.go
  - internal/pipeline/pipeline.go
  - DEVELOPMENT.md
---

## Given

The same setup as
[../session/single-turn-agrees-with-the-hooks-that-fed-it](../session/single-turn-agrees-with-the-hooks-that-fed-it.md),
plus `QA_CURSOR_MCP=1`, which builds `qa/mcp-fixture/` and registers it as `qa_fixture_alpha` in
`$PROJECT/.cursor/mcp.json`.

**A stub server, not a real one.** The fixture's tool names, arguments and results are fixed, so the
expectation is computable from the prompt before the session runs. The developer's real connectors
are production systems whose output is not reproducible, and the driver refuses to start when
`~/.cursor/mcp.json` registers any of them — Cursor has no `--strict-mcp-config` to scope them away.

**The rename is the whole claim.** Cursor reports the tool as `MCP:echo_text`, and
`internal/source/cursor.Normalize` strips the prefix and sets `mcp_server`. So the hook and the span
name the same call differently, and `qa-compare.py` has to strip the prefix on its side too or every
MCP call prints as two differing rows. That stripping is the harness agreeing with a documented rule;
this spec is what asserts the rule itself, against the raw name in the payload.

## When

```sh
QA_CURSOR_BINARY=working-tree QA_CURSOR_MCP=1 qa/tools/qa-session-cursor.sh \
  'Call the MCP tool echo_text with the text qa-mcp. Then reply with exactly the word done.' \
  spec-cursor-mcp
sleep 25
```

Shape, measured 2026-09-01 on `qa/runs/probe-cursor-mcp` against cursor-agent 2026.08.31-4057e58: 26
hook invocations and 12 spans in Dash0, of which one `execute_tool` is the MCP call. The model
grepped and read its way around the checkout first, which is what a prompt run inside a repository
invites; the count is noisy and the MCP row is not.

## Expectation

**One `execute_tool` span for the MCP call**, from the one `postToolUse` whose `tool_name` is
`MCP:echo_text`.

**`gen_ai.tool.name` is `echo_text`.** The `MCP:` prefix is gone.

**`dash0.gen_ai.tool.mcp_server` is the literal string `cursor`.** Not the server's key
`qa_fixture_alpha`, not the name the process reports in `serverInfo`, and not empty. It is a
placeholder: the specialized hooks that carry the real server name are dropped, so v1 has nothing
else to put there. `DEVELOPMENT.md` documents it as `MCP server name (placeholder cursor on Cursor)`.

**`gen_ai.tool.type` is `function`**, as on every other tool span. An MCP call is not a distinct type
in this contract.

**The non-MCP tool spans carry no `mcp_server` at all.** The attribute is set inside the `MCP:`
branch, so a `Shell` or `Read` span acquiring it would mean the branch is leaking across calls.

**The transcript does not corroborate the count, and this run is why.** It recorded the MCP call as
`GetDynamicTools` (4 blocks) and `CallDynamicTool` (1), internal plumbing that fires no hook, and it
recorded `Glob` and `Grep` separately where the hooks report both as `Grep`. 15 blocks against 11
hooks, every one of the four accounted for. So `qa-compare.py` prints the transcript's tool table and
never compares it.

## Oracle

- Channel one, Dash0: `qa/tools/qa-compare.py qa/runs/spec-cursor-mcp`, whose tool table lists
  `echo_text` with the hooks' count beside it, then `dash0 spans query` for the attributes.
- The hook record: the `postToolUse` payload in `record/events/`, which holds the raw
  `MCP:echo_text` and the `{"text": "qa-mcp"}` input. This is the pre-rename name the spec is
  written against.
- The attribute surface: `qa/tools/qa-attrs.py qa/runs/spec-cursor-mcp`, which confirms
  `dash0.gen_ai.tool.mcp_server` is in the contract rather than a surplus.
- `plugin-debug.log` holds the span as built, which is the quickest place to read both attributes.

## Then

- The recording holds exactly one `preToolUse` and one `postToolUse` whose `tool_name` is
  `MCP:echo_text`, sharing a `tool_use_id`.
- Dash0 holds exactly one `execute_tool` span with `gen_ai.tool.name = echo_text`.
- That span's `dash0.gen_ai.tool.mcp_server` is `cursor`.
- Its `gen_ai.tool.type` is `function`.
- No other span of the session carries `dash0.gen_ai.tool.mcp_server`.
- The span is parented on the turn's `chat` span.
- `qa-compare.py`'s tool table shows `echo_text 1 1` — the prefix stripped on both sides.
- `qa-compare.py` exits `0`, and `qa-attrs.py` exits `0`.

## Tolerance

**The tool count is noisy and not asserted.** 11 tool calls for a one-call prompt is what the
reference run produced. Only the MCP row is a fixed claim.

**The model may not call the tool.** A run whose recording holds no `MCP:` payload did not reach the
server — check that `$PROJECT/.cursor/mcp.json` exists in the run directory and re-run. It is not a
finding.

**The scratch project sits inside this checkout**, so a wandering model reads repository files. That
is noise rather than a hazard: the session is read-mostly and the driver's project is its own git
repository. It does mean the tool table is worth skimming before quoting a count.

**`mcp_server = cursor` is the expected value today and a defect tomorrow.** When the v2
cross-reference lands, this assertion inverts. It is written as an equality against the literal so
the change cannot pass unnoticed.
