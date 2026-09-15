# Amp: server-executed tools produce no span

**Runtime:** amp
**Found:** 2026-09-15, by `qa/runs/amp-toolerror-b` and `qa/runs/amp-mixed-tools`
**Status:** open
**Affects:** `amp/index.ts` (nothing to fix there), Amp's plugin API surface

## What happens

Amp runs some of its built-in tools on the client and some on the server. Only
the client-side ones reach a plugin: the `tool.call` and `tool.result` events
are never fired for a server-executed tool. The bridge therefore emits no
`execute_tool` span for it, and the work is invisible in the trace even though
the turn root is correct and the tool plainly ran.

This is not a bug in the bridge. It is a limit of what a plugin can observe,
and it belongs in the docs rather than in the code.

## Measured

`qa/runs/amp-mixed-tools` — one turn, told to do exactly two things:

| Channel | `shell_command` | `web_search` |
| --- | --- | --- |
| Amp's own `--stream-json` | `tool_use` present | `tool_use` present |
| QA recorder (the plugin's input) | `tool.call` + `tool.result` | **nothing** |
| Spans in the amp dataset | `execute_tool shell_command` | **nothing** |

`qa/runs/amp-toolerror-b` — one turn, a single `read_web_page` call against an
unresolvable host. Amp's stream carries the `tool_use` and the failing
`tool_result` (`Could not read the page: http_error, HTTP 503`). The recorder
captured exactly two events, `agent.start` and `agent.end`. `qa-amp-compare.py`
exits 1 with `stream=1 dash0=0`.

The recorder is what makes this attributable. Without it a missing span looks
like the bridge dropping a tool; with it, the bridge is demonstrably never told.

## Which tools

Confirmed client-side, spans produced: `shell_command`.
Confirmed server-side, no span: `read_web_page`, `web_search`.

The remaining 12 tools in `amp tools list` have not been probed. The split is
Amp's to define and it can move, so the list above is evidence, not a contract.

## What to decide

1. Say so in `amp/README.md` and `FEATURE_MATRIX.md`: Amp tool coverage is
   client-side tools only, and a turn's `execute_tool` spans are not a complete
   record of the tools it used. This is the honest, zero-code option.
2. Ask Amp for tool events covering server-executed tools, and keep the gap
   documented until they exist.

Nothing should try to reconstruct the missing calls from
`amp threads export`: that is the same read the usage path already depends on,
with the same timing problem — see
[amp-answering-model-call-is-never-attributed](amp-answering-model-call-is-never-attributed.md).

## Also learned here

Amp does **not** classify a nonzero shell exit as a tool failure. A
`shell_command` that exits 127 arrives as `status: "done"` with the exit code
buried in the tool-shaped `output` payload, so the span status is correctly
`UNSET`. The `tool-failure-sets-the-span-status` approach used for claude and
cursor cannot be ported to amp by failing a shell command — see
[../specs/amp/session/tool-status-is-copied-from-amp-not-inferred.md](../specs/amp/session/tool-status-is-copied-from-amp-not-inferred.md).
