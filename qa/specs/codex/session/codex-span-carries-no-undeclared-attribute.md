---
id: codex-span-carries-no-undeclared-attribute
area: codex/session
runtime: codex
status: active
input: qa/tools/qa-session-codex.sh, one prompt with one tool call
duration: ~12s
settling: 8s
cleanup: keep
covers:
  - internal/otlp/otlp.go
  - internal/source/codex/codex.go
  - DEVELOPMENT.md
---

## Given

The plugin provisioned by `qa-session-codex.sh`, plus the recorder. Any Codex session will do: this
asserts the shape of the attribute surface, not the content of a workload. One tool call is the
minimum that exercises both span types.

The contract is the attribute tables in `DEVELOPMENT.md`. It is hand-maintained and the pipeline
never reads it, which is what makes it an expectation rather than a mirror of the code.

## When

```sh
qa/tools/qa-session-codex.sh \
  'Run the shell command: echo qa-probe. Then reply with exactly the word done.' \
  spec-codex-attrs
sleep 8
```

Shape, measured on plugin 0.1.25 with codex-cli 0.149.1: 5 hook invocations, 2 spans, 45 distinct
attribute keys observed against 60 documented.

## Expectation

From `DEVELOPMENT.md` and the spans, with no reference to what the plugin chose to send.

**Every attribute key on a Codex span appears in the contract.** `qa-attrs.py` reads the four
attribute tables and subtracts them from the keys observed on the spans. What is left over splits
three ways, and only the first two are findings:

- **A raw payload field.** A key with no dot in it. Codex payload fields reach a span verbatim
  because `eventAttributes` is a deny list, so anything Codex adds to a payload and nobody denies
  ships on every span. This has happened: `turn_id` was on every `chat` and `execute_tool` span of
  every Codex session until 2026-08-25.
- **An undocumented export.** A dotted key the plugin source writes as a literal, absent from
  `DEVELOPMENT.md`. Either the doc is stale or the export was not meant to ship.
- **Added at ingest.** A dotted key nothing in the plugin writes. Informational. On the reference
  run these were `dash0.auth.token`, `dash0.gen_ai.usage.cost`,
  `dash0.internal.coding_agent.qualified`, `dash0.operation.*`, `dash0.resource.*`,
  `dash0.span.name` and `user.id`.

**Why Codex needs its own arm of this.** The Claude spec next door asserts the same invariant, but
the deny list is shared while the payloads are not: a key Claude never sends cannot be caught by a
Claude run. `turn_id` is the proof — it is a Codex payload field, and the Claude spec passed
throughout the months it was shipping.

## Oracle

- Channel one, Dash0: `qa/tools/qa-attrs.py qa/runs/spec-codex-attrs`. Exit `0` means every observed
  key is in the contract, `1` means one is not, `2` means the check could not run.
- Channel two, the hook record: for any key it reports as a raw payload field, `grep` it in
  `record/events/*.json` to confirm Codex is the source and the value matches.

## Then

- `qa-attrs.py` exits `0`.
- `Raw payload fields` is empty. In particular `turn_id` is absent, though it is present in four of
  the five recorded payloads.
- `Undocumented exports` is empty.
- The Codex-only families are present and documented, not surplus: `dash0.gen_ai.billing_mode`,
  `dash0.gen_ai.plan_type`, `dash0.gen_ai.rate_limit.*`, `dash0.gen_ai.credits.*`.
- `gen_ai.harness.name` is `codex` on every span, so the run is reading its own runtime.

## Tolerance

**This reads what Dash0 stored, not what the plugin sent.** An attribute the plugin sends and ingest
drops is invisible here. That limit is the same one the Claude arm has, and `test/e2e/` owns the
wire. The Codex runtime additionally has `plugin-debug.log`, which shows what was emitted — use it
to tell "never sent" from "dropped at ingest" when a key is missing rather than surplus.

**The ingest-added list is deductive.** `qa-attrs.py` separates ingest additions from undocumented
exports by grepping the plugin source for the key as a literal. A key the plugin assembles from a
prefix matches nothing and is filed as ingest-added. Two Codex families are built that way,
`rate_limit.*` and `credits.*`, so that list reads longer on a Codex run than on a Claude one. It is
used only to excuse a key, never to accuse one.

**The count of observed keys is not asserted.** 45 was this run; a session with a sub-agent or a VCS
remote carries more. Only membership in the contract is asserted.

**A new Codex version may add payload fields.** That is exactly what this spec is for. A failure
naming an unfamiliar raw key is a real finding on the day Codex ships it, not a broken spec.
