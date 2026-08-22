---
id: span-carries-no-undeclared-attribute
area: session
status: draft
input: qa/tools/qa-session.sh, one prompt that produces all three span types
duration: ~25s
settling: 10s
cleanup: keep
covers:
  - internal/otlp/otlp.go
  - DEVELOPMENT.md
---

## Given

The plugin as installed, plus the recorder, with `QA_ALLOWED_TOOLS="Task Agent Bash"` so one session
produces all three span types. A `chat`, an `execute_tool`, and an `invoke_agent` span carry different
attribute sets, and an attribute that leaks onto only one of them is missed by a spec that reads only
another.

`eventAttributes` in `internal/otlp/otlp.go` is a **deny list**. It copies every field of the hook
payload onto the span unless the field appears in `attrSkipKeys`:

```go
for k, v := range event {
    if attrSkipKeys[k] {
        continue
    }
    ...
}
```

So a field Claude Code adds to a payload is exported by default, under its raw payload name, with no
change on our side. That is not a hypothetical. `prompt_id`, `session_crons`, and `background_tasks`
shipped exactly this way and were live on every `chat` and `invoke_agent` span, and `prompt_id` on
every `execute_tool` span. `internal/demo/spans/chat.json` is a span captured on 2026-06-17 that
already carries two of the three, so they exported for over two months before anyone looked.

`background_tasks` is why this spec exists rather than a unit test alone. It carries the `Task` tool's
`description`, which is model-authored content, on a key that is not in `contentKeys`. So `omit_io`
did not redact it: a customer who turned content off was still shipping it.

## When

```sh
QA_SWAP_BINARY=1 QA_MODEL=haiku QA_ALLOWED_TOOLS="Task Agent Bash" qa/tools/qa-session.sh \
  'Use the Task tool (subagent_type general-purpose) to ask a sub-agent to run these three bash commands one after another, each as a separate Bash call: "sleep 1; echo one", then "sleep 1; echo two", then "sleep 1; echo three". When it returns, reply with exactly the word done.' \
  spec-attrs
sleep 10
```

The same prompt as
[sub-agent-tool-call-produces-a-span](sub-agent-tool-call-produces-a-span.md), deliberately. It is
the cheapest prompt that produces all three span types in one session, so one run covers the whole
attribute surface instead of three runs covering a third of it each.

Shape on the verifying run: 7 spans, 2 `chat`, 4 `execute_tool`, 1 `invoke_agent`, and 47 distinct
attribute keys across them including the resource.

## Expectation

**The expectation is the four attribute tables in `DEVELOPMENT.md`,** under `Resource attributes`,
`On every span`, `LLM / chat spans`, and `Tool-call spans`. That document is written by hand and the
pipeline never reads it, so it is an independent record in the same sense the hook recording is. 58
keys on the verifying run.

This is the one spec in this area whose expectation is a *contract* rather than a measurement. Every
other spec here asks whether a number is right. This one asks whether anything is present that nobody
declared, which no count can answer: an unexpected attribute changes no span total, so
`qa-compare.py` exits `0` whether or not it is there. Related:
[[oracle-the-hook-mapping-is-blind-to-work-that-fires-no-tool-hook]], the same blindness in the other
direction.

**Surplus is classified, because the fix differs.** `qa/tools/qa-attrs.py` splits it three ways:

| Class | Test | Verdict |
| --- | --- | --- |
| Raw payload field | The key has no dotted namespace | Finding. A hook payload field nobody denied. |
| Undocumented export | A non-test Go file under `internal/` writes the key as a literal | Finding. Either the contract is stale or the export was not meant to ship. |
| Added at ingest | No Go source writes the key | Informational. The plugin cannot emit or suppress it. |

