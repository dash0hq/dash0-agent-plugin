---
id: mcp-call-names-the-server-and-strips-the-prefix
area: mcp
status: active
input: qa/mcp-fixture, via QA_MCP=1 on qa/tools/qa-session.sh
duration: ~15s
settling: 10s
cleanup: keep
covers:
  - internal/pipeline/pipeline.go
  - internal/otlp/trace.go
  - internal/otlp/otlp.go
  - qa/mcp-fixture/main.go
---

## Given

The plugin as installed, plus the recorder, plus two stub MCP servers.

`QA_MCP=1` builds `qa/mcp-fixture` and registers it twice, as `qa_fixture_alpha` and
`qa_fixture_beta`, through `--mcp-config` and `--strict-mcp-config`. Only `qa_fixture_alpha` matters
here; the second server is what [two-mcp-servers-in-one-session-are-not-conflated](two-mcp-servers-in-one-session-are-not-conflated.md)
needs, and one config serves both specs.

`--strict-mcp-config` is a safety property, not a convenience. Without it the session also loads the
developer's real connectors, and a QA prompt could reach Slack, Linear, or Drive. With it the only
MCP servers in the session are two local processes whose every answer is fixed by
`qa/mcp-fixture/main_test.go`.

## When

```sh
QA_MCP=1 QA_MODEL=haiku qa/tools/qa-session.sh \
  'Call the qa_fixture_alpha MCP tool echo_text with text "qa-mcp-probe". Then reply with exactly the word done.' \
  spec-mcp-call
sleep 10
```

Shape, measured on release 0.1.24 with `claude` 2.1.239: 9 hook invocations, 2 `PostToolUse`, 1
`Stop`, and 3 spans. Two `PostToolUse`, not one: MCP tools are deferred, so the model spends a
`ToolSearch` call loading the schema before it can call `echo_text`. That extra call is part of the
shape of every MCP session, and it is useful here rather than noise — it is a native tool in the same
turn, so a rule that mangles every tool name equally cannot pass.

## Expectation

From `qa/runs/spec-mcp-call/record/` alone.

**The hook payload carries the prefixed name.** `record/events/*PostToolUse*.json` holds
`tool_name: "mcp__qa_fixture_alpha__echo_text"`. This is the fact the whole spec rests on, and it is
a property of the host, not of the plugin: nothing in this repository decides what Claude Code puts
in that field. Measured, it is exactly that string.

**One span per `PostToolUse`, as for any tool.** Two payloads, so two `execute_tool` spans. The
mapping is in `internal/pipeline/pipeline.go` and the count comes from the hooks.

**The split of that name.** `DEVELOPMENT.md` states the contract: an MCP tool name is stripped of its
`mcp__<server>__` prefix, and the server goes to `dash0.gen_ai.tool.mcp_server`. Applying that
contract to the recorded raw name gives `echo_text` and `qa_fixture_alpha`. This clause is deductive
— it applies a documented rule to an independent input — in the same way the `bash.command_family`
clause is in [../session/tool-call-produces-one-span-per-post-tool-use](../session/tool-call-produces-one-span-per-post-tool-use.md).
It is worth asserting because the rule is what a consumer's per-server grouping depends on, and
because the input it is applied to is in the record.

**Nothing native is touched.** The `ToolSearch` payload's `tool_name` has no `mcp__` prefix, so its
span's name is that string unchanged and it carries no `mcp_server` attribute. A rule that strips too
eagerly fails here rather than in a tenant's dashboard.

**The call ids and the durations.** Each payload carries `tool_use_id` and `duration_ms`. The id set
on the spans equals the id set in the payloads, and each span's duration equals its payload's
`duration_ms`. Measured on `spec-mcp-call`: `ToolSearch` 1 ms, `echo_text` 2 ms. Those are that run's
numbers, not constants; only the equality is the assertion.

## Oracle

- Channel one, Dash0: `qa/tools/qa-compare.py qa/runs/spec-mcp-call`. Its "Tool spans" table is
  per-tool-name, span count against `PostToolUse` count. It applies `mcp_tool_name` to the hook side
  so the two sides are named alike; see the note in Tolerance.
- Channel one, per-span detail: `dash0 spans query` filtered to the session, reading
  `gen_ai.tool.name`, `dash0.gen_ai.tool.mcp_server`, `gen_ai.tool.call.id`, the span name, the
  timestamps, and `parentSpanId`.
- Attribute surface: `qa/tools/qa-attrs.py qa/runs/spec-mcp-call`, read for one question only —
  whether any MCP-specific key is outside the contract.

## Then

- `qa-compare.py` exits `0` and prints `All three records agree.`
- Dash0 holds 3 spans: 1 `chat`, 2 `execute_tool`, 0 `invoke_agent`.
- The MCP span's `gen_ai.tool.name` is `echo_text`, with no `mcp__` prefix and no server in it.
- The MCP span's name is `execute_tool echo_text`, so the span name is normalized too, not only the
  attribute.
- `dash0.gen_ai.tool.mcp_server` on that span is `qa_fixture_alpha`.
- The `ToolSearch` span's `gen_ai.tool.name` is `ToolSearch`, and it has no
  `dash0.gen_ai.tool.mcp_server` attribute at all.
- The set of `gen_ai.tool.call.id` across both tool spans equals the set of `tool_use_id` across both
  payloads, with no duplicate.
- Each tool span's duration equals its payload's `duration_ms`.
- Both tool spans are children of the `chat` span, which is a root span.
- `gen_ai.operation.name` is `execute_tool` and `gen_ai.tool.type` is `function` on both.
- `gen_ai.tool.call.arguments` and `gen_ai.tool.call.result` are present on both.
- Every span's status code is unset.
- `qa-attrs.py` does not list `dash0.gen_ai.tool.mcp_server`, or any other MCP-specific key, as
  outside the contract.

## Tolerance

**`qa-attrs.py` exits `1` on release 0.1.24, and that is not this spec's finding.** It reports
`prompt_id`, `session_crons`, and `background_tasks`, which
[../session/span-carries-no-undeclared-attribute](../session/span-carries-no-undeclared-attribute.md)
already owns and which are fixed in the working tree. Read the tool's list, not its exit code, and
only for MCP keys. If `mcp_server` ever appears in it, that is this spec's finding.

**The hook side of the tool table is normalized by the harness.** `qa-compare.py`'s `mcp_tool_name`
mirrors `NormalizeMCPToolName`. Without it every MCP call printed two rows, both flagged, and the
tool exited `1` on a healthy run. So the tool table cannot itself detect a wrong split — it is there
to keep the counts comparable. The split is asserted from the raw payload name in the per-span
detail, which is the assertion that matters.

**Content is not assertable.** The API returns `gen_ai.tool.call.arguments` and
`gen_ai.tool.call.result` as `<REDACTED>`, so the fixture's exact echo text cannot be checked here.
The fixture's own test pins that value; `test/e2e/` owns the wire.

**Which tools the model picks is the model's choice.** A run that spends two `ToolSearch` calls, or
calls `echo_text` twice, is not a product finding. Every assertion compares the spans against that
run's own payloads, so a differently shaped run still either agrees or does not.

**Timing.** Span start is derived by subtracting an integer `duration_ms`. Compare to the
millisecond. A 0 ms `duration_ms` is real for a local stub and is not a missing value.

**Ingest lag.** A few seconds; `qa-compare.py` widens the window at both ends. Zero spans right after
a session is lag until a re-run says otherwise.
