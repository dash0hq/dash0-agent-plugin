# copilot-app

What a GitHub Copilot **app** session looks like in Dash0. The app is not the CLI. It loads the
plugin as a session extension (`copilot-app/extension.mjs`), not as hooks, and its spans come from
the session's own event stream rather than a native-OTel file. A spec written for one says nothing
about the other. [../copilot](../copilot/README.md) is the CLI.

| Topic | Covers |
| --- | --- |
| [subagents](subagents/README.md) | The first turn of a new session, which the extension joins late: the span tree, the sub-agent's own model and tokens |
| [turns](turns/README.md) | Where turns start and end: failed turns, steering and queued messages, later turns, exact token counts |
| [session](session/README.md) | The attribute surface, and what `omit_io` keeps out of it |
| [mcp](mcp/README.md) | An MCP call, through the app's built-in `github-mcp-server` |
| [skills](skills/README.md) | A skill call, through the `qa-echo` fixture |

Each topic keeps its own coverage map, and each records what is deliberately not written and why.

## The driver is a runner session in the app

Every spec here runs autonomously, from a **runner**: an agent session in the Copilot app on this
repository, executing `/qa-run`. A person creates the runner and nothing else. The runner drives
**target** sessions with the app's own session tools and runs the harness with its shell tool. A
session made by hand cannot be used: the app gives it a worktree only at its first message, which
is too late to configure it.

Each run follows the same steps. A spec names only what differs.

1. **Fake model, only when the spec says so.** Start it in the background:
   `qa/tools/qa-fake-model.py serve --port 8765 --log qa/runs/<run-id>/fake-model.jsonl --mode <mode> …`.
   Stop it when the run is collected. Runs that use it go one at a time.
2. **Create the target:** `create_session` with `workspace_type: "worktree"` and
   `notify_on_idle: "once"`, then `get_session` for its worktree path and session id. Its worktree
   exists now, before any message.
3. **Prepare it:** `qa/tools/qa-app-run.py prepare <run-id> <worktree> [--omit-io]`. This writes
   the QA config into that worktree.
4. **Prompt it:** `send_session_message` with the spec's text. Use `delivery_mode: "immediate"` to
   steer a busy session and `"enqueue"` to queue behind it. Both were measured on 2026-10-06 to
   arrive as `delivery: "steering"` and `"queued"`.
5. **Wait** for the idle notification, then the 25-second settling time.
6. **Collect:** `qa/tools/qa-app-run.py collect <run-id> --session-id <id>`, then
   `qa/tools/qa-attrs.py qa/runs/<run-id>`.

**A spec that uses the fake model first checks that the target ran on it.** `fake-model.jsonl` must
hold at least one call made after `swap-in`. If it holds none, the kickoff's model did not take.
That is a setup failure, not a spec result: report it and stop.

**A fake-model run replaces steps 2 to 4.** A session's model can only be set by a kickoff when it
is created, and a kickoff sends its prompt at once, before `prepare` can run:

- `qa/tools/qa-app-run.py swap-in <run-id> [--omit-io]` puts the run's config in place of the
  user's `~/.copilot` one and prints the `<provider-id>/qa-fake` model id;
- `create_session` with `kickoff: {prompt: <the spec's first prompt>, model: <that id>, mode: "interactive"}`;
- `get_session`, then `prepare <run-id> <worktree> [--omit-io]`, which also puts the user's config
  back. Do this straight away: until then every session on the machine reports to the QA target.

If anything fails between `swap-in` and `prepare`, run `qa/tools/qa-app-run.py restore` before
anything else. Later prompts go through `send_session_message` as usual. After a fake-model run, the app
offers `qa-fake` to every new session, so run the real-model specs first and the fake-model ones
last. Then give the picker back: one more `create_session` with a kickoff `Reply with ok.` on the
runner's own model (from `get_session` on the runner), and `archive_session` it once idle.

The fake model needs a one-time provider in the app's settings. See the fake model under
`### GitHub Copilot app` in [../../setup.md](../../setup.md).

## The one thing to know before reading any spec here

**The independent records are the session's `events.jsonl` and, for fake-model runs,
`fake-model.jsonl`.** The app writes `events.jsonl` to `~/.copilot/session-state/<sessionId>/`. It
is not the plugin's input, because the extension receives the live stream. So agreement between it
and Dash0 is a real cross-check of structure: turns, tool calls, sub-agents, their parents and
their models, `abort` and `session.error`. It carries no token count, because `assistant.usage` is
ephemeral. The fake model's log is written by the server the session talked to, so it is the one
record of tokens outside the plugin. A spec asserts an exact token count only when the fake model
served the turn.

**Dash0 masks message content on read.** Any assertion about what a prompt, response or tool
payload contained, or did not, reads the debug log.

## What no spec here can cover

**A turn the user stops, and a session reopened after the app restarts.** No tool an agent has can
press Stop or quit the app it runs in. The extension tests in
`test/consistency/copilot_app_extension_test.go` cover both: `failureReachesTheBinary` for a
cancelled run, and `resumedSessionReplaysNothing` for a reopened one.

**Whether a real model's numbers are right.** Only the fake model's own log gives an independent
token count.

**Anything about the Copilot CLI.** The two share the configuration file and the span emitters, and
nothing else.

**The install from a URL.** `install_extension` can only be judged end to end once a release ships
the `copilot-app-on-event` binary (0.1.29).
