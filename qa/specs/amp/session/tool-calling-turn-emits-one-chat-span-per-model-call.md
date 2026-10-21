---
id: tool-calling-turn-emits-one-chat-span-per-model-call
area: amp/session
runtime: amp
status: active
input: qa/tools/qa-session-amp.sh, one local turn whose work takes several tool calls
duration: ~40s
settling: 30s
cleanup: keep
covers:
  - internal/source/amp/amp.go
  - internal/source/amp/usage.go
  - cmd/amp-on-event/main.go
---

## Given

The same install and configuration as
[turn-produces-one-chat-root-and-one-span-per-tool.md](turn-produces-one-chat-root-and-one-span-per-tool.md),
with usage export on — which is the default, so this is an ordinary run.

One turn is one prompt from the user's point of view. Amp answers it with
*several* model calls: one that decides to call a tool, another after the tool
result, and so on until one of them answers. The plugin keeps each call's usage
on its own span rather than summing them into the answering model's name, so a
turn is never misattributed when two models are involved.

The cost of that choice is what this spec pins: those spans are `chat` spans
too.

## When

```sh
qa/tools/qa-session-amp.sh \
  'Create a new file at qa/runs/apply-patch-probe.txt whose only content is the line qa-amp-patch. Then reply with exactly the word done.' \
  spec-amp-calls-per-turn
# 30s, not 10s: the helper is detached and polls the export for up to twenty
# seconds after the turn ends.
sleep 30
qa/tools/qa-amp-compare.py qa/runs/spec-amp-calls-per-turn
```

`qa/runs/` is gitignored, so the file the turn writes leaves the working tree
clean. A prompt that edits a tracked file would not.

## Then

- One turn produces **one** `chat` root: the span without
  `dash0.amp.timing=observation`. `qa-amp-compare.py` asserts that equality.
- It produces **one further `chat` span per earlier model call**, each
  zero-duration and tagged `dash0.amp.timing=observation`.
- Every tool call gets its own `execute_tool` span, and the names agree with
  Amp's `--stream-json`.
- The turn's output tokens reconcile exactly against the stream.

Measured 2026-10-12 on `qa/runs/spec-amp-patch`: 4 tool calls
(`shell_command` ×3, `apply_patch` ×1), 1 chat root, **4 extra chat spans**,
`status=matched`, 347 output tokens on both channels, `AGREEMENT`. Measured the
same day on `qa/runs/setup-probe-amp`: 1 tool call, 1 chat root, **1** extra
chat span, 68 output tokens on both channels.

## And the extra spans are not counted as prompts

Dash0 classifies operations at ingest, and it classifies only the root. Pinned
here because it is the whole reason the shape above is safe:

- every turn root carries `dash0.operation.type=headless` and
  `dash0.operation.name=chat :modelName`;
- every `observation` child carries **neither**, and neither does any
  `execute_tool` span.

Measured 2026-10-12 over 7 days of real dogfood traffic in the dev `default`
dataset: 399 roots, all classified; 340 children and 7,353 tool spans, none
classified. So operation-based prompt and request-latency metrics see one
operation per turn, the same as a Claude or Codex turn.

Cost is the deliberate exception: `dash0.gen_ai.usage.cost` is computed per
span from its tokens and is present on root and children alike, because each
child really was a billed model call. Sum a turn's `chat` spans for cost;
never count them for prompts.

At scale, over 14 days, 13 people and 202 threads: 882 turns produced 1,605
`chat` spans, 725 turns (82%) emitted exactly two — and exactly **one** turn in
882 used more than one model. See
[amp-tool-calling-turn-counts-as-several-prompts](../../../findings/amp-tool-calling-turn-counts-as-several-prompts.md).

## Sharp edges

**Do not count `chat` spans to count turns.** A turn's root and its usage
children share the name. Split them on `dash0.amp.timing=observation`;
`qa-amp-compare.py` does, and the old version of it — which did not — reported
`MISMATCH` on every healthy run the moment usage export became the default.

**The number of extra spans is a property of the prompt, not of the plugin.**
A turn that needs four tool rounds produces four. Do not assert a fixed count
for a prompt whose tool use the model chooses; assert one root and let the
children be whatever the stream says.
