# Cursor's headless mode fires no beforeSubmitPrompt and no afterAgentResponse

`cursor-agent -p` is the obvious way to drive a Cursor session from a script, and it cannot
produce a turn. Measured 2026-09-01 against cursor-agent 2026.08.31 with all nine plugin
events registered: print mode fired `sessionStart`, `preToolUse`, `postToolUse`,
`afterAgentThought` and `sessionEnd`, and never `beforeSubmitPrompt` or
`afterAgentResponse`. Two runs, same result, one of them a clean success.

`afterAgentResponse` is the only event Cursor puts token usage on, and
`internal/source/cursor` renames it to `Stop`, which is the single event
`internal/pipeline.Process` turns into a `chat` span. `beforeSubmitPrompt` becomes
`UserPromptSubmit`, which is what mints the turn's trace. So a print-mode session produces
`execute_tool` spans with no parent turn: no `chat` span, no model, no tokens.

The interactive TUI fires the full set, including usage. In the CLI bundle both events are
fired from `./src/after-agent-hooks.ts` and from a React component, and print mode reaches
neither.

**Why it matters:** a harness built on `-p` would report every Cursor session as tool spans
with no turn, and read as a plugin that lost the chat span.

**How to apply:** `qa/tools/qa-cursor-drive.py` runs the TUI on a `pty.fork()` and types
into it. Three things that took a measurement each:

- `--trust` is required, or the first paint is a "Do you trust this directory?" dialog and
  nothing else happens.
- The prompt text and its newline must be two writes with a pause between them. Sent in one
  buffer the TUI treats it as a paste and leaves it in the composer.
- **Ctrl-D is the only exit that fires `sessionEnd`.** `/quit` and two Ctrl-Cs both left the
  process running until it was killed, and a killed session delivers no `sessionEnd`, so the
  pipeline never closes it.

Do not scrape the screen for turn completion. The TUI repaints constantly and every form of
that was flaky; read `afterAgentResponse` rows out of the recorder's `index.jsonl` instead,
which is exact and cannot claim a turn finished before the evidence exists.
