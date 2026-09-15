---
id: amp-span-carries-no-undeclared-attribute
area: amp/session
runtime: amp
status: active
input: any qa-session-amp.sh run; one with QA_AMP_USAGE=1 that reached `matched`
duration: ~5s over an existing run
settling: 10s
cleanup: keep
covers:
  - internal/otlp/otlp.go
  - internal/source/amp/amp.go
  - DEVELOPMENT.md
---

## Given

Any completed `amp` run. Like its four siblings this spec asks a different
question of spans another spec already paid for, so point it at the newest run
rather than driving a session of its own.

**Every other spec compares counts, and a count cannot see a surplus
attribute.** A key nobody expected changes no span total, so
`qa-amp-compare.py` exits `0` whether or not it is there. The expectation is the
attribute tables in `DEVELOPMENT.md`, a hand-maintained contract no part of the
pipeline reads.

**Amp needs no `manifest.json` port.** `qa-attrs.py` is shared across all five
runtimes and reads `manifest.json` for a `session_id` and a time window;
`qa-session-amp.sh` writes one, so the tool runs unmodified. Its timestamps must
carry a fractional second — `qa-compare.widen` parses `%Y-%m-%dT%H:%M:%S.%f%z`
and `date -u` cannot emit one portably on macOS, which is what the driver's
`stamp()` helper is for.

**Amp's surplus class is different from Cursor's.** Cursor's problem was
`eventAttributes` copying raw hook fields; amp has none of that, because the
bridge builds a typed `Turn` envelope rather than forwarding a payload. Every
surplus found on amp so far has been the other kind: a **deliberate export that
`DEVELOPMENT.md` had not caught up with**. Six were found and documented this
way:

| Key | Found by | Why it is easy to miss |
| --- | --- | --- |
| `dash0.amp.turn.id` | first probe | on both `chat` and `execute_tool` |
| `dash0.amp.executor.kind` | first probe | on both |
| `dash0.amp.truncated` | first probe | root only, and only when content was shed |
| `dash0.amp.usage.status` | first probe | root only, usage opt-in |
| `dash0.amp.usage.source` | first probe | root only, and only when usage **matched** |
| `dash0.amp.timing` | the 5-turn conversation | on the usage **child** span, which exists only on a `matched` turn |

## When

```sh
qa/tools/qa-attrs.py qa/runs/<run-id> --dataset <ampDataset>
```

`--dataset <ampDataset>` — the `ampDataset` value from `qa/config.local.json` —
is not optional. The amp arm writes to `ampDataset`, not
the shared `dataset`, so the tool's default reads an empty window and reports a
clean pass over zero spans. See `### Amp writes to its own dataset, not default` in
[../../setup.md](../../../setup.md).

Reference run: `qa/runs/conv-amp-5turn-b`, five turns, ten spans.

## Then

- `Every attribute is in the documented contract.`, exit `0`.
- No `Raw payload fields` section. One appearing means the bridge started
  forwarding something untyped, which on amp would be new behaviour rather than
  a missed deny-list entry.
- The Dash0-added keys (`dash0.auth.token`, `dash0.resource.*`,
  `dash0.span.name`, `dash0.operation.*`, `dash0.gen_ai.usage.cost`, `user.id`,
  `dash0.internal.coding_agent.qualified`) appear only in the informational
  list. That list is deductive — the tool greps the Go source for each key as a
  literal — so use it to excuse a key, never to accuse one.

## Tolerance

**A single-turn run cannot clear this spec.** Three of the six keys above are
reachable only in states a minimal probe never enters: `truncated` needs shed
content, and `usage.source` and `timing` need a turn whose usage actually
matched. `matched` is a race on a real session — see
[amp-answering-model-call-is-never-attributed](../../../findings/amp-answering-model-call-is-never-attributed.md)
— so run this against the longest run available, with `QA_AMP_USAGE=1`, and
treat a pass over a run that reported only `partial` as covering the root
attributes alone.

**Exit `2` is not a verdict.** No config, no spans, or a moved `DEVELOPMENT.md`
heading. Re-run rather than reading it as pass or fail.

**Server-executed tools contribute no span and therefore no attributes**, so a
clean result says nothing about them — see
[amp-server-side-tools-produce-no-span](../../../findings/amp-server-side-tools-produce-no-span.md).
