# A tool-calling turn emits several `chat` spans — and that is safe

**Status:** closed by measurement. No product change needed; one documentation
claim was wrong and is fixed.
**Found and resolved:** 2026-10-12, from 14 days of real dogfood data.
**Area:** amp/session, amp/usage.

## The question

Amp answers one user prompt with several model calls: one that decides to call
a tool, another after the tool result, and so on until one answers. The plugin
keeps each call's usage on its own span rather than summing them into the
answering model's name, so a turn using two models is never misattributed.

Those spans are `chat` spans, and so is the root. The concern raised in review
was that anything counting `chat` spans would read each earlier call as another
prompt — "one turn might look like several prompts".

## How common the extra spans are

Far more common than the "multi-model turn" framing in the README suggested.
Read from the dev `default` dataset on 2026-10-12, over 14 days, 13 people,
202 threads:

| | |
| --- | --- |
| turns (chat roots) | 882 |
| `chat` spans total | 1,605 |
| turns emitting exactly 2 `chat` spans | 725 (82%) |
| turns emitting exactly 1 | 156 |
| turns using **more than one model** | **1** |
| `execute_tool` spans | 18,106 |
| roots by usage status | `matched` 839, `unavailable` 34, `partial` 5 |

So the extra span is a property of *tool use*, not of multiple models: one per
earlier model call. A turn with four tool rounds produced five `chat` spans
(`qa/runs/spec-amp-patch`).

## Why it does not inflate prompt counts

Dash0 classifies operations at **ingest**, and it classifies only the root.
Measured over 7 days of the same data:

| span | `dash0.operation.type` |
| --- | --- |
| 399 turn roots | `headless` — all of them |
| 340 observation children | absent — all of them |
| 7,353 `execute_tool` spans | absent — all of them |

The roots also carry `dash0.operation.name = chat :modelName`; the children
carry neither attribute. So operation-based prompt and request-latency metrics
see **one operation per turn**, exactly as they do for Claude or Codex. The
inflation does not happen.

**Cost behaves differently, and correctly.** `dash0.gen_ai.usage.cost` is
computed per span from its tokens, so it is present on root *and* children —
on one five-turn run, 0.0043656 on a root and 0.0050875 on its child. Summing a
turn's `chat` spans gives its true cost, because each child really was a billed
model call. Counting those same spans as prompts would be wrong, and nothing
does.

## What was actually wrong

`amp/README.md` claimed those metrics "count that as an additional prompt with
no measured latency". That is false, and it was the only real defect here. It
now states the measurement, and says the extra span follows every earlier model
call rather than only a multi-model turn.

Pinned by
[tool-calling-turn-emits-one-chat-span-per-model-call](../specs/amp/session/tool-calling-turn-emits-one-chat-span-per-model-call.md),
which asserts the shape and the ingest classification, so a change to either is
noticed rather than discovered in a dashboard.
