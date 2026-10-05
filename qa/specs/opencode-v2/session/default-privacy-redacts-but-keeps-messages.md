---
id: default-privacy-redacts-but-keeps-messages
area: opencode-v2/session
runtime: opencode-v2
status: active
input: qa/tools/qa-session-opencode-v2.sh, one turn with a distinctive prompt and one file read
duration: ~30s
settling: 25s
cleanup: keep
covers:
  - internal/source/opencodev2/opencodev2.go
  - internal/otlp/otlp.go
---

## Given

The driver's defaults, so `omit_io` is `true`, the product's own default.

**Why redacted rather than absent.** The shared exporter writes redacted message attributes only for
fields that are present, so an adapter that drops the prompt and reply under `omit_io` emits none.
The conversation view separates turns on `gen_ai.input.messages` and `gen_ai.output.messages`, and
without them every tool call of a session merges into one block. The other adapters emit
`<REDACTED>` placeholders; this one must too.

## When

```sh
qa/tools/qa-session-opencode-v2.sh \
  'QA-PRIVACY-MARKER-7341: read README.md with the read tool, then reply with exactly the word done.' \
  spec-opencode-default-privacy
sleep 25
```

## Expectation

- Under `omit_io`, `session.inbox.delivered` stores `<REDACTED>` as the turn's prompt and
  `session.text.ended` stores `<REDACTED>` as its reply. The exporter turns both into one-message
  arrays: `[{"role":"user","parts":[{"type":"text","content":"<REDACTED>"}]}]` and the same with
  `assistant`.
- `session.tool.called` keeps no input, so `gen_ai.tool.call.arguments` and
  `gen_ai.tool.call.result` are `<REDACTED>`.
- The prompt's text, including the marker, appears on no span.

## Oracle

- `qa/tools/qa-compare.py qa/runs/spec-opencode-default-privacy`. It fails a `chat` span with no
  message attributes, or with unredacted ones under `omit_io`, and it fails if the manifest's prompt
  text appears on any span attribute.
- `dash0 spans query` filtered to the session, for the tool arguments and result.

## Then

- `qa-compare.py` exits `0`.
- The `chat` span's `gen_ai.input.messages` is the redacted user array and its
  `gen_ai.output.messages` the redacted assistant array.
- Every `execute_tool` span has `gen_ai.tool.call.arguments` and `gen_ai.tool.call.result` equal to
  `<REDACTED>`.
- `QA-PRIVACY-MARKER-7341` appears on no span.

## Tolerance

**JSON key order and escaping are the exporter's.** Go escapes `<` and `>` as `<` and
`>`. Compare the parsed value, not the string.
