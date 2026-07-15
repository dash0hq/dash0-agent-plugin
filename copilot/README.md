# Dash0 for GitHub Copilot CLI

OpenTelemetry observability for [GitHub Copilot CLI](https://github.com/github/copilot-cli) sessions, emitted to [Dash0](https://dash0.com) as **canonical per-turn spans** — the same span shape as the Claude Code and Cursor runtimes (tool spans, chat spans with per-turn token/cost/model usage). No backend correlation required.

## How it works

Copilot CLI ships its own native OpenTelemetry, which carries the full token/cost/model detail. This plugin uses that as a **local file** token source rather than rebuilding it:

```
launch function ─ enables native OTel → per-session file (local only)
        │
   copilot hooks (camelCase) ─▶ copilot-on-event ─▶ internal/pipeline ─▶ Dash0
        sessionStart / userPromptSubmitted / postToolUse / agentStop / sessionEnd
        at each agentStop: read the file for this turn's chat spans → attach
        gen_ai.usage.* + cost + model → emit a canonical chat span
```

The hooks build the canonical span tree; the native-OTel file supplies per-turn tokens. Dash0 sees one uniform schema across all three runtimes.

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

**Per-turn token/cost/model requires the launch function.** A `copilot` started from a shell without it still emits canonical spans — just without usage data (graceful).

## Team rollout

Enable for a repo's contributors via `.github/copilot/settings.json`:
```json
{ "enabledPlugins": ["dash0-agent-plugin"] }
```
Distribute the launch shell function through your team's shell provisioning (dotfiles, devcontainer).

## Notes

- **Prompt mode** (`copilot -p`) fires the hooks for user-installed plugins, so headless runs are instrumented (when launched via the function).
- **Updating**: after a version bump, `copilot plugin update dash0-agent-plugin`.
- Telemetry is fail-open: a broken exporter, missing config, or absent OTel file never breaks your Copilot session.
