---
id: skill-invocation-is-attributed-by-either-route
area: codex/skills
runtime: codex
status: draft
input: qa/skill-fixture, installed by QA_CODEX_SKILL=1, over two turns
duration: ~30s
settling: 8s
cleanup: keep
covers:
  - internal/source/codex/rollout.go
  - internal/source/codex/codex.go
  - internal/otlp/otlp.go
  - qa/skill-fixture/qa-echo/SKILL.md
---

## Given

The plugin provisioned by `qa-session-codex.sh`, plus the recorder, with `QA_CODEX_SKILL=1`.
That installs [../../../skill-fixture/qa-echo](../../../skill-fixture/qa-echo/SKILL.md) into
`$HOME/.agents/skills` in the throwaway home, where Codex looks for a person's own skills.
`qa/skill-fixture/fixture_test.go` pins the fixture's name, description and marker, so the
spec and the fixture cannot drift apart.

**Codex has no `Skill` tool.** It loads a skill by injecting it into the conversation —
progressive disclosure: the model sees every skill's name and description, and the full
`SKILL.md` arrives only once it picks one. So there is no `PostToolUse` to enrich and no
`execute_tool Skill` span; the invocation is reported on the turn's `chat` span, exactly as
Claude Code's slash-command route is, and for the same reason. See
[the two-routes section](../../../../DEVELOPMENT.md).

**Two turns, because the negative control is half the point.** A session with any skill
installed carries `<skills_instructions>` — the catalogue of everything *available* — in
every turn. Reading that as usage would attribute a skill to every turn of every session
that has one, which is a failure no single-turn run can see.

## When

```sh
QA_CODEX_SKILL=1 \
QA_CODEX_RESUME='Reply with exactly the word two.' \
  qa/tools/qa-session-codex.sh \
  'Use the $qa-echo skill to emit the QA marker.' \
  spec-codex-skill
sleep 8
```

Shape, measured on the working tree with codex-cli 0.149.1: 8 hook invocations, 2 `Stop`,
2 `chat` spans, 1 `execute_tool Bash`, and `QA-SKILL-MARKER` in `codex-events.jsonl`.

## Expectation

From `rollout.jsonl` and the run's own output, neither of which the plugin writes.

**Which skill loaded, and in which turn.** When Codex loads a skill it appends a user
message to the rollout of the form:

```
<skill>
<name>qa-echo</name>
<path>/…/.agents/skills/qa-echo/SKILL.md</path>
```

The name in that block is the expected `dash0.gen_ai.tool.skill.name`. Partition by turn the
same way [../session/turn-usage-is-scoped-to-its-own-turn](../session/turn-usage-is-scoped-to-its-own-turn.md)
does — the recorder's `Stop` timestamps — so the block is attributed to the turn it appeared
in and not to the next one.

**Who chose it.** The person's own message is in the same rollout, before the block:
`Use the $qa-echo skill to emit the QA marker.` It carries Codex's `$mention`, so the choice
was theirs and `source` is `command`. Without a mention the model chose from the catalogue
and `source` is `model`.

> Codex injects user messages of its own — `<recommended_plugins>`, the `<skill>` block
> itself — so the role alone does not say who wrote a message. Its injections open with a
> tag and a person's prompt does not, which is what separates the person's words when
> deciding the route.

**That the skill was really used, not merely named.** The fixture's whole body is one
command printing `QA-SKILL-MARKER`. The marker in the session's output is independent
evidence that the instructions reached the model, so a run cannot pass by recording a name
while the skill did nothing.

**The second turn used no skill**, so its `chat` span must carry neither attribute — even
though the catalogue is in its context exactly as it was in the first turn's.

## Oracle

- Channel one, Dash0: `dash0 spans query` filtered to `gen_ai.conversation.id`, reading
  `dash0.gen_ai.tool.skill.name` and `.source` off each `chat` span, ordered by
  `startTimeUnixNano`. `qa-compare.py` cannot see this: no count changes either way.
- Channel two, the record: the `<skill>` block in `rollout.jsonl`, the person's message in
  the same file, and `QA-SKILL-MARKER` in `codex-events.jsonl`.

## Then

- Dash0 holds exactly 2 `chat` spans.
- The **first** turn's `chat` span carries `dash0.gen_ai.tool.skill.name` equal to the name
  in the rollout's `<skill>` block — `qa-echo` on this input.
- Its `dash0.gen_ai.tool.skill.source` is `command`, because the person's own message
  contains `$qa-echo`.
- The **second** turn's `chat` span carries **neither** attribute. This is the assertion the
  second turn exists for: the catalogue is present in both turns and must not be mistaken
  for use.
- No `execute_tool` span carries either attribute. Codex runs no tool for a skill, and a
  span that claimed otherwise would be inventing one.
- `QA-SKILL-MARKER` appears in `codex-events.jsonl`, so the skill's body ran.
- `qa-compare.py` exits `0` and `qa-attrs.py` exits `0` — the second matters here, because a
  new attribute that is not in `DEVELOPMENT.md` is exactly what it catches.

## Tolerance

**The `model` route is not asserted, because it cannot be forced.** Dropping the `$mention`
and describing the task instead may make the model load the skill, or may make it run the
command itself — its choice, and not repeatable. A run where the second turn loads the skill
without a mention must show `source: model`, and that is worth checking when it happens; it
is not something to arrange.

**Which shell the model uses is its choice.** The fixture asks for one `echo`. A run that
wraps it differently, or adds a call, still either agrees with its own record or does not.
Only the marker's presence is asserted, not how it was produced.

**A skill loaded but not followed is still attributed.** The name comes from the load, not
from obedience. If the marker is absent while the attribute is present, that is a finding
about the fixture or the model, not about the plugin — say which before reporting it.

**One skill per turn is reported.** A turn that loads two can only be labelled with one, and
the reader takes the last. No input here produces that, and a run that does should be
reported rather than asserted against.

**The catalogue grows with the fixture directory.** Adding skills to `qa/skill-fixture`
changes what every Codex run carries in context and gives the model more to choose from.
That is a reason to keep the fixture at one skill unless a spec needs a second.

**Ingest lag.** A few seconds, as everywhere in this suite.
