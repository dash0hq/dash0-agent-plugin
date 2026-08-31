# copilot

What a GitHub Copilot CLI session looks like in Dash0 once it ends. One area per runtime, because a
run is one driver, one credential and one cost profile — `## Runtimes` in
[../../setup.md](../../setup.md) is the table, and [../claude](../claude/README.md) and
[../codex](../codex/README.md) are the other two.

| Topic | Covers |
| --- | --- |
| [session](session/README.md) | One turn: where the numbers come from, where the tool spans come from, and the attribute surface |
| [turns](turns/README.md) | Two turns in one session: per-turn scoping, which is the whole job of the OTel cursor. Needs `QA_COPILOT_RESUME` |
| [subagents](subagents/README.md) | Delegation through the `task` tool: the anchor, the nesting, and the sessions that must mint nothing |
| [skills](skills/README.md) | A loaded skill and which route chose it. Needs `QA_COPILOT_SKILL=1` |

Each topic keeps its own coverage map, and each records what is deliberately not written and why.

## The one thing to know before reading any spec here

**Copilot's hooks carry no numbers, and no tool events the plugin consumes.** The plugin drives the
session lifecycle from four camelCase hooks — `sessionStart`, `userPromptSubmitted`, `agentStop`,
`sessionEnd` — and reads everything quantitative out of Copilot's own OpenTelemetry file at each
turn boundary: tokens, model, the response text, and every `execute_tool` span with its real
start and end times.

That inverts the evidence the other two runtimes rest on. There, the hook recording is the whole
input and alone implies every span. Here it implies one `chat` span per `agentStop` and nothing
else, and the tool and token comparisons run against the OTel file — **which is also the plugin's
input**. So agreement between Dash0 and that file proves the plugin copied faithfully. It does not
prove Copilot measured the session correctly, and no spec here may claim that it does.

Every spec therefore names which of its assertions rest on the independent hook record and which
rest on the shared file. Where an assertion can be made structural rather than numeric — a parent
relationship, a span that must not exist, an attribute that must be absent — prefer that: those are
claims the shared input cannot launder.

## What no spec here can cover

**Whether the Copilot install on this machine is configured correctly.** A run provisions its own
install into a throwaway home through the real marketplace path and never touches `~/.copilot`.
That is what makes the runtime safe and hermetic, and it is also why no spec here says anything
about a developer's or a customer's install.

**Whether Copilot's own numbers are right.** See above. `qa/tools/qa-otel.py` prints Copilot's
per-turn roll-up next to the sum of that turn's `chat` spans, which catches Copilot disagreeing with
itself, and that is the closest this runtime gets.

**Anything about Claude Code or Codex.** A fix verified here is unverified there, and the reverse.
The three runtimes share the pipeline but not the payloads, and Copilot shares less than the other
two share with each other: different event names, different field names, and a second source the
others do not have at all.
