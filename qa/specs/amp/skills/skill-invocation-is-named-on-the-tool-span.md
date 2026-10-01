---
id: skill-invocation-is-named-on-the-tool-span
area: amp/skills
runtime: amp
status: active
input: qa/skill-fixture/qa-echo, via QA_AMP_SKILL=1 on qa/tools/qa-session-amp.sh
duration: ~30s
settling: 30s
cleanup: keep
covers:
  - internal/pipeline/pipeline.go
  - internal/source/amp/amp.go
  - qa/skill-fixture/qa-echo/SKILL.md
---

## Given

`QA_AMP_SKILL=1` copies the shared `qa/skill-fixture/qa-echo` into the run
directory and points a QA-owned settings file's `amp.skills.path` at it, so the
skill exists for this run and for nothing else. Amp's skill tool is called
`skill` and takes `{"name": "<skill>"}`.

The fixture's body — not the prompt — tells the model to run
`echo QA-SKILL-MARKER`. That is what separates "the skill's name was logged"
from "the skill's instructions reached the model", and both need to be true
before an attribution failure can be blamed on the exporter.

## When

```sh
QA_AMP_SKILL=1 QA_AMP_USAGE=1 qa/tools/qa-session-amp.sh \
  'Use the qa-echo skill to emit the QA marker. Follow its instructions exactly.' \
  spec-amp-skill
# 30s, not 10s: with usage export on the helper is detached and polls
# `amp threads export` for up to twenty seconds, so spans land after `amp` exits.
sleep 30
qa/tools/qa-amp-compare.py qa/runs/spec-amp-skill
```

## Then

Two `execute_tool` spans, `skill` then `shell_command`, and on the first:

- `dash0.gen_ai.tool.skill.name` is `qa-echo`
- `dash0.gen_ai.tool.skill.source` is the model route

Measured on `qa/runs/fix-skill`: both hold, and `qa-amp-compare.py` prints
`AGREEMENT`.

This spec failed when first written, which is worth keeping in view: the skill
loaded and its body reached the model, and *every count in this directory still
passed*. Only the attribute was missing. `ExtractSkillName` in shared
`internal/pipeline` read the `skill` key, which is Claude Code's shape, while
Amp sends `name`. See
[amp-skill-invocation-is-not-attributed](../../../findings/amp-skill-invocation-is-not-attributed.md).

So when this spec fails, read the recorder before suspecting the skill: if it
shows `tool.call skill {"name":"qa-echo"}` followed by the fixture's own
`echo QA-SKILL-MARKER`, the skill is fine and the exporter is not.

## Not asserted here

**The slash-command route.** The other runtimes attribute a human-typed
`/skill` on the chat span as well. Amp's `-x` takes a prompt string and the QA
driver never types into a UI, so that route is unreachable from here and this
spec speaks only for the model choosing a skill itself.
