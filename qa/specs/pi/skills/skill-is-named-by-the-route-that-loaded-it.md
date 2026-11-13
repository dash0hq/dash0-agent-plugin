---
id: skill-is-named-by-the-route-that-loaded-it
area: pi/skills
runtime: pi
status: active
input: qa/tools/qa-session-pi.sh with QA_PI_SKILL=1, two runs
duration: ~30s
settling: none; qa-pi-compare.py polls
cleanup: keep
covers:
  - pi/index.ts
  - internal/source/pi/pi.go
---

## Given

The Pi extension and helper **from this working tree**, loaded by
`qa/tools/qa-session-pi.sh` with `--no-extensions -e pi/index.ts` and a helper
built into the run directory. No installed copy is read. The plugin exports to
`piDataset` in `qa/config.local.json` through `PI_PLUGIN_OPTION_*`. See
`### Pi` under `## Runtimes` in [setup.md](../../../setup.md).

## When

```sh
QA_PI_SKILL=1 qa/tools/qa-session-pi.sh \
  'Use the qa-echo skill to emit the QA marker. Follow its instructions exactly.' \
  spec-pi-skill-model
QA_PI_SKILL=1 qa/tools/qa-session-pi.sh '/skill:qa-echo' spec-pi-skill-command
qa/tools/qa-pi-compare.py qa/runs/spec-pi-skill-model
qa/tools/qa-pi-compare.py qa/runs/spec-pi-skill-command
```

## Expectation

Pi has no skill tool. In the model run, `stream.jsonl` holds a
`tool_execution_start` for `read` whose `path` is the fixture's `SKILL.md`,
then `bash` with `echo QA-SKILL-MARKER`. In the command run, Pi expands
`/skill:qa-echo` into the prompt before the turn starts, so no `read` of
`SKILL.md` occurs. `record.jsonl` shows the expanded `<skill name="qa-echo"`
prompt in `before_agent_start`.

## Oracle

- Channel one: Dash0, `dash0 spans query` filtered by `gen_ai.conversation.id`
  in `piDataset`, through `qa-pi-compare.py`.
- Channel two: Pi's own `--mode json` stream, `stream.jsonl` in the run.

## Then

- Model run: the `execute_tool read` span carries
  `dash0.gen_ai.tool.skill.name=qa-echo` and
  `dash0.gen_ai.tool.skill.source=model`. No other span carries a skill name.
- Command run: the root `chat` span carries `qa-echo` and `command`. No tool
  span carries a skill name.

## Tolerance

Exact. In the model run, a model that runs the marker without reading the
skill is a failed run. Run it again. Do not read the absent attribute as a
defect.

## Verified

2026-11-13 on `qa/runs/setup-probe-pi-skill` (model) and `qa/runs/setup-probe-pi-skill-command` (command): attributes as above, both `AGREEMENT`.
