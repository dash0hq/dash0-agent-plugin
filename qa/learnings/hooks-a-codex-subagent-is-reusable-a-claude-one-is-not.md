# A Codex SubagentStop ends a task; a Claude one ends the agent

Claude Code spawns an agent, it works, it stops, and that is its whole life. Five
sub-agent runs, one `SubagentStart` and one `SubagentStop` each, and never a tool call
from that agent afterwards.

Codex treats an agent as an addressable thing you send more work to. The same `agent_id`
stops, then spawns another agent, runs tools, and stops again — and **no second
`SubagentStart` is emitted** before the later work. Measured across two runs:

| agent | start | stop | work after the stop | another start? |
| --- | --- | --- | --- | --- |
| Codex | 1 | 2 | spawn, wait, `Bash` | none |
| Claude | 1 | 1 | none | n/a |

The difference is visible in the tool set. Claude has one `Agent` tool. Codex has
`spawn_agent`, `wait_agent`, `followup_task`, `list_agents` and `close_agent`, and it
records the distinction itself: a `SubAgentActivity` item in the rollout is `kind:
"started"` for a spawn and `kind: "interacted"` for an exchange with an agent already
running.

**Why it matters:** anything keyed on "an agent stops once" is right for Claude and wrong
for Codex. `internal/pipeline` consumed the agent's trace-context snapshot at
`SubagentStop`, which dropped every span of the later work — and because one of those
dropped calls was itself a nested spawn, the agent it created lost its parent too. The
same assumption is easy to make when reading a run: a second `SubagentStop` for one agent
is not a duplicate hook, and two `invoke_agent` spans carrying the same `gen_ai.agent.id`
are two completed tasks rather than a double export.

**How to apply:** when a Codex run involves delegation, count `invoke_agent` spans by
*task*, not by agent, and expect `gen_ai.agent.id` to repeat. To provoke the reuse
deliberately, ask for a follow-up rather than a second agent:

```
Spawn a sub-agent named worker that runs the shell command "echo alpha". Wait for it to
finish. Then send that SAME agent a follow-up task to run the shell command "echo beta",
and wait for that too. Then reply with exactly the word delegated.
```

Reading the ordering out of `record/index.jsonl` is the only way to see it after the
fact, exactly as in [[hooks-the-task-tool-returns-before-its-sub-agent-runs]], which is
the Claude-side timing counterpart. Needs
[[deadend-codex-does-not-delegate-without-multi-agent-mode]] to produce an agent at all.
