# Dash0 Agent Plugin

Connect your coding agent to [Dash0](https://dash0.com) for deep insight into how it's used — prompts and responses, tool calls, MCP calls, sub-agent activity, and token consumption — emitted as OpenTelemetry traces.

Trace through a session, see what each turn cost, find where the agent got stuck, and join agent activity with the systems it touches.

## Supported runtimes

- **Claude Code** — installation, configuration, and usage in [`.claude-plugin/README.md`](./.claude-plugin/README.md).
- **Cursor** — installation, configuration, and usage in [`.cursor-plugin/README.md`](./.cursor-plugin/README.md).
- **OpenAI Codex** — installation, configuration, and usage in [`.codex-plugin/README.md`](./.codex-plugin/README.md).
- **GitHub Copilot CLI** — installation, configuration, and usage in [`.github/plugin/README.md`](./.github/plugin/README.md).
- **OpenCode** — two ways in, both in [`opencode/README.md`](./opencode/README.md#install-layout):

  ```bash
  curl -fsSL https://raw.githubusercontent.com/dash0hq/dash0-agent-plugin/main/install-opencode.sh | bash
  ```

  or, if you would rather have it as an npm package:

  ```bash
  opencode plugin @dash0/opencode-plugin --global
  ```

The first four run on macOS, Linux, and Windows, on `amd64` or `arm64`. On Windows, Claude Code also needs [Git for Windows](https://gitforwindows.org/): it runs hook commands through Git Bash, where Cursor, Codex, and Copilot CLI use a PowerShell bootstrap. OpenCode is macOS and Linux only.

## Repository layout

This repo ships one shared Go pipeline (`cmd/`, `internal/`) and runtime-specific plugin surfaces. The rule: **`<runtime>/` holds everything shipped to that runtime**, including its `<runtime>-on-event.sh` bootstrap wrapper.

| Path | Runtime | Purpose |
|---|---|---|
| `claude/` (`claude-on-event.sh`, `hooks.json`, `commands/`, `skills/`, `tools/`), `.claude-plugin/` | Claude Code | Bootstrap wrapper, hook registration, slash commands, configure skill, diagnostic scripts, manifest |
| `cursor/` (`cursor-on-event.sh`, `hooks.json`, `skills/`), `.cursor-plugin/`, `install-cursor.sh` | Cursor | Bootstrap wrapper, hook registration, configure skill, manifest, installer |
| `codex/` (`codex-on-event.sh`, `hooks.json`), `.codex-plugin/`, `.agents/plugins/marketplace.json`, `install-codex.sh` | OpenAI Codex | Bootstrap wrapper, hook registration, manifest, self-hosted Codex marketplace, installer. Installed via marketplace (`codex plugin add`) or the installer (hooks written to `~/.codex/config.toml`). `.agents/plugins/` is Codex-only — Claude reads `.claude-plugin/`, Cursor its own dir |
| `copilot/` (`copilot-on-event.sh`, `plugin.json`, `hooks.json`, `skills/`), `.github/plugin/marketplace.json` | GitHub Copilot CLI | Self-contained plugin package (bootstrap wrapper, manifest, camelCase hooks, configure skill) + self-hosted Copilot marketplace listing it. Installed via marketplace (`copilot plugin install dash0-agent-plugin@dash0`) or the `:copilot` subpath. `.github/plugin/` is Copilot-only |
| `opencode/` (`opencode-on-event.sh`, `src/`, `skills/`, `command/`, `package.json`), `install-opencode.sh` | OpenCode | Bootstrap wrapper plus the in-process TypeScript plugin that replaces a hook mechanism OpenCode does not have, configure skill, slash commands, and the `@dash0/opencode-plugin` npm package. No dotted directory: OpenCode auto-loads whatever sits in `~/.config/opencode/plugin/` |

The dotted directories are fixed by each agent's plugin discovery and cannot move. Keeping every other runtime asset under `claude/`, `cursor/`, `codex/`, `copilot/`, and `opencode/` stops one marketplace from auto-discovering another runtime's components. `scripts/` is repo tooling only (release, version checks, the Docker test harness) — nothing there is shipped to a user.

## Releasing

**Actions → Release.** Pick `patch`, `minor` or `major`; the workflow bumps every
file that pins a version, builds, verifies, publishes, and moves `main` last. One
button, no PR.

`dry_run` builds and checks without publishing anything.

Full detail in [DEVELOPMENT.md](./DEVELOPMENT.md#releasing).

## License

Apache-2.0 — see [LICENSE](LICENSE).
