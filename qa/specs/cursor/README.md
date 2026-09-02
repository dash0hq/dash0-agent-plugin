# cursor

What a Cursor session looks like in Dash0 once it ends. One area per runtime, because a run is one
driver, one credential and one cost profile — `## Runtimes` in [../../setup.md](../../setup.md) is
the table, and [../claude](../claude/README.md), [../codex](../codex/README.md) and
[../copilot](../copilot/README.md) are the other three.

| Topic | Covers |
| --- | --- |
| [session](session/README.md) | One turn: the span set, the tool spans, the failure path, and the attribute surface |
| [turns](turns/README.md) | Two turns in one session: per-turn scoping. Needs `QA_CURSOR_RESUME` |
| [subagents](subagents/README.md) | Delegation: what Cursor gives a hook, and why none of the sub-agent's work reaches a span |
| [mcp](mcp/README.md) | An MCP call: the name, and the placeholder server. Needs `QA_CURSOR_MCP=1` |

Each topic keeps its own coverage map, and each records what is deliberately not written and why.

## The two things to know before reading any spec here

**A cursor run is driven through a terminal, and it has to be.** `cursor-agent -p` is the headless
mode and it fires neither `beforeSubmitPrompt` nor `afterAgentResponse` — measured 2026-09-01
against cursor-agent 2026.08.31. `afterAgentResponse` is the only event carrying token usage, and
`internal/source/cursor` renames it to `Stop`, which is the single event
`internal/pipeline.Process` turns into a `chat` span. So a print-mode session produces tool spans
and no turn at all: no `chat` span, no model, no tokens. `qa/tools/qa-cursor-drive.py` therefore
drives the interactive TUI on a pty and types into it, and it reads turn completion out of the
recorder's index rather than off the screen.

**There is no independent reading of a token count on this runtime.** Cursor exposes usage in
exactly one place, the `afterAgentResponse` payload, and that payload is the plugin's own input. The
agent transcript, which is Cursor's own file and which the plugin never reads, carries no token
count of any kind. This is weaker than Claude, where the transcript is a genuine second
measurement, and weaker than Copilot, whose OTel file at least comes from the host's own
instrumentation. **No spec here may assert that a token count is correct.** What a spec can assert
is that it is scoped correctly — that turn 2 is not turn 1 plus turn 2 — and that is arithmetic over
the same input rather than a second reading of it.

What the transcript *is* good for is turns and structure. It records one `<user_query>` entry per
submitted prompt, and its tool blocks reveal work the spans are missing — which is how the
sub-agent gap in [subagents](subagents/README.md) is visible at all.

## What no spec here can cover

**Whether Cursor's own numbers are right.** See above.

**The tool count, from the transcript.** Measured 2026-09-01 on `qa/runs/probe-cursor-mcp`: the
transcript held 15 `tool_use` blocks where the hooks and Dash0 both held 11, and every one of the
four was accounted for. Cursor names some tools differently there than in its hooks — `Glob` and
`Grep` in the transcript are both `Grep` to a hook — and it records internal plumbing that fires no
hook at all: `GetDynamicTools` and `CallDynamicTool` carried the MCP call, which reached the hooks
once, as `MCP:echo_text`. So the transcript's tool count is a superset in another vocabulary.
`qa-compare.py` prints it and never compares it.

**Which MCP server a call belongs to.** Cursor exposes an MCP call through the generic
`preToolUse`/`postToolUse` pair as `MCP:<tool>` and drops the specialized hooks that carry the
server name, so `internal/source/cursor` tags `dash0.gen_ai.tool.mcp_server` with the literal
placeholder `cursor`. A two-server run could not tell its two servers apart, so the fixture
registers one.

**A skill invocation.** Cursor reads skills from `~/.cursor/skills-cursor/`, at user scope only, and
this runtime uses the developer's real `HOME` — the login is not portable. Installing a fixture skill
there would change the machine, which every driver here refuses to do. `DEVELOPMENT.md` scopes the
chat-span skill route to Claude and Codex anyway.

**Anything about Claude Code, Codex or Copilot.** A fix verified here is unverified there, and the
reverse.
