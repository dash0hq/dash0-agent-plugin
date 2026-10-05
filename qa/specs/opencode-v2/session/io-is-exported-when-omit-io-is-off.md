---
id: io-is-exported-when-omit-io-is-off
area: opencode-v2/session
runtime: opencode-v2
status: active
input: qa/tools/qa-session-opencode-v2.sh with QA_OMIT_IO=false, one turn that reads one file
duration: ~30s
settling: 25s
cleanup: keep
covers:
  - opencode-v2/index.js
  - internal/source/opencodev2/opencodev2.go
---

## Given

The driver with `QA_OMIT_IO=false`. The opposite of
[default-privacy-redacts-but-keeps-messages](default-privacy-redacts-but-keeps-messages.md): a fix
that redacts too much would pass that spec and fail this one.

## When

```sh
QA_OMIT_IO=false qa/tools/qa-session-opencode-v2.sh \
  'QA-IO-MARKER-5108: read README.md with the read tool, then reply with exactly the word done.' \
  spec-opencode-io-on
sleep 25
```

## Expectation

- `opencode-v2/index.js` looks up the delivered message with `ctx.session.context` and passes its text
  to the exporter as `prompt`; the exporter puts it in `gen_ai.input.messages`.
- The reply text from `session.text.ended` becomes `gen_ai.output.messages`.
- The tool's input from `session.tool.called` becomes `gen_ai.tool.call.arguments`, and the text
  parts of `session.tool.success` become `gen_ai.tool.call.result`.

## Oracle

- `qa/tools/qa-compare.py qa/runs/spec-opencode-io-on`. With `omit_io` off it fails if no `chat`
  span in `plugin-debug.log` carries the prompt.
- `plugin-debug.log` for the messages, **not Dash0**: Dash0 masks `gen_ai.input.messages` and
  `gen_ai.output.messages` at ingest. Measured 2026-10-05 on `qa/runs/spec-opencode-io-on`: the
  debug log carried the prompt and `done`, and Dash0 stored `<REDACTED>` for both, with no marker
  attribute saying so. Tool IO was stored unmasked on the same run.
- `dash0 spans query` filtered to the session, for the tool IO.

## Then

- `qa-compare.py` exits `0`.
- The `chat` span in `plugin-debug.log` has `gen_ai.input.messages` containing
  `QA-IO-MARKER-5108` and `gen_ai.output.messages` containing the reply, `done`.
- The `execute_tool read` span's `gen_ai.tool.call.arguments` names `README.md`, and its
  `gen_ai.tool.call.result` contains text from that file (`QA fixture`).

## Tolerance

**Dash0 storing the messages as `<REDACTED>` is not a plugin defect.** The plugin sent them; the
masking is Dash0's, at ingest. `qa-compare.py` prints a note when it sees this.

**The reply may not be exactly `done`.** A model that adds punctuation still passes; one that says
something else entirely is a prompt problem, so re-run.
