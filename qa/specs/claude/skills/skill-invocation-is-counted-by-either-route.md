---
id: skill-invocation-is-counted-by-either-route
area: claude/skills
runtime: claude
status: draft
input: qa/tools/qa-session.sh, two prompts that invoke the same skill by different routes
duration: ~10s each
settling: 10s
cleanup: keep
covers:
  - internal/pipeline/pipeline.go
  - internal/transcript/transcript.go
  - internal/otlp/otlp.go
---

## Given

The plugin as installed, plus the recorder. `QA_ALLOWED_TOOLS="Skill Read Edit Write Bash"` so the
`Skill` tool is permitted.

A skill can be invoked two ways, and a user counting skill usage does not distinguish them:

1. **Explicitly**, by typing the slash command. The person decided.
2. **By the model**, which reads the skill descriptions and calls the `Skill` tool. The model decided.

Both are one invocation of one skill. The count Dash0 reports must include both, or it answers a
different question than the one anybody asks of it. Route 1 produced nothing at all until the fix, so
every count was the model's choices only.

`writing:unslop` is the skill under test because it is invocable both ways, is fast, and needs no
repository state. Note the plugin: `unslop` ships in `writing` and in `general`, not in
`engineering`.

## When

Two sessions, one per route. Same skill, same input text, so the only difference is the route.

```sh
# Route 1: the person invokes it.
QA_SWAP_BINARY=1 QA_MODEL=haiku QA_ALLOWED_TOOLS="Skill Read Edit Write Bash" qa/tools/qa-session.sh \
  '/writing:unslop Our robust platform leverages cutting-edge technology to seamlessly deliver comprehensive insights.' \
  fix-skill-slash

# Route 2: the model invokes it.
QA_SWAP_BINARY=1 QA_MODEL=haiku QA_ALLOWED_TOOLS="Skill Read Edit Write Bash" qa/tools/qa-session.sh \
  'unslop this sentence for me, use the unslop skill: Our robust platform leverages cutting-edge technology to seamlessly deliver comprehensive insights.' \
  fix-skill-model
sleep 10
```

Route 1 records 5 hook invocations and no tool hook at all. Route 2 records 7, including a
`PreToolUse`/`PostToolUse` pair for `Skill`. Measured with `claude` 2.1.238.

## Expectation

**The expected count is 1 per session, and it comes from the input, not from a record.** This is the
one spec in this suite whose expectation is known by construction: the prompt invokes `writing:unslop`
exactly once, deliberately, and no model decision changes that. Route 2's prompt names the skill, so a
run where the model declines to use it is a discarded run rather than a result.

That matters because the usual expectation source is unavailable here. `qa-compare.py` derives its
expectation from the hook-to-span mapping, and on route 1 there is no tool hook to map, so it agrees
with Dash0 whatever Dash0 says. See
[the learning on that blind spot](../../../learnings/oracle-the-hook-mapping-is-blind-to-work-that-fires-no-tool-hook.md).

**Route 2, from the record.** `record/events/*PostToolUse*.json` holds one payload with
`tool_name: Skill` and `tool_input` `{"skill": "writing:unslop", "args": "..."}`.
`ExtractSkillName` reads the `skill` field, so the expectation is one `execute_tool Skill` span
carrying `writing:unslop` and `source` `model`.

**Route 1, from the record and the transcript.** There is no tool payload. The `UserPromptSubmit`
payload's `prompt` field holds the raw text `/writing:unslop Our robust platform...`, and the
transcript records the expansion in two parts: a user entry wrapping the command as
`<command-name>/writing:unslop</command-name>`, and an `isMeta` entry whose text opens
`Base directory for this skill: …/skills/unslop`. `transcript.ReadTurnSkillCommand` requires both, and
requires the command's last colon-separated segment to match the directory's name. The expectation is
one invocation of `writing:unslop` on the turn's `chat` span, with `source` `command`.

## Oracle

- Channel one, Dash0, per session: `dash0 spans query` filtered to `gen_ai.conversation.id`, reading
  the span names, `dash0.gen_ai.tool.skill.name`, and `dash0.gen_ai.tool.skill.source`.
- Channel two, the record: `record/index.jsonl` for the hook inventory, plus the `UserPromptSubmit`
  payload and the transcript for the route-1 signal.
- `qa-compare.py` is **not** a sufficient oracle for this spec. It exits `0` on route 1 either way.
  Use it only to confirm nothing else about the session broke.

## Then

Route 2, the model route:

- Dash0 has 2 spans: 1 `chat` and 1 `execute_tool Skill`, the latter a child of the former.
- `dash0.gen_ai.tool.skill.name` on the tool span is `writing:unslop`, the full plugin-qualified name.
- `dash0.gen_ai.tool.skill.source` is `model`.
- The `chat` span carries **neither** key, so the invocation is counted once and not twice.

Route 1, the command route:

- `record/index.jsonl` holds 5 invocations: `SessionStart`, `InstructionsLoaded`, `UserPromptSubmit`,
  `Stop`, `SessionEnd`. No `PreToolUse` and no `PostToolUse`.
- Dash0 has 1 span, a `chat`, carrying `dash0.gen_ai.tool.skill.name` `writing:unslop` and
  `dash0.gen_ai.tool.skill.source` `command`.
- A query counting spans with `dash0.gen_ai.tool.skill.name` across both sessions returns 2, and
  splitting by `source` returns one of each.

## Tolerance

**The two arms are one spec because the control arm gives the other its meaning.** Route 2 passing is
what proves skill tracking works generally. Run it first: if it fails, route 1 says nothing new.

**The name is plugin-qualified, and that is correct.** `writing:unslop` rather than `unslop`. Two
plugins ship a skill by that name on this machine, so the unqualified form is ambiguous. Assert the
qualified form. A project-level skill invoked as `/tidy` has no prefix to qualify, and the bare name
is then the right value.

**The routes live on different span types, deliberately.** A slash command runs no tool, so there is
nothing to time and nothing to wrap: a zero-duration `execute_tool` span would be a fabrication. The
attribute goes on the turn's `chat` span instead. Counting is by attribute presence, not by span type.

**One command invocation per turn, at most.** A prompt has one leading token, so route 1 cannot
double-count. A turn that starts with a slash command and also has the model call `Skill` reports both,
on different spans, which is two invocations because it is.

**A built-in slash command must not count.** `/compact` and `/plugin` write the same `<command-name>`
tag and load no skill, which is why the skill-instructions entry is required as well. Not asserted
live: `/help` fires no `UserPromptSubmit` and no `Stop` under `claude -p`, so there is no span to
inspect, and `/compact` is not drivable headlessly. Covered by unit tests built from real transcript
shapes.

**Model choice on route 2.** The prompt names the skill explicitly to make the run repeatable. A run
where the model answers without calling `Skill` is discarded, not recorded as a finding. Whether the
model picks a skill unprompted is a separate question and a separate spec.

**Ingest lag.** A few seconds, as everywhere else in this suite.