The third class is the reason this spec needs a rule rather than a plain set difference. **The stored
span is not the sent span.** Dash0 derives attributes at ingest, so a query returns keys the plugin
never sent: `dash0.gen_ai.usage.cost`, `dash0.gen_ai.request.model.original`, `dash0.operation.*`,
`dash0.span.name`, `dash0.internal.coding_agent.qualified`, `dash0.resource.*`, `dash0.auth.token`,
and `user.id` on the verifying run. Grepping the plugin for the key is what separates them, and it is
a deductive test, not an independent one — it is used only to *exclude* keys from the finding set,
never to justify one.

**Absence is not asserted.** Most documented keys are conditional: `gen_ai.conversation.name` is
interactive-only, the `dash0.gen_ai.rate_limit.*` and `dash0.gen_ai.credits.*` families are Codex
only, `dash0.team.name` needs `team_name` set. A documented key missing from a Claude run is normal.
Only surplus is a finding.

## Oracle

- Channel one, Dash0: `qa/tools/qa-attrs.py qa/runs/spec-attrs`. Exit `0` means every observed key is
  in the contract or added at ingest. Exit `1` lists the surplus and which span types carry it.
- Channel two, the contract: `DEVELOPMENT.md`. The tool fails with exit `2` rather than `0` if any of
  the four headings has moved, because a partial contract would report healthy keys as surplus and a
  silent partial parse is worse than no check.
- Channel three, the session is otherwise intact: `qa/tools/qa-compare.py qa/runs/spec-attrs`. A run
  whose span counts are wrong is not a clean reading of its attribute surface.
- `qa-compare.py` is **not** an oracle for this spec on its own. It compares counts, and this spec is
  about keys.

## Then

Measured on the verifying run:

- `qa-attrs.py` exits `0` and prints `Every attribute is in the documented contract.`
- No observed key lacks a dotted namespace. A bare `snake_case` key is the signature of a copied hook
  payload field, and there is no legitimate one.
- `prompt_id`, `session_crons`, and `background_tasks` are absent from all three span types.
- `qa-compare.py` exits `0`, so the attribute reading came from a session whose counts reconcile.
- The keys reported as added at ingest are namespaced under `dash0.` or are `user.id`. An
  unnamespaced key in that class would mean the plugin stopped writing something it used to write,
  which the class cannot distinguish from ingest and which the first assertion catches anyway.

## Tolerance

**A pass means nothing if the run produced one span type.** Read the span count first. The prompt is
chosen to produce all three, and the model may decline to delegate — no `invoke_agent` span means
re-run, not pass. This is the same discard rule as
[sub-agent-usage-is-counted-once](sub-agent-usage-is-counted-once.md).

**The ingest-added set will grow, and that is not a finding.** Dash0 adds attributes on its own
schedule and the plugin has no say. A new key in that class needs no action here. It is printed
rather than hidden so that a key moving *out* of it — because the plugin started writing it — is
visible as the class change it is.

**An undocumented export may be a documentation bug rather than a telemetry bug.** The tool cannot
tell which, and it does not guess. Read the key: if the export is wanted, add it to `DEVELOPMENT.md`;
if not, deny it in `attrSkipKeys`. Both close the finding, and the choice is not the tool's to make.

**This spec cannot see the wire.** It reads what Dash0 stored, so an attribute the plugin sends and
ingest drops is invisible here. `test/e2e/` owns the sent bytes against a mock, and a unit test in
`internal/otlp/` is the cheaper place to pin a specific key. This spec's job is the one thing neither
can do: notice a key nobody thought to write a test for.

**The deny-list shape is the standing cause, and this spec does not fix it.** It detects the next
occurrence rather than preventing it. An allow list would make an unrecognized field invisible by
default instead of exported by default. Until that changes, expect this spec to fail again whenever
Claude Code grows a payload field, and treat that failure as working as intended.

**Content is not readable through this oracle.** The API returns `gen_ai.input.messages` and the tool
I/O keys as `<REDACTED>`, so this spec asserts which keys exist and never what is in them. That is
also why `background_tasks` was worth denying on the strength of its key alone: its value could not
be inspected from here, only its presence.

**Ingest lag.** A few seconds, as everywhere else in this suite. `0 spans` exits `2`, not `0`, so lag
cannot read as a clean pass.
