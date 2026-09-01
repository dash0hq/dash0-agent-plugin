# cursor / turns

Two turns in one Cursor session. What a single-turn probe cannot see.

| Spec | Asserts |
| --- | --- |
| [resumed-turn-is-scoped-to-itself](resumed-turn-is-scoped-to-itself.md) | Turn 2's `chat` span carries turn 2's usage, not the session's, and turn 1's tool span is not re-emitted |

## Why this is its own area

With one turn, "this turn's usage" and "the session's usage" are the same number, so a double-count
is invisible. Both other multi-turn-capable runtimes shipped a defect exactly there: Codex stopped
resetting at a turn boundary Codex had renamed, and Copilot's OTel cursor lived in a directory the
pipeline deleted at `SessionEnd`. Neither was visible on a single-turn probe.

**Cursor's mechanism is different from both, and simpler.** Usage arrives per turn, in the
`afterAgentResponse` payload, already scoped by the host — there is no cursor to keep and no file to
re-read. So the failure mode the other two had is not reachable here. What is reachable is the trace
context: `internal/pipeline` mints a new trace at `UserPromptSubmit` and clears it at `Stop`, and a
second turn depends on that cycle running twice.

## What a second turn costs on this runtime

Nothing extra in setup. The TUI stays open between turns, so `QA_CURSOR_RESUME` is a second prompt
typed into the session that is already running — no resume flag, no second launch, no shared-file
question. That is a genuine advantage over `codex exec resume --last` and `copilot --resume`, both of
which relaunch the host and brought their own defects with them: a resumed Copilot launch of a
live-loaded plugin fired no hooks at all, which read as a plugin bug for three days.

## Deliberately not written

**"A third turn behaves like the second."** The reset is one cycle; running it three times asserts
the same thing at higher cost.

**"An interrupted turn still produces a span."** `internal/pipeline` emits a `chat` span on
`SessionEnd` when a trace context is still open, and reaching that needs a session ended mid-turn.
`qa-cursor-drive.py` waits for `afterAgentResponse` before it sends Ctrl-D, so no run here can
reach it. A driver knob for it does not exist yet.
