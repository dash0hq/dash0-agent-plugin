# Dash0 Agent Plugin

Connect your coding agent to [Dash0](https://dash0.com) for deep insight into how it's used — prompts and responses, tool calls, MCP calls, sub-agent activity, and token consumption — emitted as OpenTelemetry traces.

Trace through a session, see what each turn cost, find where the agent got stuck, and join agent activity with the systems it touches.

## Supported runtimes

- **Claude Code** — installation, configuration, and usage in [`.claude-plugin/README.md`](./.claude-plugin/README.md).
- **Cursor** — installation, configuration, and usage in [`.cursor-plugin/README.md`](./.cursor-plugin/README.md).
- **OpenAI Codex** — installation, configuration, and usage in [`.codex-plugin/README.md`](./.codex-plugin/README.md).
- **GitHub Copilot CLI** — installation, configuration, and usage in [`.github/plugin/README.md`](./.github/plugin/README.md).
- **Amp / ampcode CLI and Orbs** — native plugin, scripted installation (`install-amp.sh`, `install-amp.ps1`), and opt-in per-model usage in [`amp/README.md`](./amp/README.md). Completed-turn telemetry; export-based usage is partial and uses an unstable Amp export schema.

Release builds cover macOS, Linux, and Windows on `amd64` and `arm64`. The four
shell-hook integrations use bootstrap scripts; Amp instead loads `amp/index.ts`
as a native plugin and invokes its helper directly. On Windows, Claude Code also
needs [Git for Windows](https://gitforwindows.org/): it runs hook commands through
Git Bash, while Cursor, Codex, and Copilot use PowerShell. See each runtime guide
for tested host behavior; release coverage does not imply every host and OS
combination has been exercised live.

## Repository layout

This repo ships one shared Go pipeline (`cmd/`, `internal/`) and runtime-specific
plugin surfaces. Each `<runtime>/` holds everything shipped to that runtime. The
four hook integrations include a `<runtime>-on-event.sh` bootstrap wrapper; Amp
contains its native TypeScript plugin and built helper instead.

| Path | Runtime | Purpose |
|---|---|---|
| `amp/` (`index.ts`, built `amp-on-event` helper), `install-amp.sh` | Amp CLI and Orbs | Native plugin events, bounded completed-turn batches, optional exact-message per-model export usage, installer |
| `claude/` (`claude-on-event.sh`, `hooks.json`, `commands/`, `skills/`, `tools/`), `.claude-plugin/` | Claude Code | Bootstrap wrapper, hook registration, slash commands, configure skill, diagnostic scripts, manifest |
| `cursor/` (`cursor-on-event.sh`, `hooks.json`, `skills/`), `.cursor-plugin/`, `install-cursor.sh` | Cursor | Bootstrap wrapper, hook registration, configure skill, manifest, installer |
| `codex/` (`codex-on-event.sh`, `hooks.json`), `.codex-plugin/`, `.agents/plugins/marketplace.json`, `install-codex.sh` | OpenAI Codex | Bootstrap wrapper, hook registration, manifest, self-hosted Codex marketplace, installer. Installed via marketplace (`codex plugin add`) or the installer (hooks written to `~/.codex/config.toml`). `.agents/plugins/` is Codex-only — Claude reads `.claude-plugin/`, Cursor its own dir |
| `copilot/` (`copilot-on-event.sh`, `plugin.json`, `hooks.json`, `skills/`), `.github/plugin/marketplace.json` | GitHub Copilot CLI | Self-contained plugin package (bootstrap wrapper, manifest, camelCase hooks, configure skill) + self-hosted Copilot marketplace listing it. Installed via marketplace (`copilot plugin install dash0-agent-plugin@dash0`) or the `:copilot` subpath. `.github/plugin/` is Copilot-only |

The dotted directories are fixed by each agent's plugin discovery and cannot move. Keeping every other runtime asset under `claude/`, `cursor/`, `codex/`, and `copilot/` stops one marketplace from auto-discovering another runtime's components. `scripts/` is repo tooling only (release, version checks, the Docker test harness) — nothing there is shipped to a user.

## Releasing

**Actions → Release.** Pick `patch`, `minor` or `major`; the workflow bumps every
file that pins a version, builds, verifies, publishes, and moves `main` last. One
button, no PR.

`dry_run` builds and checks without publishing anything.

Full detail in [DEVELOPMENT.md](./DEVELOPMENT.md#releasing).

## License

Apache-2.0 — see [LICENSE](LICENSE).
