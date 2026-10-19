# Amp: the answering model call is never attributed

> **Fixed.** Kept for the measurements and the ruled-out causes, which are
> what the fix was built on. See `## The fix` at the end.

**Runtime:** amp
**Found:** 2026-09-15, by the first `amp` QA probes
**Status:** FIXED 2026-09-15, verified on `qa/runs/fix-conv-5turn`
**Affects:** `cmd/amp-on-event/main.go`, `internal/source/amp/usage.go`, `amp/index.ts`

## What happens

With `AMP_PLUGIN_OPTION_EXPORT_USAGE=true`, every turn reports
`dash0.amp.usage.status=partial`, and the model call that actually answered the
turn contributes no tokens to any span. On a turn with a single model call, the
turn gets **no model and no tokens at all**.

This is not the documented degraded case. `amp/README.md` and the code comment
in `internal/source/amp/amp.go` both say the root `chat` span is named by "the
last selected record — the message that answered the turn". In practice that
record is never present, so the root is named by whichever *earlier* call
happened to have persisted, or by nothing.

## Measured

Two probes, both on plugin built from `a1cc0ed`, Amp CLI as installed
2026-09-15, exporting to the amp dataset.

`qa/runs/setup-probe-amp-ids` — one turn, two model calls:

| | |
| --- | --- |
| Bridge sent `assistant_ids` | `M-034PFd1Lj3AZZuc7ORye3X`, `M-034PFd9Vk7OkWuxfCE8uuf` |
| `amp threads export` at helper read time | 3 messages: user 1, assistant 2, user 3 |
| The same export re-read afterwards | 4 messages: the answering assistant 4 is there, with usage |
| Span produced | `chat gpt-5.6-sol`, `output_tokens=49`, `status=partial` |

The 49 output tokens are assistant message **2**, the tool-calling round. The
answering message **4** is absent from the span entirely. Amp's own
`--stream-json` reports both calls for the same turn.

`qa/runs/setup-probe-amp-turns` — two turns, four model calls: both turns
`status=partial`, **no** `gen_ai.request.model` and **no** token attributes on
either root, while the post-hoc export carries usage on all four assistant
messages.

## Why the existing retry does not catch it

`cmd/amp-on-event/main.go` re-reads the thread once after 1.5 s when the first
read returns `partial`. That is the right idea and the wrong duration: the
answering message was still missing from `amp threads export` when the driver
exported the thread again *after the whole session had exited*, which is far
more than 1.5 s later. Whatever flushes that message is not on a short timer.

## Why the unit tests do not catch it

`internal/source/amp/amp_test.go` and `amp/transport.test.ts` build the export
fixture with the answering message already present and already carrying usage.
That is the one state the real export is not in at the moment the helper reads
it. The protocol-id matching those fixtures exercise is genuinely correct — a
hand-built envelope naming `messageId` 2 and 4 against the real export returns
`status=matched` with `model=gpt-5.6-sol` and the expected usage child span, so
the matcher is not at fault. The fixture's *timing* is what is unreal.

## Not the cause

Ruled out during the investigation, so nobody repeats it:

- **Protocol vs numeric ids.** The bridge passes `M-…` strings and the export
  names messages numerically, which looks like the bug and is not: the export
  also carries `protocolMessageID`, `ReadUsage` keys on both, and the first
  assistant message matched correctly through exactly that path.
- **The matcher.** Fed ids that are present, it matches, names the model and
  emits the usage child span.

## What to decide

The fix is a product decision, not a mechanical one:

1. Wait longer or poll, accepting that the helper holds the host turn for that
   time — and `amp/index.ts` awaits delivery at `agent.end`, so that is
   directly user-visible latency.
2. Take usage from somewhere other than `amp threads export`.
3. Accept the gap and stop claiming the answering message names the root —
   which means correcting `amp/README.md`, `FEATURE_MATRIX.md` and the comment
   in `internal/source/amp/amp.go`, and reconsidering whether an opt-in feature
   that cannot attribute the answering call is worth shipping as is.

Whichever is chosen, `dash0.amp.usage.status=partial` is doing its job: it is
the attribute that made this visible, and it should not be softened.

## The fix

Two changes, both amp-only.

**The re-read was not too short, it was the wrong shape.** `amp threads export`
does not lag incrementally. For the first seconds after a turn it returns the
thread with *no messages at all*, then materializes the whole thing at once,
complete with usage — measured at roughly seven seconds past CLI exit, which is
later still than `agent.end`. A single re-read after 1.5 s landed inside the
empty window every time, which is why `partial` was the normal result rather
than a rare one. `cmd/amp-on-event/main.go` now waits before reading rather
than after and backs off by half each round, covering the same twenty-second
window in four exports instead of ten and landing the first read past the empty
stretch. It stops the moment usage matches. It also gives up after two
consecutive export *command* failures, because a missing or logged-out `amp` is
a broken environment rather than a thread that needs another moment, and
polling it would spawn twenty processes per turn.

**Waiting had to stop costing the user anything.** Amp awaits the `agent.end`
handler, so a twenty-second poll would have been twenty seconds of latency
between turns — which is why option 1 in `## What to decide` was rejected when
this was written. `amp/index.ts` now detaches the helper and stops *waiting*
for it after two seconds. A healthy helper finishes in 110-200 ms, so the
common path is unchanged and still reports its exit code; only the slow usage
poll is released early. A failure detected after the handoff logs itself rather
than being silently dropped.

The consequence worth knowing: **a turn's spans can now arrive after the
process that produced them has exited.** Anything reading them has to wait.
`qa/tools/qa-amp-compare.py` and the setup checks now sleep 25-30 s rather
than 10 s.

## Measured after the fix

`qa/runs/fix-conv-5turn`, the same five-turn conversation that produced the
measurements above:

| | Before | After |
| --- | --- | --- |
| Turns reporting `matched` | 1 of 5 | **5 of 5** |
| Roots with `gen_ai.request.model` | 4 of 5 | **5 of 5** |
| Output tokens, stream vs spans | not comparable | **222 vs 222** |

The token identity is the strong one: Amp's own `--stream-json`, which the
plugin never reads, reports 222 output tokens for the conversation, and the
exporter's per-call attribution now sums to exactly that. `qa-amp-compare.py`
asserts it whenever every turn matched, rather than leaving it to be compared
by hand.
