---
id: client-side-tool-names-are-copied-verbatim
area: amp/tools
runtime: amp
status: active
input: qa/tools/qa-session-amp.sh, one local turn that uses more than one kind of tool
duration: ~40s
settling: 30s
cleanup: keep
covers:
  - amp/index.ts
  - internal/source/amp/amp.go
  - internal/pipeline/pipeline.go
---

## Given

The same install and configuration as
[../session/turn-produces-one-chat-root-and-one-span-per-tool.md](../session/turn-produces-one-chat-root-and-one-span-per-tool.md).

The existing tool specs cover `shell_command` and the server-executed tools
that reach no span at all. Real usage is wider than that: across 14 days of
dogfooding, 18,106 tool spans were named

| tool | spans |
| --- | --- |
| `shell_command` | 12,665 |
| `shell_command_status` | 2,090 |
| `apply_patch` | 1,866 |
| `skill` | 1,387 |
| `shell_command_kill` | 74 |
| `edit_file` | 13 |
| `reload_skills` | 7 |
| `upload_thread_file`, `download_thread_file`, `reload_mcp` | 4 |

Every one of those names came through the plugin untouched. This spec pins
that: Amp's tool name is what lands on the span, with no mapping table, no
normalization, and nothing invented for a tool the plugin has never seen.

## When

```sh
qa/tools/qa-session-amp.sh \
  'Create a new file at qa/runs/apply-patch-probe.txt whose only content is the line qa-amp-patch. Then reply with exactly the word done.' \
  spec-amp-tool-names
sleep 30
qa/tools/qa-amp-compare.py qa/runs/spec-amp-tool-names
```

The prompt asks for a file rather than naming a tool, because which tool Amp
reaches for is Amp's decision. What the spec asserts is agreement between the
two channels, not a particular tool.

## Then

- The `execute_tool` span names in Dash0 equal the tool names in Amp's
  `--stream-json`, as a multiset: same names, same counts.
- A tool the plugin has no special handling for still gets a span under its own
  name.

Measured 2026-10-12 on `qa/runs/spec-amp-patch`: the turn used four tools —
`shell_command` ×3 and `apply_patch` ×1 — and Dash0 held exactly
`{'shell_command': 3, 'apply_patch': 1}`. `AGREEMENT`.

## Sharp edges

**Tool choice is not deterministic.** The same prompt can be answered with one
`apply_patch` or with a shell heredoc, and both are correct runs. Assert the
agreement between channels; never assert that a named tool appeared. A run
where the model chose differently is a different run, not a failure.

**`shell_command_status` and `shell_command_kill` are their own tools.** They
are how Amp polls and stops a backgrounded command, and they produce their own
spans — 2,164 of them in the dogfood sample. A spec that expects one span per
shell command will read those as duplicates.
