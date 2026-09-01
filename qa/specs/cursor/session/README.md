# cursor / session

One Cursor turn, driven through the TUI on a pty. The span set it produces, where each span comes
from, and what a span must not carry.

Read `## The two things to know before reading any spec here` in [../README.md](../README.md) first.
Both of them bite in this area: print mode produces no turn at all, and no token count here has a
second reading behind it.

| Spec | Asserts |
| --- | --- |
| [single-turn-agrees-with-the-hooks-that-fed-it](single-turn-agrees-with-the-hooks-that-fed-it.md) | The whole method on the smallest session: one `chat`, one `execute_tool`, parented, and the recorder implies both |
| [tool-failure-sets-the-span-status](tool-failure-sets-the-span-status.md) | A shell command that exits non-zero produces `postToolUseFailure`, an ERROR span status and `exception.message` |
| [cursor-span-carries-no-undeclared-attribute](cursor-span-carries-no-undeclared-attribute.md) | Every key Dash0 stored is in `DEVELOPMENT.md`, which is the check that found five raw payload leaks |

## Deliberately not written

**"The model name is correct."** Cursor reports `model: "default"` whenever the picker is on Auto,
which is the default, and `internal/source/cursor` rewrites that to `cursor-auto`. The real model is
chosen per request by Cursor's backend and appears nowhere — not in the payload, not in the
transcript, not in any readable on-disk state. So the only assertable claim is the rewrite itself,
which `single-turn-agrees-with-the-hooks-that-fed-it` makes. Pinning `QA_MODEL` would assert a
different thing on a different code path and tell you nothing about the common case.

**"A `preToolUse` produces no span."** True, and it holds by construction:
`internal/pipeline.Process` acts on `PostToolUse` and `PostToolUseFailure` only. Every spec here
counts spans against `postToolUse` rows, so a span minted at `preToolUse` would fail all of them.
A spec of its own would restate the arithmetic.

**"A `SessionEnd` with an open trace context closes the turn."** `internal/pipeline` does emit a
`chat` span on `SessionEnd` when a context is still open, which is how an interrupted session gets a
turn. Reaching it needs a session killed mid-turn, and `qa-cursor-drive.py` deliberately ends with
Ctrl-D so `sessionEnd` arrives cleanly after `afterAgentResponse` has already closed the turn. A
spec for the interrupted path needs a driver knob that does not exist yet.

**Anything about the wire format.** A cursor run reads spans back out of Dash0 and the plugin's own
debug log. The bytes on the wire belong to `test/e2e/`, which owns them against a mock.
