# A Copilot skill is named on its tool span, never on the turn's chat span

Claude Code has a `Skill` tool for the model route and expands a slash command for the
person's, and the plugin reads the second out of the transcript. Codex has no skill tool
at all and injects the skill into the conversation, so both of its routes land on the
turn's `chat` span.

Copilot has exactly one route and it is a tool call. The model calls a tool named `skill`,
the native `execute_tool` span carries
`github.copilot.tool.parameters.skill_name`, and the plugin maps it to
`dash0.gen_ai.tool.skill.name` with `dash0.gen_ai.tool.skill.source` of `model` on that
same tool span. The `chat` span carries neither. Measured 2026-08-28 on
`qa/runs/probe-skill`.

**Why it matters:** `DEVELOPMENT.md` describes the chat-span route, correctly scoped to
Claude and Codex. A spec carried over from either of those runtimes looks for the skill
name on the `chat` span, finds nothing, and reports a defect that is not one.

**How to apply:** assert skill attribution on the `execute_tool skill` span. And assert
that the fixture's *body* ran — `qa/skill-fixture/qa-echo` exists to `echo QA-SKILL-MARKER`
for exactly this reason — because a name-only assertion passes even when the instructions
never reached the model.
