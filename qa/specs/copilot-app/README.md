# copilot-app

What a GitHub Copilot **app** session looks like in Dash0. The app is not the CLI. It loads the
plugin as a session extension (`copilot-app/extension.mjs`), not as hooks, and its spans come from
the session's own event stream rather than a native-OTel file. A spec written for one says nothing
about the other. [../copilot](../copilot/README.md) is the CLI.

| Topic | Covers |
| --- | --- |
| [subagents](subagents/first-turn-is-traced-with-its-subagent.md) | The first turn of a new session, which runs a tool and delegates to a sub-agent: the span tree, the sub-agent's own model, and the turn the extension joined late |

## The one thing to know before reading any spec here

**There is no driver yet.** The app has no headless mode that a script can launch, so a run is a
real session in the app, prompted by hand or by another session through `send_session_message`.
That is why every spec here is `draft`. The install can still be hermetic: copy the extension into
one worktree as `.github/extensions/dash0-agent-plugin/`, and give that worktree a
`.copilot/dash0-agent-plugin.local.md` pointing at the target. A run set up that way touches no
other session and no file in `~/.copilot`.

**The independent record is the session's `events.jsonl`.** The app writes every persisted event to
`~/.copilot/session-state/<sessionId>/events.jsonl`. That file is not the plugin's input, because
the extension receives the live stream. Agreement between it and Dash0 is therefore a real
cross-check of structure: tool calls, sub-agents, their parents and their models. It is **not** a
cross-check of tokens, because `assistant.usage` is ephemeral and never reaches the file. Token
counts can only be read back from the plugin's debug log, which is the plugin's own output.

## What no spec here can cover

**Whether the app's own numbers are right.** See above. No record outside the plugin carries tokens.

**Anything about the Copilot CLI.** The two share the configuration file and the span emitters, and
nothing else.
