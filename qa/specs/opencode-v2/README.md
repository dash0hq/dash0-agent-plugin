# opencode-v2

What an OpenCode V2 session looks like in Dash0 once it ends. One area per runtime, because a run is
one driver, one credential and one cost profile — `## Runtimes` in [../../setup.md](../../setup.md)
is the table, and [../claude](../claude/README.md), [../codex](../codex/README.md),
[../copilot](../copilot/README.md) and [../cursor](../cursor/README.md) are the other four.

| Topic | Covers |
| --- | --- |
| [session](session/README.md) | One turn: the span set, privacy in both modes, the failure path, and the attribute surface |
| [turns](turns/README.md) | Two turns in one session (one trace and one token count per turn, `QA_OPENCODE_V2_RESUME`), and a reload mid-turn (`QA_OPENCODE_V2_RELOAD_ON`) |
| [subagents](subagents/README.md) | Delegation: the child session's `invoke_agent` span, its anchor, and whose tokens are whose |

Each topic keeps its own coverage map.

## The two things to know before reading any spec here

**There is no hook recording.** The plugin subscribes to the server's V2 event stream, so there is
no per-event payload to capture and no `hooks` column. The independent record is OpenCode's own
session store, read after the run with `opencode session export`. A span missing because its event
never reached the plugin and one the pipeline dropped therefore look the same from outside;
`plugin-debug.log` still tells "never built" apart from "built and lost".

**Every spec here runs through `qa/tools/qa-session-opencode-v2.sh`**, which keeps a private
`opencode serve` running for the whole session, because a resumed turn and the session export need
the server the turns ran on. `opencode run --standalone` is covered by
`TestE2EOpenCodeV2SurvivesShutdownSignal` in `test/e2e/`, not by a spec.

## What no spec here can cover

**The release bootstrap.** There is no OpenCode release asset yet, so every run uses the working-tree
exporter through the `executable` option. `opencode-v2-on-event.sh` downloading and verifying a binary
is covered by `test/e2e/` and `test/contracts/release.sh`, not here.

**Every reload timing.** `opencode reload --server` reloads on cue, and
[reload-mid-turn-keeps-the-turn](turns/reload-mid-turn-keeps-the-turn.md) covers one during a tool
call. A reload that lands between two events of a busy stream can still lose events (the documented
limit of a stream-only API); `test/e2e/opencode_v2_e2e_test.go` and the unit tests cover the state
handoff itself.
