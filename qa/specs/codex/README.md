# codex

What an OpenAI Codex session looks like in Dash0 once it ends. One area per runtime, because a run is
one driver, one credential and one cost profile — `## Runtimes` in [../../setup.md](../../setup.md)
is the table, and [../claude](../claude/README.md) is the other half.

| Topic | Covers |
| --- | --- |
| [session](session/README.md) | One session: per-turn usage, the attribute surface, tool-call durations, plan and allowance |
| [subagents](subagents/README.md) | Delegation: the spawn anchor, a reused agent, and whose tokens are whose. Needs `QA_CODEX_MULTI_AGENT=1` |
| [skills](skills/README.md) | A loaded skill, and which route chose it. Needs `QA_CODEX_SKILL=1` |

Each topic keeps its own coverage map, and each records what is deliberately not written and why.

## What no spec here can cover

**Whether the Codex install on this machine is configured correctly.** A run provisions its own
install into a throwaway home and never touches `~/.codex`. That is what makes the runtime safe and
hermetic, and it is also why no spec here says anything about a developer's or a customer's install.

**Anything about Claude Code.** A fix verified here is unverified there, and the reverse. The two
runtimes share the pipeline but not the payloads, and every defect found in this area was invisible
from the other side — including one, agent reuse, where the correct behaviour is the opposite.
