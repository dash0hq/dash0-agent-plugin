---
id: tool-failure-sets-the-span-status
area: opencode-v2/session
runtime: opencode-v2
status: active
input: qa/tools/qa-session-opencode-v2.sh, one turn whose only tool call reads a file that does not exist
duration: ~30s
settling: 25s
cleanup: keep
covers:
  - internal/source/opencodev2/opencodev2.go
  - internal/otlp/otlp.go
---

## Given

The driver's defaults, `omit_io` on. The input is a tool that fails at the tool level, reading a
missing file, rather than a shell command that exits non-zero: on Copilot those two are different
things, and on OpenCode the shell case has not been measured.

## When

```sh
qa/tools/qa-session-opencode-v2.sh \
  'Read the file does-not-exist.txt with the read tool. Do not retry or investigate. Then reply with exactly the word done.' \
  spec-opencode-tool-failure
sleep 25
```

## Expectation

- `session.tool.failed` closes the tool span with its failure flag set. The span's error text comes
  from `errorText`: under `omit_io` it is the V2 error **type**, never the message, because V2 error
  messages can carry whole arguments and the exporter copies this string into `status.message`,
  outside its redaction path.
- The turn itself succeeds, so its `chat` span carries no error status.

## Oracle

- `qa/tools/qa-compare.py qa/runs/spec-opencode-tool-failure` for the counts.
- `dash0 spans query` filtered to the session, reading `status` off each span.
- `plugin-debug.log` for the span's status as it was built.

## Then

- `qa-compare.py` exits `0`.
- The `execute_tool read` span has status code `2`, and its status message is a short error type, not
  a sentence naming the path.
- `does-not-exist.txt` appears in no status message and no attribute.
- The `chat` span's status code is `0`.

## Tolerance

**OpenCode may not treat a missing file as a failed tool.** If the read returns an error *as text*
through `session.tool.success`, the span is not an error and this spec is reporting OpenCode's
semantics, not a plugin defect. Check the export's tool part `state.status` before reporting.

**A retry breaks the count, not the claim.** The failing call must be the ERROR span; others must
not be.
