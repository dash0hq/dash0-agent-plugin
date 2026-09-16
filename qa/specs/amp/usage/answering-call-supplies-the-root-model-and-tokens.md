---
id: answering-call-supplies-the-root-model-and-tokens
area: amp/usage
runtime: amp
status: active
input: qa/tools/qa-session-amp.sh with QA_AMP_USAGE=1, one local turn with one tool call
duration: ~25s
settling: 30s
cleanup: keep
covers:
  - cmd/amp-on-event/main.go
  - internal/source/amp/usage.go
  - internal/source/amp/amp.go
---

## Given

The same install and configuration as
[../session/turn-produces-one-chat-root-and-one-span-per-tool.md](../session/turn-produces-one-chat-root-and-one-span-per-tool.md),
plus `QA_AMP_USAGE=1`, which sets `AMP_PLUGIN_OPTION_EXPORT_USAGE=true`.

Usage export is on by default, so that variable only matters here as an
explicit record of what the run exercised. `export_usage: false` in either
configuration file, or the variable set to `false`, turns it off and yields
`status=disabled` with no tokens on a run that otherwise looks healthy.

## When

```sh
QA_AMP_USAGE=1 qa/tools/qa-session-amp.sh \
  'Run the shell command: echo qa-amp-usage. Then reply with exactly the word done.' \
  spec-amp-usage
# 30s, not 10s: the helper is detached and polls the export for up to twenty
# seconds after the turn ends, so the spans land well after `amp` has exited.
sleep 30
qa/tools/qa-amp-compare.py qa/runs/spec-amp-usage
```

## Then

The turn's root `chat` span is named by the model call that **answered** the
turn, and carries that call's tokens:

- `dash0.amp.usage.status` is `matched`.
- `gen_ai.request.model` is present on the root.
- Every model call Amp's `--stream-json` reports for the turn is accounted for:
  the answering one on the root, each earlier one on its own zero-duration
  `chat` child.
- No token count is invented, estimated, or zero-filled.

Measured on `qa/runs/fix-conv-5turn`: all five turns `matched`, all five roots
carrying a model, and the output tokens reconciling **exactly** against Amp's
own stream — 222 against 222. `qa-amp-compare.py` asserts that identity
whenever every turn matched.

## This spec was failing, and the history is the point

Written 2026-09-15 as `status: failing`. Measured then on
`qa/runs/setup-probe-amp-ids`: `status=partial`, the root named by the
*tool-calling* round rather than the answering one, and on a five-turn
conversation only one turn in five ever matched.

It was kept as a failing spec rather than softened, because it described what
the code claimed to do — and that is what made it the fix's acceptance test.
The diagnosis, including the causes ruled out along the way and the two changes
that closed it, is in
[amp-answering-model-call-is-never-attributed](../../../findings/amp-answering-model-call-is-never-attributed.md).

The short version: `amp threads export` returns the thread *empty* for several
seconds after a turn and then materializes it whole, so the old single 1.5 s
re-read always landed in the empty window. The helper now polls for up to
twenty seconds, and the bridge hands off after two so that poll is never
latency the user feels.

## Sharp edges

Do not verify this against `amp threads export` read after the session. The
export catches up later, so a post-hoc read shows every assistant message with
usage and makes the defect invisible. The only honest witnesses are the span in
Dash0 and the `--stream-json` stream, which is why `qa-amp-compare.py` computes
its expectation from the latter and reads the export only to explain a
mismatch after one is already found.

**A turn's spans can arrive after `amp` has exited.** The helper is detached
deliberately. Anything that reads spans for an amp run has to wait out the
poll, and a read at 10 s can now report zero spans for a perfectly healthy run.
