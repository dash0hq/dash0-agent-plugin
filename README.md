# Dash0 Agent Plugin

Connect your coding agent to [Dash0](https://dash0.com) for deep insight into how it's used — prompts and responses, tool calls, MCP calls, sub-agent activity, and token consumption — emitted as OpenTelemetry traces.

Trace through a session, see what each turn cost, find where the agent got stuck, and join agent activity with the systems it touches.

## Supported runtimes

- **Claude Code** — installation, configuration, and usage in [`.claude-plugin/README.md`](./.claude-plugin/README.md).
- **Cursor** — installation, configuration, and usage in [`.cursor-plugin/README.md`](./.cursor-plugin/README.md).

## Repository layout

This repo ships one shared Go pipeline (`cmd/`, `internal/`, `scripts/`) and two runtime-specific plugin surfaces:

| Path | Runtime | Purpose |
|---|---|---|
| `.claude-plugin/`, `claude/commands/`, `claude/skills/`, `hooks/hooks.json` | Claude Code | Manifest, slash commands, configure skill, hook registration |
| `.cursor-plugin/`, `cursor/plugin-hooks.json`, `cursor/skills/` | Cursor | Manifest, hook registration, configure skill |

Runtime-specific assets live under `claude/` and `cursor/` so neither marketplace auto-discovers the other runtime's components. Shared hook binaries stay in `scripts/`.

## License

Apache-2.0 — see [LICENSE](LICENSE).
