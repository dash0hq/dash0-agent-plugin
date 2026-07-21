# Dash0 Agent Plugin

Connect your coding agent to [Dash0](https://dash0.com) for deep insight into how it's used — prompts and responses, tool calls, MCP calls, sub-agent activity, and token consumption — emitted as OpenTelemetry traces.

Trace through a session, see what each turn cost, find where the agent got stuck, and join agent activity with the systems it touches.

## Supported runtimes

- **Claude Code** — installation, configuration, and usage in [`.claude-plugin/README.md`](./.claude-plugin/README.md).
- **Cursor** — installation, configuration, and usage in [`.cursor-plugin/README.md`](./.cursor-plugin/README.md).
- **OpenAI Codex** — installation, configuration, and usage in [`.codex-plugin/README.md`](./.codex-plugin/README.md).
- **GitHub Copilot CLI** — installation, configuration, and usage in [`copilot/README.md`](./copilot/README.md).

## Repository layout

This repo ships one shared Go pipeline (`cmd/`, `internal/`, `scripts/`) and runtime-specific plugin surfaces:

| Path | Runtime | Purpose |
|---|---|---|
| `.claude-plugin/`, `claude/commands/`, `claude/skills/`, `hooks/hooks.json` | Claude Code | Manifest, slash commands, configure skill, hook registration |
| `.cursor-plugin/`, `cursor/plugin-hooks.json`, `cursor/skills/` | Cursor | Manifest, hook registration, configure skill |
| `.codex-plugin/`, `codex/hooks.json`, `.agents/plugins/marketplace.json`, `install-codex.sh` | OpenAI Codex | Manifest, hook registration, self-hosted Codex marketplace, installer. Installed via marketplace (`codex plugin add`) or the installer (hooks written to `~/.codex/config.toml`). `.agents/plugins/` is Codex-only — Claude reads `.claude-plugin/`, Cursor its own dir |
| `copilot/` (`plugin.json`, `hooks.json`, `skills/`, `copilot-on-event.sh`), `.github/plugin/marketplace.json` | GitHub Copilot CLI | Self-contained plugin package (manifest, camelCase hooks, configure skill, vendored bootstrap) + self-hosted Copilot marketplace listing it. Installed via marketplace (`copilot plugin install dash0-agent-plugin@dash0`) or the `:copilot` subpath. `.github/plugin/` is Copilot-only |

Runtime-specific assets live under `claude/`, `cursor/`, and `copilot/` so neither marketplace auto-discovers the other runtime's components. Shared hook binaries stay in `scripts/`.

## License

Apache-2.0 — see [LICENSE](LICENSE).
