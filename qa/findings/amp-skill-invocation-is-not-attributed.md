# Amp: a skill invocation is not attributed

> **Fixed.** Kept for the root cause, which is a seam worth remembering.

**Runtime:** amp
**Found:** 2026-09-15, by `qa/runs/amp-skill`
**Status:** FIXED 2026-09-15, verified on `qa/runs/fix-skill`
**Affects:** `internal/pipeline/pipeline.go` (`ExtractSkillName`) — **shared code**

## What happens

When the model loads a skill on Amp, the `execute_tool skill` span carries no
`dash0.gen_ai.tool.skill.name` and no `dash0.gen_ai.tool.skill.source`. Every
other runtime names the skill. On Amp the invocation is recorded as an
anonymous tool call, so a count of skill use over an Amp fleet is zero.

## Root cause

`EnrichToolEvent` gates on the tool name and then reads the skill out of the
tool input:

```go
if strings.EqualFold(toolName, "Skill") {
    if skill := ExtractSkillName(toolInput); skill != "" {
        event["skill_name"] = skill
    }
```

The gate matches — Amp's tool is called `skill`, and `EqualFold` covers the
case difference. `ExtractSkillName` is what fails: it reads the `skill` key,

```go
name, _ := val["skill"].(string)
```

and **Amp's skill tool names its argument `name`, not `skill`**. Recorded
verbatim by the QA recorder:

```json
{"event":"tool.call","payload":{"tool":"skill","input":{"name":"qa-echo"}}}
```

So the branch runs, extracts `""`, sets nothing, and `skill_source` is skipped
too because it is gated on `skill_name` being present.

## Measured

`qa/runs/amp-skill` — `QA_AMP_SKILL=1`, the shared `qa/skill-fixture/qa-echo`
mounted through `amp.skills.path`.

| | |
| --- | --- |
| Recorder | `tool.call skill {"name":"qa-echo"}`, then `tool.call shell_command {"command":"echo QA-SKILL-MARKER"}` |
| Skill body reached the model | yes — the marker command is in the fixture's body, not in the prompt |
| `execute_tool skill` span | `gen_ai.tool.name=skill`, `gen_ai.tool.type=function`, **no skill attribute of any kind** |

The second row matters: this is not a skill that failed to load. It loaded, its
instructions reached the model, and the model followed them. Only the
attribution is missing.

## The fix (applied)

`ExtractSkillName` now falls back to `name` when `skill` is absent, via a
shared `skillNameFrom` helper used by both its string and map branches.

The fallback cannot regress the other runtimes — it only fires when `skill` is
missing, Claude Code always sends `skill`, and Copilot never reaches
`ExtractSkillName` at all because its source pre-sets `skill_name`. But it is
`internal/pipeline`, every runtime runs through it, and
`amp-span-carries-no-undeclared-attribute` should be re-run on all five
afterwards rather than on amp alone.

Guarding it on the tool name instead (`name` only when the harness is amp)
would be narrower and uglier; the fallback is preferred unless some runtime
turns out to send a `skill` tool whose `name` argument means something else.

## Why the unit tests do not catch it

`ExtractSkillName`'s tests feed it `{"skill": ...}`, which is Claude Code's
shape. No test feeds it Amp's. The amp source's own tests stop at building the
tool event and never assert what `EnrichToolEvent` makes of it, so the seam
between the two is untested on both sides.

## Verified

`qa/runs/fix-skill`: the `execute_tool skill` span carries
`dash0.gen_ai.tool.skill.name=qa-echo` and
`dash0.gen_ai.tool.skill.source=model`.

**On not regressing the other four runtimes.** `skill` is checked first and
`name` is only a fallback, so any harness sending `skill` keeps its old answer
byte for byte; the gate is also narrow, firing only for a tool named `skill`.
`TestExtractSkillName` pins all of it, including `skill beats name` and
`empty skill falls back to name`. This machine's QA token is scoped to the
amp dataset alone, so the claude, codex, copilot and cursor skill specs could **not**
be re-run here — the unit tests are the evidence, and those four specs should
be re-run wherever their datasets are reachable.
