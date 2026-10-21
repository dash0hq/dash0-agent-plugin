---
id: server-side-tools-reach-no-span
area: amp/tools
runtime: amp
status: failing
input: qa/tools/qa-session-amp.sh, one turn using a client-side and a server-side tool
duration: ~40s
settling: 30s
cleanup: keep
covers:
  - amp/index.ts
---

## Given

Amp runs some built-in tools on the client and some on the server. Only the
client-side ones fire the `tool.call` / `tool.result` events a plugin can bind
to, so the bridge is never told about the rest.

This spec asserts the behaviour that *should* hold — every tool Amp's own
`--stream-json` reports gets a span — and records that it does not, so a future
Amp release that closes the gap is noticed rather than assumed.

The QA recorder is what makes this attributable rather than merely visible.
Without it, a missing span is indistinguishable from the bridge dropping a
tool; with it, the bridge is demonstrably never told.

## When

One turn using both kinds, so the comparison is within a single session and
cannot be blamed on run-to-run variation:

```sh
QA_AMP_USAGE=1 qa/tools/qa-session-amp.sh \
  'Do exactly two things and nothing else. First, run the shell command: echo qa-mixed. Second, use web_search to search for: OpenTelemetry semantic conventions gen_ai. Then reply with exactly the word done.' \
  spec-amp-server-tools
# 30s, not 10s: with usage export on the helper is detached and polls
# `amp threads export` for up to twenty seconds, so spans land after `amp` exits.
sleep 30
qa/tools/qa-amp-compare.py qa/runs/spec-amp-server-tools
```

## Then

`qa-amp-compare.py` exits `0`: two `tool_use` blocks in the stream, two
`execute_tool` spans.

## Status: failing

Measured on `qa/runs/amp-mixed-tools`:

| Channel | `shell_command` | `web_search` |
| --- | --- | --- |
| Amp's `--stream-json` | `tool_use` | `tool_use` |
| QA recorder (the bridge's input) | `tool.call` + `tool.result` | **nothing** |
| Spans | `execute_tool shell_command` | **nothing** |

And on `qa/runs/amp-toolerror-b`, a turn whose only tool was `read_web_page`:
the recorder captured exactly two events, `agent.start` and `agent.end`, and
`qa-amp-compare.py` exits `1` with `stream=1 dash0=0`.

See [amp-server-side-tools-produce-no-span](../../../findings/amp-server-side-tools-produce-no-span.md).

## Not asserted here

**Which tools are on which side.** Confirmed client-side: `shell_command`.
Confirmed server-side: `read_web_page`, `web_search`. The other 12 in
`amp tools list` are unprobed, and the split is Amp's to define and can move,
so the lists are evidence rather than a contract.

**That this is a bridge defect.** It is not. There is nothing to fix in
`amp/index.ts`; the decision is what to document.
