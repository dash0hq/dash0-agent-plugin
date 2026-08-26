---
id: two-mcp-servers-in-one-session-are-not-conflated
area: claude/mcp
runtime: claude
status: active
input: qa/mcp-fixture, via QA_MCP=1 on qa/tools/qa-session.sh
duration: ~15s
settling: 10s
cleanup: keep
covers:
  - internal/pipeline/pipeline.go
  - internal/otlp/otlp.go
  - qa/mcp-fixture/main.go
---

## Given

The same two stub servers as
[mcp-call-names-the-server-and-strips-the-prefix](mcp-call-names-the-server-and-strips-the-prefix.md),
and this time both are called.

The two servers expose the *same* tool name, `echo_text`, from the same binary. That is the point.
Two spans that differ only in `dash0.gen_ai.tool.mcp_server` cannot be told apart by any other
attribute, by the span name, or by the tool name, so nothing but the attribute under test can carry
the distinction.

## When

```sh
QA_MCP=1 QA_MODEL=haiku qa/tools/qa-session.sh \
  'Call the qa_fixture_alpha MCP tool echo_text with text "alpha-probe". Then call the qa_fixture_beta MCP tool echo_text with text "beta-probe". Then reply with exactly the word done.' \
  spec-mcp-two-servers
sleep 10
```

Shape, measured on release 0.1.24 with `claude` 2.1.239: 11 hook invocations, 3 `PostToolUse`, 1
`Stop`, and 4 spans. One `ToolSearch`, which loaded both tools in a single call, then one call per
server. All in one turn, so one `chat` span.

## Expectation

From `qa/runs/spec-mcp-two-servers/record/` alone.

**Two payloads, two servers.** `record/events/*PostToolUse*.json` holds
`mcp__qa_fixture_alpha__echo_text` and `mcp__qa_fixture_beta__echo_text`, each with its own
`tool_use_id`. The two server names come out of those two strings, and out of nothing else.

**Three spans from three payloads**, one of them the `ToolSearch` that neither server owns.

**The attribute is per call.** Applying the `DEVELOPMENT.md` split to each payload independently
gives `qa_fixture_alpha` for the first and `qa_fixture_beta` for the second. The failure this spec
exists for is a server name resolved once and reused: a cache keyed on the session, or on the turn,
or a last-writer-wins field on shared state. Every such implementation passes
[mcp-call-names-the-server-and-strips-the-prefix](mcp-call-names-the-server-and-strips-the-prefix.md),
because that spec's session calls one server. This one fails it, and the direction of the smear says
which: two `alpha` spans means the first call won, two `beta` means the last did.

**The pairing, not just the set.** The check is per span, matched through `gen_ai.tool.call.id`. Two
spans carrying the right *set* of server names, swapped between the two calls, is still wrong, and a
set comparison would call it correct.

## Oracle

- Channel one, Dash0: `qa/tools/qa-compare.py qa/runs/spec-mcp-two-servers` for the counts. Note that
  its tool table shows `echo_text 2`, one row for both servers — it does not split by server, which
  is why the assertions below are read per span.
- Channel one, per-span detail: `dash0 spans query` filtered to the session, reading
  `gen_ai.tool.call.id` and `dash0.gen_ai.tool.mcp_server` off each `execute_tool` span.

## Then

- `qa-compare.py` exits `0` and prints `All three records agree.`
- Dash0 holds 4 spans: 1 `chat`, 3 `execute_tool`, 0 `invoke_agent`.
- Exactly two `execute_tool` spans have `gen_ai.tool.name` = `echo_text`, both named
  `execute_tool echo_text`.
- Their two `dash0.gen_ai.tool.mcp_server` values are `qa_fixture_alpha` and `qa_fixture_beta`, one
  each. Neither value appears twice.
- Matched by `gen_ai.tool.call.id`, each span's server equals the server in the prefix of its own
  payload's `tool_name`. Assert per span, not as a set.
- The `ToolSearch` span has no `dash0.gen_ai.tool.mcp_server` attribute, so the attribute did not
  leak onto a native tool call in a session where MCP servers were in play.
- All three tool spans are children of the same `chat` span.
- Every span's status code is unset.

## Tolerance

**The model may call the two servers in either order, or in two turns.** Neither is a finding. The
assertions match each span to its own payload by call id, so the order does not enter into them. Two
turns would add a second `chat` span and change only the parent assertion, which then reads: each
tool span is a child of the `chat` span for the turn that called it.

**One `ToolSearch` or two.** Measured, one call loaded both tools. A run that searches twice has four
`execute_tool` spans and still satisfies every assertion above.

**A run where the model calls only one server proves nothing and is not a failure.** Discard it and
re-run; there is nothing to compare.

**Ingest lag.** As in
[mcp-call-names-the-server-and-strips-the-prefix](mcp-call-names-the-server-and-strips-the-prefix.md).
