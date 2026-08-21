---
id: skill-invocation-is-counted-by-either-route
area: skills
status: draft
input: qa/tools/qa-session.sh, two prompts that invoke the same skill by different routes
duration: ~10s each
settling: 10s
cleanup: keep
covers:
  - internal/pipeline/pipeline.go
  - internal/otlp/otlp.go
known_failure: not filed
---

## Given

The plugin as installed, plus the recorder. `QA_ALLOWED_TOOLS="Skill Read Edit Write Bash"` so the
`Skill` tool is permitted.

A skill can be invoked two ways, and a user counting skill usage does not distinguish them:

1. **Explicitly**, by typing the slash command. The person decided.
2. **By the model**, which reads the skill descriptions and calls the `Skill` tool. The model decided.

Both are one invocation of one skill. The count Dash0 reports must include both, or it answers a
different question than the one anybody asks of it.

`writing:unslop` is the skill under test because it is invocable both ways, is fast, and needs no
repository state. Note the plugin: `unslop` ships in `writing` and in `general`, not in
`engineering`.

## When

Two sessions, one per route. Same skill, same input text, so the only difference is the route.

```sh
# Route 1: the person invokes it.
QA_MODEL=haiku QA_ALLOWED_TOOLS="Skill Read Edit Write Bash" qa/tools/qa-session.sh \
  '/writing:unslop Our robust platform leverages cutting-edge technology to seamlessly deliver comprehensive insights.' \
  spec-skill-slash

# Route 2: the model invokes it.
QA_MODEL=haiku QA_ALLOWED_TOOLS="Skill Read Edit Write Bash" qa/tools/qa-session.sh \
  'Please unslop this sentence for me: "Our robust platform leverages cutting-edge technology to seamlessly deliver comprehensive insights." Use the unslop skill.' \
  spec-skill-model
sleep 10
```

Measured on release 0.1.24 with `claude` 2.1.238: route 1 records 5 hook invocations and no tool
hook at all. Route 2 records 7, including a `PreToolUse`/`PostToolUse` pair for `Skill`.

## Expectation

**The expected count is 1 per session, and it comes from the input, not from a record.** This is the
one spec in this suite whose expectation is known by construction: the prompt invokes `writing:unslop`
exactly once, deliberately, and no model decision changes that. Route 2's prompt names the skill, so a
run where the model declines to use it is a discarded run rather than a result.

That matters because the usual expectation source is unavailable here. `qa-compare.py` derives its
expectation from the hook-to-span mapping, and on route 1 there is no tool hook to map, so it agrees
with Dash0 at zero and reports no difference. **The recording and the telemetry agree, and both are
wrong about the thing being measured.** An oracle built only on the hook mapping cannot see this
defect at all.

**Route 2, from the record.** `record/events/*PostToolUse*.json` holds one payload with
`tool_name: Skill` and `tool_input` `{"skill": "writing:unslop", "args": "..."}`.
`ExtractSkillName` reads the `skill` field and `internal/otlp/otlp.go` maps it to
`dash0.gen_ai.tool.skill.name`, so the expectation is one `execute_tool Skill` span carrying
`writing:unslop`.

**Route 1, from the record.** There is no tool payload. The `UserPromptSubmit` payload's `prompt`
field holds the raw text `/writing:unslop Our robust platform...`, and the transcript records a user
entry wrapping it in `<command-name>/writing:unslop</command-name>`. So the skill's identity is
present in a hook the plugin already receives; nothing produces a span from it. The expectation is
still one invocation of `writing:unslop`, recoverable from `prompt` by reading the leading token.

## Oracle

- Channel one, Dash0, per session: `dash0 spans query` filtered to `gen_ai.conversation.id`, reading
  the span names and `dash0.gen_ai.tool.skill.name`.
- Channel two, the record: `record/index.jsonl` for the hook inventory, and the `UserPromptSubmit`
  payload for the route-1 signal.
- `qa-compare.py` is **not** a sufficient oracle for this spec. It exits `0` on route 1. Use it only
  to confirm nothing else about the session broke.

## Then

Route 2, the control arm, currently holds:

- Dash0 has 2 spans: 1 `chat` and 1 `execute_tool Skill`.
- `dash0.gen_ai.tool.skill.name` on that span is `writing:unslop`, the full plugin-qualified name.
- `gen_ai.tool.name` is `Skill`, and the span is a child of the `chat` span for the turn.

Route 1, the failing arm:

- `record/index.jsonl` holds 5 invocations: `SessionStart`, `InstructionsLoaded`,
  `UserPromptSubmit`, `Stop`, `SessionEnd`. No `PreToolUse` and no `PostToolUse`.
- The `UserPromptSubmit` payload's `prompt` begins with `/writing:unslop`.
- Dash0 has 1 span, a `chat`, and **no span anywhere in the session carries
  `dash0.gen_ai.tool.skill.name`**. The invocation is absent.
- A query that counts skill invocations across both sessions returns 1 where the answer is 2.

Once fixed, both arms must give:

- One countable invocation of `writing:unslop` per session, carrying the same plugin-qualified name
  by either route.
- The two routes distinguishable from each other, so "the model chose this skill" stays answerable.
  A new attribute is the natural place; folding route 1 into `execute_tool Skill` would make the
  invocation countable and the decision unattributable.

## Tolerance

**The two arms are one spec because the control arm gives the failing arm its meaning.** Route 2
passing is what proves skill tracking exists and route 1 is a hole in it. Run route 2 first: if it
fails, skill spans are broken generally and route 1 says nothing new.

**The name is plugin-qualified, and that is correct.** `writing:unslop` rather than `unslop`. Two
plugins ship a skill by that name on this machine, so the unqualified form is ambiguous. Assert the
qualified form.

**Route 1's failure is not the plugin ignoring a hook it receives.** Claude Code expands the slash
command before any tool runs: it injects the skill's instructions as attachments and the model
answers directly. So no `Skill` tool call exists to hook, and there is no `SlashCommand` event in the
24 the plugin registers. The recoverable signal is the `UserPromptSubmit` prompt text, which is a
different and weaker source than a tool payload: it is a prefix match on user input, so it would need
to tell a real invocation from a person typing the name of one.

**Model choice on route 2.** The prompt names the skill explicitly to make the run repeatable. A run
where the model answers without calling `Skill` is discarded, not recorded as a finding. Whether the
model picks a skill unprompted is a separate question and a separate spec.

**`known_failure` has no ticket id.** It reads `not filed`. The finding is in
[../../findings/slash-invoked-skills-produce-no-telemetry.md](../../findings/slash-invoked-skills-produce-no-telemetry.md).

**Ingest lag.** A few seconds, as everywhere else in this suite.
