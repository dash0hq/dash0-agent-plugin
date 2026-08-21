---
id: slash-invoked-skills-produce-no-telemetry
severity: high
status: unaddressed
filed: no
found: 2026-08-21
affects: claude-on-event 0.1.24, all releases
spec: qa/specs/skills/skill-invocation-is-counted-by-either-route.md
---

# A skill invoked by its slash command produces no telemetry at all

Typing `/writing:unslop ...` runs the skill and emits nothing. No tool span, no
`dash0.gen_ai.tool.skill.name`, no record that a skill ran. The same skill invoked by the model emits
a correct `execute_tool Skill` span. So skill invocation counts in Dash0 are the model's choices
only, and every deliberate invocation is missing.

## What happens

Claude Code expands a slash command before any tool runs. The transcript shows a user entry wrapping
the command as `<command-name>/writing:unslop</command-name>`, the skill's instructions arrive as
attachments, and the model answers directly. No `Skill` tool call is ever made, so no `PreToolUse` or
`PostToolUse` fires, and `sendToolTrace` is never reached. There is no `SlashCommand` event among the
24 the plugin registers, and Claude Code does not offer one.

Measured, same skill and same input text both ways:

| Route | Hook invocations | Tool hooks | Spans | `dash0.gen_ai.tool.skill.name` |
| --- | --- | --- | --- | --- |
| `/writing:unslop <text>` | 5 | none | 1 `chat` | absent |
| "unslop this ... use the unslop skill" | 7 | `Skill` pair | 1 `chat`, 1 `execute_tool Skill` | `writing:unslop` |

**The signal is not lost, only unread.** The `UserPromptSubmit` payload the plugin already receives
carries the raw text in its `prompt` field, beginning with `/writing:unslop`. Nothing in the pipeline
looks at it.

## Reproduction

```sh
QA_MODEL=haiku QA_ALLOWED_TOOLS="Skill Read Edit Write Bash" qa/tools/qa-session.sh \
  '/writing:unslop Our robust platform leverages cutting-edge technology to seamlessly deliver comprehensive insights.' \
  finding-skill-slash
sleep 10
```

Then query the session and look for any span carrying `dash0.gen_ai.tool.skill.name`. There is none.
Run the same thing phrased as a request rather than a command to see the control arm pass.

## Why it matters

Deliberate invocation is the common case for the skills people rely on, so the count is not slightly
low, it is measuring a different population. Any conclusion drawn from it about which skills are used
is inverted: a skill that people invoke on purpose looks unused, and a skill the model reaches for
unprompted looks popular.

**No channel reveals it.** `qa-compare.py` derives its expectation from the hook-to-span mapping, and
with no tool hook to map it agrees with Dash0 at zero and reports the session as healthy. The
recording and the telemetry agree, and both are silent about the invocation. Only knowing what was
typed exposes the gap, which is why this went unnoticed.

## Suggested fix

Read the leading token of `UserPromptSubmit`'s `prompt`. When it is a slash command that names a
skill, emit the invocation with the plugin-qualified name, and mark the route so a deliberate
invocation stays distinguishable from a model-chosen one. Folding it into `execute_tool Skill` would
make invocations countable at the cost of making the decision unattributable, which is the more
interesting of the two questions.

Two things to settle in that change:

- A prompt can mention a slash command without invoking one. A prefix match on user input is weaker
  evidence than a tool payload, and it needs a rule.
- A skill invoked by slash command has no duration and no result, because nothing wraps its
  execution. A zero-duration span may be the wrong shape; an event or a counter may fit better.
