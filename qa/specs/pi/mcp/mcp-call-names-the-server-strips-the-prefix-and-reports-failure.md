---
id: mcp-call-names-the-server-strips-the-prefix-and-reports-failure
area: pi/mcp
runtime: pi
status: active
input: qa/tools/qa-session-pi.sh with QA_PI_MCP=1
duration: ~20s
settling: none; qa-pi-compare.py polls
cleanup: keep
covers:
  - pi/index.ts
  - internal/source/pi/pi.go
  - internal/pipeline/pipeline.go
---

## Given

The Pi extension and helper **from this working tree**, loaded by
`qa/tools/qa-session-pi.sh` with `--no-extensions -e pi/index.ts` and a helper
built into the run directory. No installed copy is read. The plugin exports to
`piDataset` in `qa/config.local.json` through `PI_PLUGIN_OPTION_*`. See
`### Pi` under `## Runtimes` in [setup.md](../../../setup.md).

## When

```sh
QA_PI_MCP=1 qa/tools/qa-session-pi.sh \
  'Call the tool mcp__qa_fixture_alpha__echo_text once with the text QA-MCP-ALPHA. Then call the tool mcp__qa_fixture_beta__always_fails once; it will fail, which is expected, so do not retry it and do not try an alternative. Then reply with exactly the word done.' \
  spec-pi-mcp
qa/tools/qa-pi-compare.py qa/runs/spec-pi-mcp
```

## Expectation

From `stream.jsonl`: two `tool_execution_end` records,
`mcp__qa_fixture_alpha__echo_text` with `isError: false` and
`mcp__qa_fixture_beta__always_fails` with `isError: true`.

## Oracle

- Channel one: Dash0, `dash0 spans query` filtered by `gen_ai.conversation.id`
  in `piDataset`, through `qa-pi-compare.py`.
- Channel two: Pi's own `--mode json` stream, `stream.jsonl` in the run.

## Then

- Span names are `execute_tool echo_text` and `execute_tool always_fails`:
  the `mcp__<server>__` prefix is stripped.
- `dash0.gen_ai.tool.mcp_server` is `qa_fixture_alpha` on the first and
  `qa_fixture_beta` on the second.
- `always_fails` has status `ERROR` and `exception.message`. `echo_text` has
  neither.

## Tolerance

Exact. If the stream shows no `mcp__` tool at all, the servers did not
connect, and the run failed. It did not pass. Make sure that the driver loads
`builtin:mcp`, which `--no-extensions` otherwise disables.

## Verified

2026-11-13 on `qa/runs/setup-probe-pi-mcp`: both names stripped, both servers named, `ERROR` on `always_fails` only, `AGREEMENT`. The first attempt reached no MCP tool because `--no-extensions` disabled Pi's built-in MCP support. The driver now loads it.
