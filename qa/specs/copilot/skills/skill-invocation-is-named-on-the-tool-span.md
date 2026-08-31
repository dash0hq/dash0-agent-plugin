---
id: skill-invocation-is-named-on-the-tool-span
area: copilot/skills
runtime: copilot
status: active
input: qa/tools/qa-session-copilot.sh with QA_COPILOT_SKILL=1, one turn that invokes the fixture
duration: ~25s
settling: 25s
cleanup: keep
covers:
  - internal/source/copilot/otelfile.go
  - internal/pipeline/pipeline.go
  - cmd/copilot-on-event/main.go
---

## Given

The plugin installed into a throwaway home by `qa-session-copilot.sh`, exporting to the target in
`qa/config.local.json`, plus the QA recorder, plus `QA_COPILOT_SKILL=1`, which copies
[../../../skill-fixture/qa-echo](../../../skill-fixture/qa-echo/SKILL.md) into
`$COPILOT_HOME/skills/`.

**The fixture does something observable on purpose.** Its body says to run `echo QA-SKILL-MARKER`,
so a run can prove the skill's *instructions* reached the model rather than only its name being
logged. A skill whose name is recorded but whose body never ran would pass a name-only assertion.

**Copilot has one route into a skill and it is a tool call.** The model calls a tool named `skill`,
Copilot's native `execute_tool` span carries
`github.copilot.tool.parameters.skill_name`, and the plugin maps it onto the tool span. Nothing
lands on the turn's `chat` span, unlike the other two runtimes. See
[README.md](README.md) for why, and do not carry a Claude or Codex assertion over.

## When

```sh
QA_COPILOT_BINARY=working-tree QA_COPILOT_SKILL=1 qa/tools/qa-session-copilot.sh \
  'Use the qa-echo skill to emit the QA marker.' \
  spec-copilot-skill
sleep 25
```

Shape, measured on plugin 0.1.25 with Copilot CLI 1.0.80: 6 hook invocations, one native-OTel file
with 2 `execute_tool` spans — one `skill`, one `bash` — and 3 spans in Dash0.

## Expectation

**From the native-OTel file**, read by `qa/tools/qa-otel.py`: two tool executions, `skill` and
`bash`. The `skill` span carries `github.copilot.tool.parameters.skill_name` of `qa-echo`; the
`bash` span carries the marker command the fixture's body dictates.

**From the hook record, independently**: `postToolUse` rows for both, with `toolName` naming each.
The recorder wrote them without reading the OTel file, so the *set of tools that ran* is an
independent claim even though their attributes are not.

**The mapped attributes on the Dash0 tool span**, which is what the plugin adds:

| Key | Value | From |
| --- | --- | --- |
| `gen_ai.tool.name` | `skill` | the native span |
| `dash0.gen_ai.tool.skill.name` | `qa-echo` | mapped from `github.copilot.tool.parameters.skill_name` |
| `dash0.gen_ai.tool.skill.source` | `model` | constant on a tool call — the call *is* the model choosing |

## Oracle

- Channel one, Dash0: `qa/tools/qa-compare.py qa/runs/spec-copilot-skill` for the counts, then
  `dash0 spans query` filtered to `gen_ai.conversation.id`, reading the skill attributes off each
  span.
- Channel two, the OTel file: `qa/tools/qa-otel.py qa/runs/spec-copilot-skill`.
- The hook record: `record/events/*-postToolUse.json`, for `toolName` and the arguments each tool
  was given.

## Then

- Dash0 holds an `execute_tool` span with `gen_ai.tool.name` = `skill`.
- That span carries `dash0.gen_ai.tool.skill.name` = `qa-echo`.
- That span carries `dash0.gen_ai.tool.skill.source` = `model`.
- Dash0 also holds an `execute_tool` span for `bash` whose
  `dash0.gen_ai.tool.bash.command_family` is `echo`, proving the fixture's body ran and not only its
  name being logged.
- The turn's `chat` span carries **neither** skill attribute. This is the assertion that stops a
  Claude or Codex spec being carried over wrongly.
- Both tool spans are parented on the turn's `chat` span.
- `qa-compare.py` exits `0`.

## Tolerance

**The model may do the work itself.** Asked to emit a marker, a model that decides to run `echo`
directly produces a `bash` span and no `skill` span. That is not a finding — the run simply did not
exercise the spec. Check the OTel file for an `execute_tool skill` span before drawing any
conclusion, and reword the prompt rather than reporting a defect.

**The order of the two tool spans is not fixed** and nothing here asserts it.

**Installing a skill changes the session's context.** Copilot lists every available skill in
`github.copilot.context.skills` on its own `invoke_agent` span, which is why `QA_COPILOT_SKILL=1` is
opt-in: most runs are not measuring a session that carries a skill catalogue. The plugin does not
read that attribute, so it reaches no Dash0 span, but a run comparing token counts against another
run's should not be surprised that the numbers moved.

**`source` is always `model` on this runtime.** There is no person-initiated route into a personal
skill that reaches the plugin, so a spec asserting the other value would be asserting an input that
does not exist.

**Ingest lag: 25 seconds.** See `## Settling` in [../../../setup.md](../../../setup.md).
