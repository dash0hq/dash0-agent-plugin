---
id: turn-tokens-are-exact
area: copilot-app/turns
runtime: copilot-app
status: draft
input: fake model in ok mode (1000 prompt and 10 completion tokens per call); three prompts
duration: ~30s
settling: 25s
cleanup: keep
covers:
  - copilot-app/extension.mjs
  - internal/source/copilotapp/copilotapp.go
  - internal/source/copilot/emit.go
---

## Given

The runner procedure in [../README.md](../README.md), with the fake model and `omit_io` off. Start
the fake model with `--mode ok --prompt-tokens 1000 --completion-tokens 10`. Create the target with a kickoff whose
prompt is `one`, after `swap-in`, as the README's fake-model steps say.

With a real model, no record outside the plugin carries tokens, so
[later-turns-are-traced-live](later-turns-are-traced-live.md) can only check scoping by a heuristic.
The fake model reports a fixed usage on every call and logs each call, so here the expected count
of every turn can be computed exactly, from a record the plugin never touched. The turns are
first, live and live, so both the history path and the live path are covered.

## When

Three prompts, each after the previous turn has gone idle. The first is the kickoff's:

```text
kickoff: one
send_session_message <target>: two
send_session_message <target>: three
```

Then wait out the settling time.

## Expectation

**From `fake-model.jsonl`, independently:** every call, its time, and the usage it reported.
Assign each call to a turn by time, splitting at the three `user.message` timestamps in
`events.jsonl`. For turn *n* with *c(n)* calls, the expected input tokens are 1000 × *c(n)*, and
the expected output tokens are 10 × *c(n)*. The fake model answers with text and never calls a
tool, so *c(n)* is expected to be 1. The log decides, not that expectation.

## Oracle

- The debug log, for `gen_ai.usage.input_tokens` and `gen_ai.usage.output_tokens` on each `chat`
  span. Dash0 holds the same values, and is read as a second channel.
- `fake-model.jsonl` and `events.jsonl` in the run directory.

## Then

- Exactly three `chat` spans, in three traces.
- Each `chat` span's `gen_ai.usage.input_tokens` is exactly 1000 × *c(n)*, and its
  `gen_ai.usage.output_tokens` exactly 10 × *c(n)*, in the debug log and in Dash0.
- The three spans' input tokens sum to 1000 × the number of calls in the log.
- Every `chat` span's `gen_ai.request.model` names `qa-fake`.

## Tolerance

**None on the counts.** The fake model reports exact integers and the plugin must copy them. Any
difference is a finding.

**The model name may carry the provider prefix** (`<provider-id>/qa-fake`) or not. Assert that it
ends in `qa-fake`.

**Ingest lag: 25 seconds, and up to five minutes in this runtime.** The debug log is complete at once. When Dash0 holds fewer spans than it, re-query for up to five minutes before calling one missing.
