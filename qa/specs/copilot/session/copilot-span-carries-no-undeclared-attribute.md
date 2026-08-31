---
id: copilot-span-carries-no-undeclared-attribute
area: copilot/session
runtime: copilot
status: active
input: any qa-session-copilot.sh run; the reference is one turn with one tool call
duration: ~5s over an existing run
settling: 25s
cleanup: keep
covers:
  - internal/otlp/otlp.go
  - internal/source/copilot/copilot.go
  - cmd/copilot-on-event/main.go
  - DEVELOPMENT.md
---

## Given

Any completed `copilot` run. This spec asks a different question of the same spans every other spec
here reads, so it costs no session of its own — point it at the newest run.

**Every other spec compares numbers, and a number cannot see a surplus attribute.** A key nobody
expected changes no span count, so `qa-compare.py` exits `0` whether or not it is there. The
expectation here is the attribute tables in `DEVELOPMENT.md`, a hand-maintained contract the
pipeline never reads, which is what makes it an independent record in the same sense the hook
recording is.

**Copilot is the runtime where this class of defect is most likely, and the first run proved it.**
`eventAttributes` in `internal/otlp/otlp.go` copies every payload field it does not recognize, and
the deny list is spelled in the field names Claude Code and Codex use. Copilot's payloads are
camelCase, so a field the deny list already covers under one spelling arrives under another and
ships. `stopReason` did exactly that, on every `chat` span, alongside the already-denied
`stop_hook_active` on the same payload.

**And this check has a blind spot that only a person found.** An interactive session's payloads
carry a `traceparent` that prompt-mode payloads do not, and it reached every `chat` span the same
way — worse than redundant, since it named Copilot's native trace while the span belonged to the
plugin's own. The driver runs prompt mode, so no run of this spec could ever have seen it. It is
denied now and locked by `TestE2ECopilotPerTurnSpans`, which drives the interactive payload shape
through the built binary. Until the driver can run interactive sessions, **a clean result here is a
statement about prompt mode only** — say so when quoting it.

## When

```sh
qa/tools/qa-attrs.py qa/runs/<run-id>
```

Reference run: `qa/runs/setup-probe-copilot`, one turn with one `bash` call, 2 spans, 39 distinct
attribute keys observed against 60 documented.

## Expectation

**Every attribute key on every span of the session appears in `DEVELOPMENT.md`**, in one of the four
tables `qa-attrs.py` reads: Resource attributes, On every span, LLM / chat spans, Tool-call spans.

The tool splits a surplus three ways because the fix differs:

| Class | What it is | Verdict |
| --- | --- | --- |
| raw payload field | no dotted namespace at all — a Copilot payload field the deny list missed | finding; deny it in `attrSkipKeys` |
| undocumented export | the plugin writes the key and the contract does not list it | finding; either the contract is stale or the export was not meant to ship |
| added at ingest | no Go source writes the key, so Dash0 derived it | informational, never a finding |

There is no Copilot-specific key left. Everything a Copilot span carries is a key another
runtime carries too — including a sub-agent's identity, which rides on `gen_ai.agent.name` and
`gen_ai.agent.id` of its own `invoke_agent` span. That is the point: a custom key would pass this
check once documented and still be invisible to every backend feature.

**And one key that must be absent, which this check is the only thing watching.**
`github.copilot.cost` is on every native-OTel `chat` span the plugin reads, in AI credits, and the
plugin deliberately does not carry it through — it would sit one attribute away from
`dash0.gen_ai.usage.cost`, which Dash0 derives from tokens at ingest and reports in money. Because
the key is namespaced, a reintroduction would be classed as an "undocumented export" rather than a
raw payload field, which is exactly the signal this check gives. Nothing denies the key, and
nothing needs to: it reaches the plugin only through the native-OTel file, so the guard is that
neither `internal/source/copilot` nor `attachUsage` carries it across. `TestE2ECopilotPerTurnSpans`
asserts the same absence end to end, on a fixture whose native chat span has it.

## Oracle

`qa/tools/qa-attrs.py`, which reads the same Dash0 spans `qa-compare.py` reads and asks the
contract instead of the counts. It is a second question of channel one, not a third channel.

## Then

- `qa-attrs.py` exits `0`.
- The `Raw payload fields` list is empty. In particular `stopReason` is absent, and so is
  `stop_hook_active`, which shares the same `agentStop` payload and was already denied.
- `traceparent` is absent — though a prompt-mode run proves nothing about it, since those payloads
  never carry it. See the blind spot above.
- The `Undocumented exports` list is empty. In particular `github.copilot.cost` is absent from
  every span: it is a deliberate non-export, and reinstating it would land it here.
- The `Added at ingest` list is informational and may be non-empty; on the reference run it holds
  eleven keys, all `dash0.*` or `user.id`, none of which the plugin can emit or suppress.

## Tolerance

**Exit `2` is not a verdict.** It means the check could not run — no config, a failed query, a
truncated span set, or a moved `DEVELOPMENT.md` heading. Re-run; do not report a pass or a fail.

**`added at ingest` is deduced, not proven.** The tool decides it by grepping the plugin source for
the key as a string literal, so a key assembled at runtime from a prefix matches nothing and lands
in that list wrongly. It is used only to *excuse* a key, never to accuse one, which is why a false
entry there is harmless. Copilot has no such assembled family today — unlike Codex's `rate_limit.*`
and `credits.*` — so the list should be pure ingest additions here.

**A one-turn run cannot see every attribute.** Keys that only appear on a `task` span, a failed
call, or a resumed turn are not exercised. Run this against the newest run of whatever area is being
worked on rather than against one canonical probe, and expect the observed count to move.

**The observed count is not an assertion.** 39 keys is what the reference run carried; the
delegating run in [../subagents](../subagents/README.md) carries 40, the extra one being
`gen_ai.agent.id` on the sub-agent's span. Only the two surplus lists being empty is asserted.
