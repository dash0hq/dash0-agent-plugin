# Some slash commands fire no UserPromptSubmit and no Stop, so they cannot be probed

`claude -p '/help'` runs and records three hook invocations: `SessionStart`, `InstructionsLoaded`,
`SessionEnd`. No `UserPromptSubmit`, no `Stop`, no span. Claude Code answers it entirely client-side,
so there is no turn for the plugin to see.

A skill invoked the same way is the opposite: `/writing:unslop <text>` opens a real turn and produces
a `chat` span, because the expansion feeds the model.

**Why it matters:** the two look identical when you type them, so it is easy to design a probe around
the wrong one. A negative control for "a built-in command must not be counted as a skill invocation"
cannot be built from `/help`: there is no span to inspect, so the run proves nothing either way rather
than proving absence. `/compact` does open a turn, but it is not drivable through `claude -p` at all.

**How to apply:** before building a spec around a slash command, run it once and read
`record/index.jsonl`. If there is no `Stop`, there is no span, and the question has to be answered
somewhere other than a live run — a unit test over a real transcript shape is the honest substitute,
and the transcript entries are worth copying out of a session that did it interactively. Related:
[[oracle-the-hook-mapping-is-blind-to-work-that-fires-no-tool-hook]].
