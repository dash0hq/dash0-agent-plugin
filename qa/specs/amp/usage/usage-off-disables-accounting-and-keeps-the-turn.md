---
id: usage-off-disables-accounting-and-keeps-the-turn
area: amp/usage
runtime: amp
status: active
input: qa/tools/qa-session-amp.sh with QA_AMP_USAGE=0, one local turn with one tool call
duration: ~20s
settling: 20s
cleanup: keep
covers:
  - cmd/amp-on-event/main.go
  - internal/source/amp/amp.go
  - internal/harness/harness.go
---

## Given

The same install and configuration as
[../session/turn-produces-one-chat-root-and-one-span-per-tool.md](../session/turn-produces-one-chat-root-and-one-span-per-tool.md),
with usage export switched **off**.

Usage export is on by default and needs working Amp authentication on the
executor, so the opt-out is the documented answer for anyone who does not want
an `amp threads export` subprocess after every turn. This spec proves the
opt-out does what the README promises: accounting stops, the turn does not.

`QA_AMP_USAGE=0` makes the driver set `AMP_PLUGIN_OPTION_EXPORT_USAGE=false`,
which is the environment form of `export_usage: false` in either configuration
file.

## When

```sh
QA_AMP_USAGE=0 qa/tools/qa-session-amp.sh \
  'Run the shell command: echo qa-amp-nousage. Then reply with exactly the word done.' \
  spec-amp-usage-off
# 20s is enough with usage off: nothing polls the export, so the spans land
# shortly after the turn ends rather than up to twenty seconds later.
sleep 20
qa/tools/qa-amp-compare.py qa/runs/spec-amp-usage-off
```

## Then

The turn is exported in full, and only its accounting is missing:

- One `chat` root for the turn, and one `execute_tool` span per tool call, with
  the names agreeing with `--stream-json`.
- `dash0.amp.usage.status` is `disabled` — not absent, and not `unavailable`,
  which would mean the read was attempted and failed.
- The root carries **no** `gen_ai.request.model` and **no** token counts.
  Nothing is zero-filled or estimated.
- **No `chat` children at all.** Every extra `chat` span in an ordinary run
  comes from the usage read, so with it off a turn is exactly one `chat` span.

Measured 2026-10-12 on `qa/runs/spec-amp-usage-off`:

```text
execute_tool shell_command   (no model, no status)
chat                         status=disabled, no model, no tokens
```

`qa-amp-compare.py` printed `usage : off` and `AGREEMENT`, with 1 chat root
against 1 stream turn and `shell_command` on both channels.

## Sharp edges

**`disabled` and `unavailable` are different verdicts and must not be
conflated.** `disabled` means nobody asked; `unavailable` means the export was
attempted and did not answer — in 14 days of dogfooding, 34 of 878 real turns
(3.9%) reported `unavailable`, almost certainly executors without a usable
`amp` login. A spec that accepts either would pass on a fleet that silently
lost all accounting.

**This is the only usage state QA can provoke on purpose.** `unavailable`
needs a broken `amp threads export`, and the helper resolves `amp` from `PATH`
at its own runtime — the same `PATH` the session itself needs — so there is no
way to break one without breaking the other from a single driver run. That
path is covered by the Go tests in `cmd/amp-on-event`, not here.
