# The hook-to-span mapping cannot detect missing telemetry for work that fires no tool hook

`qa-compare.py` builds its expectation from `record/index.jsonl`: each `PostToolUse` implies one
`execute_tool`, each `Stop` one `chat`, each `SubagentStop` one `invoke_agent`. When work happens
without firing a tool hook, there is nothing to map, so the expected count is zero, Dash0's count is
zero, and the run is reported as `All three records agree.`

Measured on a skill invoked by its slash command: 5 hook invocations, no `PreToolUse`, no
`PostToolUse`, one `chat` span, and a clean exit `0`. The same skill invoked by the model fired a
`Skill` tool pair and produced the span. The two sessions differ by one route and the comparison
called both healthy.

**The recording and the telemetry agreed, and both were silent about the thing being measured.** That
is the failure mode this oracle cannot see, and it is not rare: anything Claude Code expands or
handles before a tool runs looks identical to it never having happened.

**Why it matters:** a green `qa-compare.py` is easy to read as "the telemetry is complete". It means
something narrower: the spans match the hooks the plugin was fed. A whole class of gap sits outside
that, and the more confident the exit code, the less likely anyone looks further.

**How to apply:** when a spec is about whether something is observable at all, the expectation must
come from what you did, known by construction, not from the record. Do the thing exactly once,
deliberately, and assert one. `claude -p` accepts a slash command as its prompt, so a deliberate
invocation is drivable headlessly and the count is not in doubt. Use `qa-compare.py` alongside it only
to confirm nothing else about the session broke. Related:
[[hooks-the-task-tool-returns-before-its-sub-agent-runs]], where the mapping does see the gap because
the hook fires and the span does not.
