---
id: mcp-tool-failure-sets-the-span-status
area: claude/mcp
runtime: claude
status: active
input: qa/mcp-fixture, via QA_MCP=1 on qa/tools/qa-session.sh
duration: ~15s
settling: 10s
cleanup: keep
covers:
  - internal/pipeline/pipeline.go
  - internal/otlp/trace.go
  - claude/hooks.json
  - qa/mcp-fixture/main.go
---

## Given

The same two stub servers, and the fixture's second tool. `always_fails` answers every `tools/call`
with a JSON-RPC error, code `-32000`, message `qa-fixture: always_fails failed on purpose`. Its
result is pinned by `qa/mcp-fixture/main_test.go`, so the error text is known before the session
runs.

A protocol-level error rather than an `isError` result, deliberately. The two are different failures
and a host may report them differently; this spec covers the unambiguous one. What an `isError`
result does is a separate question and would need its own tool and its own spec.

This is the only input in the QA harness that fails on demand. The `session` coverage map has carried
"a failed turn or a failed tool call sets the span status" as unwritten because it "needs a prompt
that reliably fails". A stub that always errors is that prompt for the tool half.

## When

```sh
QA_MCP=1 QA_MODEL=haiku qa/tools/qa-session.sh \
  'Call the qa_fixture_alpha MCP tool always_fails once. It is expected to fail. Do not retry it. Then reply with exactly the word done.' \
  spec-mcp-failure
sleep 10
```

Shape, measured on release 0.1.24 with `claude` 2.1.239: 9 hook invocations, 1 `PostToolUse` (the
`ToolSearch`), 1 `PostToolUseFailure`, 1 `Stop`, and 3 spans. The session itself succeeds — the model
reports the failure and finishes — so `claude` exits `0` and the `chat` span is not in error. Only
the tool call failed.

## Expectation

From `qa/runs/spec-mcp-failure/record/` alone.

**The host reports the failure as its own hook.** The recorded event name for the `always_fails` call
is `PostToolUseFailure`, and its payload carries
`error: "MCP error -32000: qa-fixture: always_fails failed on purpose"` and no `tool_response`. That
the host distinguishes a failed MCP call at all is a fact about Claude Code, established by the
record rather than assumed. Were it to arrive as a plain `PostToolUse`, this spec fails, and it fails
for the right reason: a broken MCP server would then be indistinguishable from a healthy one in
Dash0, which is precisely the production failure the spec exists to prevent.

**One span either way.** `internal/pipeline/pipeline.go` maps `PostToolUse` and `PostToolUseFailure`
to one `execute_tool` each, so two payloads expect two tool spans. A failed call must not be dropped
and must not be counted twice.

**The status follows the hook.** `DEVELOPMENT.md` states `Error` with `exception.message` on
`PostToolUseFailure`, `Unset` otherwise. Applied to this record: the `always_fails` span is in error
and carries `exception.message`, the `ToolSearch` span is unset and does not.

**The failed call is still fully identified.** The payload carries `tool_name`, `tool_use_id`, and
`duration_ms`, so the failed span carries the same normalized name, the same call id, the same
duration, and the same `dash0.gen_ai.tool.mcp_server` as a successful one would. An error path that
loses the server attribute makes "which server fails most" unanswerable, which is the question a
failure span is for. Measured on `spec-mcp-failure`: `always_fails`, `qa_fixture_alpha`, 5 ms. The
duration is whatever that run's payload says; only the equality is the assertion.

## Oracle

- Channel one, Dash0: `qa/tools/qa-compare.py qa/runs/spec-mcp-failure` for the counts. It folds
  `PostToolUseFailure` into the same `execute_tool` expectation, so a dropped failure span shows as a
  count difference.
- Channel one, per-span detail: `dash0 spans query` filtered to the session, reading `status`,
  `exception.message`, `gen_ai.tool.name`, `dash0.gen_ai.tool.mcp_server`, and `gen_ai.tool.call.id`.
- Channel two, `claude-result.json`: `is_error` is `false`. A failed tool call inside a session that
  the model recovers from is not a failed session, and the two channels must disagree about that
  on purpose.

## Then

- The recorded event for the `always_fails` call is `PostToolUseFailure`, not `PostToolUse`.
- `qa-compare.py` exits `0` and prints `All three records agree.`
- Dash0 holds 3 spans: 1 `chat`, 2 `execute_tool`, 0 `invoke_agent`.
- The failed span's status code is `2` (`Error`). Measured on the API as
  `{'code': 2, 'message': '<REDACTED>'}`.
- The failed span carries `exception.message`.
- The failed span's `gen_ai.tool.name` is `always_fails` and its name is `execute_tool always_fails`,
  so the MCP prefix is stripped on the error path too.
- Its `dash0.gen_ai.tool.mcp_server` is `qa_fixture_alpha`.
- Its `gen_ai.tool.call.id` equals the payload's `tool_use_id`, and its duration equals the payload's
  `duration_ms`.
- It carries `gen_ai.tool.call.arguments` but no `gen_ai.tool.call.result`. There was no result.
- The `ToolSearch` span's status code is unset and it carries no `exception.message`.
- The `chat` span's status code is unset, and `claude-result.json` reports `is_error: false`.
- Both tool spans are children of the `chat` span.

## Tolerance

**The error text is not assertable through this oracle.** The API returns `exception.message` and the
status message as `<REDACTED>`, exactly as it does for the tool arguments. Presence is checkable,
value is not. That the text is right is a unit-test question; `test/e2e/` sees the bytes.

**Ingest adds error-fingerprint attributes to a span in error.** `dash0.error.fingerprint.id`,
`.rule.id`, `.rule.version`, and `.source` appear on the failed span and are not sent by the plugin.
`qa-attrs.py` lists them under "added at ingest". They are not a finding, and their presence is
incidental confirmation that Dash0 stored the span as an error.

**A retry is not a finding.** The prompt asks for one call, but a model that calls `always_fails`
twice produces two `PostToolUseFailure` payloads and two error spans. Every assertion above compares
spans to that run's own payloads, so a retry still either agrees or does not. Only a run with zero
`PostToolUseFailure` is unusable: re-run it.

**A session where the model gives up and errors out.** Then `claude-result.json` has
`is_error: true`, the `Stop` may arrive as `StopFailure`, and the `chat` span is in error too. The
tool-span assertions are unaffected. Re-run for the clean shape before reading anything into the
`chat` span.

**Ingest lag.** As in
[mcp-call-names-the-server-and-strips-the-prefix](mcp-call-names-the-server-and-strips-the-prefix.md).
