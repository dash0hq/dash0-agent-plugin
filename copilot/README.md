# Dash0 for GitHub Copilot CLI

OpenTelemetry observability for [GitHub Copilot CLI](https://github.com/github/copilot-cli) sessions, emitted to [Dash0](https://dash0.com) as **canonical per-turn spans**.

## How it works

Copilot CLI ships its own native OpenTelemetry, which carries the full token/cost/model and tool-execution detail. This plugin uses that as a **local file** source rather than rebuilding it:

```
launch function ─ enables native OTel → per-session file (local only)
        │
   copilot hooks ─▶ copilot-on-event ─▶ internal/pipeline ─▶ Dash0
        sessionStart / userPromptSubmitted / agentStop / sessionEnd
        at each agentStop: read the file for this turn
```

The hooks drive the session/turn lifecycle; the native-OTel file supplies everything quantitative — including tool spans, which hooks can't provide with real timings (and never fire inside sub-agents at all).

## Install

```bash
copilot plugin install dash0hq/dash0-agent-plugin:copilot
```

> Use the `:copilot` subpath. A bare `copilot plugin install dash0hq/dash0-agent-plugin`
> loads only the Claude Code skills/commands (the root manifest declares no
> hooks) — no telemetry.

Restart `copilot` after installing (hooks load at startup).

## Configure

```
/dash0-configure
```
This writes your Dash0 credentials to `~/.copilot/dash0-agent-plugin.local.md` **and** installs a launch shell function that shadows `copilot` to enable native OTel into a per-session file. Open a new shell afterward.

**The native-OTel file (enabled by the launch function) is the source of per-turn usage, the agent response, and all tool spans.** A `copilot` started from a shell without it still emits a chat span per turn — just without usage, response, or tool detail (graceful).

## Notes

- **Prompt mode** (`copilot -p`) fires the hooks for user-installed plugins, so headless runs are instrumented (when launched via the function).
- **Updating**: after a version bump, `copilot plugin update dash0-agent-plugin`.
- Telemetry is fail-open: a broken exporter, missing config, or absent OTel file never breaks your Copilot session.
