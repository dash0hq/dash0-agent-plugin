# Dash0 Agent Plugin

Connect your coding agent to [Dash0](https://dash0.com) for deep insight into how it's used — prompts and responses, tool calls, MCP calls, sub-agent activity, and token consumption — emitted as OpenTelemetry traces.

Trace through a session, see what each turn cost, find where the agent got stuck, and join agent activity with the systems it touches.

## Setup

Check out our documentation for setting up and configuring the Dash0 Agent Plugin: [Set Up AI SDLC Insights](https://www.dash0.com/docs/dash0/darkplane/insights/setup)

## Supported runtimes

- **Claude Code** — installation, configuration, and usage in [`.claude-plugin/README.md`](./.claude-plugin/README.md).
- **Cursor** — installation, configuration, and usage in [`.cursor-plugin/README.md`](./.cursor-plugin/README.md).
- **OpenAI Codex** — installation, configuration, and usage in [`.codex-plugin/README.md`](./.codex-plugin/README.md).
- **GitHub Copilot CLI** — installation, configuration, and usage in [`.github/plugin/README.md`](./.github/plugin/README.md).
- **OpenCode V2** — installation, configuration, and usage in [`opencode-v2/README.md`](./opencode-v2/README.md).

All runtimes run on macOS, Linux, and Windows, on `amd64` or `arm64`. On Windows, Claude Code also needs [Git for Windows](https://gitforwindows.org/): it runs hook commands through Git Bash, where the other runtimes use a PowerShell bootstrap.

## Repository layout

This repo ships one shared Go pipeline (`cmd/`, `internal/`) and runtime-specific plugin surfaces. The rule: **`<runtime>/` holds everything shipped to that runtime**, including its `<runtime>-on-event.sh` bootstrap wrapper.

| Path | Runtime | Purpose |
|---|---|---|
| `claude/` (`claude-on-event.sh`, `hooks.json`, `commands/`, `skills/`, `tools/`), `.claude-plugin/` | Claude Code | Bootstrap wrapper, hook registration, slash commands, configure skill, diagnostic scripts, manifest |
| `cursor/` (`cursor-on-event.sh`, `hooks.json`, `skills/`), `.cursor-plugin/`, `install-cursor.sh` | Cursor | Bootstrap wrapper, hook registration, configure skill, manifest, installer |
| `codex/` (`codex-on-event.sh`, `hooks.json`), `.codex-plugin/`, `.agents/plugins/marketplace.json`, `install-codex.sh` | OpenAI Codex | Bootstrap wrapper, hook registration, manifest, self-hosted Codex marketplace, installer. Installed via marketplace (`codex plugin add`) or the installer (hooks written to `~/.codex/config.toml`). `.agents/plugins/` is Codex-only — Claude reads `.claude-plugin/`, Cursor its own dir |
| `copilot/` (`copilot-on-event.sh`, `plugin.json`, `hooks.json`, `skills/`), `.github/plugin/marketplace.json` | GitHub Copilot CLI | Self-contained plugin package (bootstrap wrapper, manifest, camelCase hooks, configure skill) + self-hosted Copilot marketplace listing it. Installed via marketplace (`copilot plugin install dash0-agent-plugin@dash0`) or the `:copilot` subpath. `.github/plugin/` is Copilot-only |
| `opencode-v2/` (`index.js`, bootstraps, `skills/`, npm metadata) | OpenCode V2 | V2 `Plugin.define` package, configure skill and persistent event-stream consumer. Installed with `opencode plugin add @dash0/agent-plugin-opencode-v2` (npm, published by the release workflow) or from a cloned path |

The dotted directories are fixed by each agent's plugin discovery and cannot move. Keeping every other runtime asset under `claude/`, `cursor/`, `codex/`, `copilot/`, and `opencode-v2/` stops one marketplace from auto-discovering another runtime's components. `scripts/` is repo tooling only (release, version checks, the Docker test harness) — nothing there is shipped to a user.

## Contributing

Start with [DEVELOPMENT.md](DEVELOPMENT.md) for coding conventions, verification
commands and local runtime development. [ARCHITECTURE.md](ARCHITECTURE.md) explains
event flow, package ownership and telemetry contracts.
[AGENTS.md](AGENTS.md) provides the short version for coding agents.

## Releasing

**Actions → Release.** Pick `patch`, `minor` or `major`; the workflow bumps every
file that pins a version, builds, verifies, publishes, and moves `main` last. One
button, no PR.

`dry_run` builds and checks without publishing anything.

Full detail in [DEVELOPMENT.md](./DEVELOPMENT.md#releasing).

## License

Apache-2.0 — see [LICENSE](LICENSE).
