# Amp runs MCP tools inside `code_exec`, and the wrappers reach no span

**Status:** open. Not a regression in the plugin — an Amp change the arm had to
catch up with, plus one real gap it exposes.
**Found:** 2026-10-12, running the existing MCP spec against a current Amp.
**Area:** amp/mcp, amp/tools.

## What changed

The arm was written on amp `0.0.1789407412-g29cb5a` (2026-09-14), where a model
called an MCP tool directly and Amp's `--stream-json` named it:
`mcp__qa_fixture_alpha__echo_text`.

On amp `0.0.1791792883-gf1ab8d` (2026-10-12) the same prompt produces:

| channel | what it reports |
| --- | --- |
| `--stream-json` | `tool_search` ×1, `code_exec` ×2 |
| plugin hooks (recorder) | `mcp__qa_fixture_alpha__echo_text`, `mcp__qa_fixture_beta__always_fails` |
| Dash0 spans | `execute_tool echo_text`, `execute_tool always_fails` |

Amp now finds the tool with `tool_search` and calls it by running code inside
`code_exec`. The plugin hooks fire for the **inner** MCP call; the stream names
the **outer** wrapper. The tool list in the session's `system` event confirms
it — `code_exec` is present, the `mcp__…` tools are not.

## What still works, and it is most of it

Everything the MCP spec actually asserts holds on the current version, measured
on `qa/runs/spec-amp-mcp`:

```text
execute_tool always_fails   dash0.gen_ai.tool.mcp_server=qa_fixture_beta    ERROR
execute_tool echo_text      dash0.gen_ai.tool.mcp_server=qa_fixture_alpha   UNSET
```

The prefix is stripped, each call names its own server (not one value for
both), and the failing call sets `ERROR`. Usage reconciled exactly, 185 output
tokens on both channels.

## The two real consequences

**1. The second channel goes blind for MCP runs.** The stream and the spans now
name different layers, so comparing their tool names as one multiset reports a
mismatch on a perfectly correct run — which is what it did before this was
understood. `qa-amp-compare.py` now recognises `code_exec` and `tool_search` as
wrappers and marks the tool rows `UNPROVABLE` rather than `MISMATCH`, with the
reason printed. An MCP run can still prove span shape, server naming and
failure status; it can no longer prove *how many* tool calls there were from an
independent channel.

**2. `code_exec` and `tool_search` reach no span at all.** They fire no plugin
hook, so a turn that spent its time inside `code_exec` shows the inner calls
and nothing about the wrapper that ran them. This is the same class as
[amp-server-side-tools-produce-no-span](amp-server-side-tools-produce-no-span.md)
— a tool the user sees and the trace does not — and it is a bigger hole than
that one, because `code_exec` is on the path of every MCP call rather than a
handful of server-executed tools.

Nothing in the bridge can close either: both follow from which events Amp
fires. Worth raising with Amp as the plugin API's most load-bearing gap, and
worth saying in `amp/README.md` so nobody reads an MCP trace as a complete
account of the turn.
