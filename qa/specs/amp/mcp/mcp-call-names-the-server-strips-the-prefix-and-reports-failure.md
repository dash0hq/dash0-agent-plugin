---
id: mcp-call-names-the-server-strips-the-prefix-and-reports-failure
area: amp/mcp
runtime: amp
status: active
input: qa/mcp-fixture, via QA_AMP_MCP=1 on qa/tools/qa-session-amp.sh
duration: ~40s
settling: 30s
cleanup: keep
covers:
  - amp/index.ts
  - internal/source/amp/amp.go
  - internal/pipeline/pipeline.go
  - qa/mcp-fixture/main.go
---

## Given

`QA_AMP_MCP=1` builds `qa/mcp-fixture` and registers it twice, as
`qa_fixture_alpha` and `qa_fixture_beta` — the same two names the claude and
cursor MCP specs use, so a result here is comparable with theirs. The fixture's
two tools are pinned by `qa/mcp-fixture/main_test.go`: `echo_text` returns
`qa-fixture <server>: <text>` and always succeeds; `always_fails` returns JSON-RPC
error `-32000`.

Three amp-specific details, each of which cost a failed run to learn:

- **`--mcp-config` takes the server map flat.** Not Claude Code's
  `{"mcpServers": {...}}`, and not amp's own `"amp.mcpServers"` settings key.
  Either wrapper is rejected with `Invalid MCP server configuration`.
- **Amp has no `--strict-mcp-config`.** `--mcp-config` *merges* into the
  settings file, so isolation comes from `--settings-file` instead: the driver
  writes a QA-owned settings file declaring `"amp.mcpServers": {}`, and the
  run's only servers are QA's. Without it a QA prompt could reach the
  developer's real connectors.
- **This machine has no `~/.config/amp/settings.json` at all**, so the QA
  settings file adds nothing it displaces. On a machine that has one, check
  what the substitution costs before trusting a run.

## When

```sh
QA_AMP_MCP=1 QA_AMP_USAGE=1 qa/tools/qa-session-amp.sh \
  'Call the tool mcp__qa_fixture_alpha__echo_text once with the text QA-MCP-ALPHA. Then call the tool mcp__qa_fixture_beta__always_fails once; it will fail, which is expected, so do not retry it and do not try an alternative. Then reply with exactly the word done.' \
  spec-amp-mcp
# 30s, not 10s: with usage export on the helper is detached and polls
# `amp threads export` for up to twenty seconds, so spans land after `amp` exits.
sleep 30
qa/tools/qa-amp-compare.py qa/runs/spec-amp-mcp
```

Naming the tools in full (`mcp__<server>__<tool>`) is deliberate. Asking for
"the alpha echo tool" gets the model reaching for `shell_command` often enough
to waste runs.

## Then

`qa-amp-compare.py` prints `AGREEMENT`, and on the two `execute_tool` spans:

- **the prefix is stripped.** Span names are `execute_tool echo_text` and
  `execute_tool always_fails`; `gen_ai.tool.name` matches. `mcp__…` appearing
  anywhere is the failure.
- **the server is named, per call.** `dash0.gen_ai.tool.mcp_server` is
  `qa_fixture_alpha` on the first and `qa_fixture_beta` on the second. One
  value on both spans would mean the two servers were conflated — the thing the
  two-server setup exists to catch.
- **the failure sets the span status.** `always_fails` has status `ERROR`;
  `echo_text` does not.

Measured on `qa/runs/amp-mcp-b`, all four hold.

## Why this is the spec that proves the error path

Amp does **not** treat a nonzero shell exit as a tool failure — see
[../session/tool-status-is-copied-from-amp-not-inferred](../session/tool-status-is-copied-from-amp-not-inferred.md)
— and the built-in tools Amp does fail visibly are executed server-side and
fire no plugin events at all. An MCP tool is the one thing on Amp that is both
client-side and able to fail on demand, so this is the only spec here that
exercises the `tool.Status != "done"` branch in `internal/source/amp/amp.go`
against a real session.

## Not asserted here

**The echoed text.** `omit_io=true` in the driver, so
`gen_ai.tool.call.arguments` and `gen_ai.tool.call.result` are `<REDACTED>`.
That the fixture echoed correctly is `qa/mcp-fixture/main_test.go`'s job.

**Tokens**, for the usual reason —
[amp-answering-model-call-is-never-attributed](../../../findings/amp-answering-model-call-is-never-attributed.md).
