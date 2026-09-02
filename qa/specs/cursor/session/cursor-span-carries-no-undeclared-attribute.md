---
id: cursor-span-carries-no-undeclared-attribute
area: cursor/session
runtime: cursor
status: active
input: any qa-session-cursor.sh run, plus one whose tool call failed
duration: ~5s over an existing run
settling: 25s
cleanup: keep
covers:
  - internal/otlp/otlp.go
  - internal/source/cursor/cursor.go
  - DEVELOPMENT.md
---

## Given

Any completed `cursor` run. This spec asks a different question of the same spans every other spec
here reads, so it costs no session of its own — point it at the newest run.

**Every other spec compares numbers, and a number cannot see a surplus attribute.** A key nobody
expected changes no span count, so `qa-compare.py` exits `0` whether or not it is there. The
expectation here is the attribute tables in `DEVELOPMENT.md`, a hand-maintained contract the pipeline
never reads, which is what makes it an independent record in the same sense the hook recording is.

**Cursor was the worst runtime yet for this class, and the first run proved it.**
`eventAttributes` in `internal/otlp/otlp.go` copies every payload field it does not recognize, and
the deny list was spelled in the fields Claude Code, Codex and Copilot use. Cursor puts four of its
own on **every** payload, so all four reached every `chat` and `execute_tool` span:

| Key | Was | Why it is denied now |
| --- | --- | --- |
| `conversation_id` | the session id again | `session_id` already becomes `gen_ai.conversation.id`; this duplicated it unnamespaced |
| `generation_id` | Cursor's `prompt_id` | groups a turn's spans, which the trace already does through parenting |
| `cursor_version` | the host build | `gen_ai.harness.name` says which agent; no other runtime exports its version |
| `workspace_roots` | a JSON array of absolute paths | put the developer's filesystem layout on every span, on a raw key `omit_io` does not cover |

**A fifth is reachable only on the failure path.** `failure_type` is on `postToolUseFailure` and
nowhere else, so a healthy probe cannot see it. That is why this spec needs two runs, and why
[tool-failure-sets-the-span-status](tool-failure-sets-the-span-status.md) asserts its absence too.

**This spec covers the interactive payload shape, which is the one users get.** That is the opposite
of the Copilot equivalent, whose driver runs prompt mode and which therefore cannot speak for
interactive payloads at all. The cursor driver has no choice but to be interactive: print mode fires
no `afterAgentResponse`.

## When

```sh
qa/tools/qa-attrs.py qa/runs/<healthy-run-id>
qa/tools/qa-attrs.py qa/runs/<failed-tool-run-id>
```

Reference runs: `qa/runs/setup-probe-cursor-attrs`, one turn with one `Shell` call, 2 spans, 37
distinct keys observed against 60 documented; and `qa/runs/probe-cursor-tool-failure2`, the same
prompt with `exit 3`, 41 keys observed.

## Expectation

**Every attribute key on every span of the session appears in `DEVELOPMENT.md`.** `qa-attrs.py`
splits a surplus the same three ways it does on every runtime, and on Cursor the first class is the
live one: a raw payload field means `eventAttributes` copied a hook field nobody denied, and the fix
is `attrSkipKeys` in `internal/otlp/otlp.go`.

**None of the five keys above appears on any span.**

**`gen_ai.conversation.id` still does.** It comes from `session_id`, not from the denied
`conversation_id`, and every Cursor payload carries both with the same value. Denying the wrong one
of the pair would take the conversation id off every span and break every read in this directory,
which is why this is asserted rather than assumed.

**The keys Dash0 adds at ingest are not exports.** `dash0.auth.token`, `dash0.resource.*`,
`dash0.span.name`, `dash0.operation.*`, `dash0.gen_ai.usage.cost`, `user.id`,
`dash0.internal.coding_agent.qualified` and — on the failure run — the
`dash0.error.fingerprint.*` family all appear in `qa-attrs.py`'s informational list. That list is
deductive: the tool greps the Go source for each key as a literal, so a key assembled at runtime
lands there too. Use it to excuse a key, never to accuse one.

## Oracle

- `qa/tools/qa-attrs.py <run>`, over the same Dash0 spans `qa-compare.py` reads. It is a second
  question asked of channel one, not a third channel.
- `DEVELOPMENT.md`'s four attribute tables, which the tool parses. A moved heading makes it exit `2`
  rather than pass.

## Then

- Both runs report `Every attribute is in the documented contract.` and exit `0`.
- The healthy run observes 37 keys; the failure run observes 41, the extra four being Dash0's error
  fingerprint family.
- No `Raw payload fields` section appears in either.
- `gen_ai.conversation.id` is present on both spans of both runs — proved by `qa-compare.py` finding
  the spans at all, since that is the filter it queries on.

## Tolerance

**The observed key count is not a constant.** A turn with more tool calls, a repository with a
different VCS state, or a Dash0 rule change all move it. Only the verdict is asserted.

**Exit `2` is not a verdict.** It means the check could not run: no config, no spans, or a moved
`DEVELOPMENT.md` heading. Re-run rather than reading it as pass or fail.

**A clean result speaks for the events this run fired.** Cursor declares nine hook events and a
plain probe reaches six of them. `subagentStop` fires on no delegating run measured so far — see
[../subagents](../subagents/README.md) — so its payload's fields have never been through this check.
