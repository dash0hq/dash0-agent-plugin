# Dash0 Agent Plugin — OpenAI Codex

Emit OpenAI Codex agent activity as OpenTelemetry spans to your Dash0 endpoint — prompts and responses, tool calls, MCP calls, and sub-agent activity, with shared trace context across each turn.

**Requirements:** the OpenAI Codex CLI, on macOS or Linux.

## Installation

```bash
curl -fsSL https://raw.githubusercontent.com/dash0hq/dash0-agent-plugin/main/install-codex.sh | bash
```

You'll be prompted for your Dash0 endpoint, token, and dataset. The installer registers the plugin's hooks in `~/.codex/config.toml` (as a managed block, preserving any hooks and config you already have), fetches the `codex-on-event` binary from [GitHub Releases](https://github.com/dash0hq/dash0-agent-plugin/releases) — verifying the checksum — into `~/.local/state/dash0-agent-plugin/codex/bin/`, and writes credentials to `~/.codex/dash0-agent-plugin.local.md` (chmod 600).

After install, **start a new Codex session.**

### Headless / non-interactive (CI, containers, fleet rollout)

Pass credentials up front so there are no prompts — as flags:

```bash
curl -fsSL https://raw.githubusercontent.com/dash0hq/dash0-agent-plugin/main/install-codex.sh | bash -s -- \
  --endpoint https://ingress.<region>.aws.dash0.com \
  --token <your-token> \
  --dataset default
```

…or as environment variables:

```bash
DASH0_OTLP_URL=https://ingress.<region>.aws.dash0.com \
DASH0_AUTH_TOKEN=<your-token> \
DASH0_DATASET=default \
  curl -fsSL https://raw.githubusercontent.com/dash0hq/dash0-agent-plugin/main/install-codex.sh | bash
```

Each flag (and its env-var equivalent) skips the corresponding prompt, so the installer runs fully unattended. The team-name prompt has no flag — set `DASH0_TEAM_NAME` to provide it. `DASH0_VERSION` pins a specific release; the default is the latest. With no credentials supplied, the installer still completes but stays inactive until you fill in `~/.codex/dash0-agent-plugin.local.md`.

> **Note:** `DASH0_AUTH_TOKEN` is read by the installer only — it writes the token into the config file. The runtime hook does **not** read `DASH0_AUTH_TOKEN` from the shell; it reads `auth_token:` from `~/.codex/dash0-agent-plugin.local.md` (which the bootstrap script passes to the hook as `CODEX_PLUGIN_OPTION_AUTH_TOKEN`). This prevents the token from leaking into tool-spawned shell environments where other Dash0 tools might pick it up.

### Via the Codex plugin marketplace

If you prefer Codex's native plugin flow:

```bash
codex plugin marketplace add dash0hq/dash0-agent-plugin
codex plugin add dash0-agent-plugin@dash0
```

Then two manual steps the installer would otherwise handle for you:

1. **Credentials** — create `~/.codex/dash0-agent-plugin.local.md` with your endpoint and token (see [Configuration](#configuration)).
2. **Trust** — in Codex, run `/hooks` and trust the Dash0 hooks (press `t`). Codex does not auto-trust a plugin's hooks on install.

Start a new Codex session. The `curl … install-codex.sh` flow above does both of these for you, so it's the simpler path unless you specifically want the plugin managed by `codex plugin`.

## Upgrading

Re-run the installer:

```bash
curl -fsSL https://raw.githubusercontent.com/dash0hq/dash0-agent-plugin/main/install-codex.sh | bash
```

It fetches the latest release (or the release pinned by `DASH0_VERSION`) and leaves your credentials untouched. Start a new Codex session to pick up the update.

## Configuration

The config file lives at `~/.codex/dash0-agent-plugin.local.md` (chmod 600 — it holds your token in cleartext). YAML frontmatter:

```yaml
---
otlp_url: "https://ingress.<region>.aws.dash0.com"
auth_token: "<your-dash0-auth-token>"
dataset: "default"            # optional
agent_name: "codex"           # optional — used as service.name
team_name: "<your-team>"      # optional — tagged as dash0.team.name on every span
omit_io: false                # set true to redact prompts and tool input/output
omit_user_info: false         # set true to hash user.name and omit user.email
---
```

To reconfigure later, edit the file directly. Changes take effect on the next hook fire — no restart needed.

Per-project overrides work: drop a `.codex/dash0-agent-plugin.local.md` inside your repo and it takes precedence over the global file (the bootstrap script checks the workspace CWD first, then `$HOME/.codex/`).

## Privacy defaults

| Setting | Default | Behavior |
|---|---|---|
| `omit_user_info` | `false` | Real `user.name` and `user.email` are sent. When `true`, `user.name` is a SHA-256 hash, `user.email` is omitted, working directory is redacted. |
| `omit_io` | `false` | When `true`, prompt content and tool call inputs/outputs are stripped from spans. |

**Always collected** (regardless of settings): tool names, token counts, durations, model names, session structure, VCS repository/branch info.

For the full list of telemetry attributes emitted, see the [Claude Code plugin README](../.claude-plugin/README.md#telemetry-attributes).

## Verify

Send a prompt that uses a tool. In Dash0 you should see one trace per turn with:

- one `chat <model>` span at turn end carrying `gen_ai.usage.input_tokens`, `output_tokens`, and `cache_read.input_tokens`
- one `execute_tool <Name>` span per tool call, with `parentSpanId` pointing at the chat span
- the same `traceId` on every span in the turn

Sub-agents appear as `invoke_agent` spans parenting their own tool calls, and MCP calls carry `dash0.gen_ai.tool.mcp_server`.

## Troubleshooting

If no traces arrive:

- Confirm you started a **new** Codex session after installing.
- In Codex, run `/hooks` and check that the Dash0 hooks are listed and **Active**.
- Enable the debug log — add `debug: true` and `debug_file: /tmp/dash0-codex-debug.log` to `~/.codex/dash0-agent-plugin.local.md`, then run Codex and watch it:

  ```bash
  tail -F /tmp/dash0-codex-debug.log
  ```

  Every emitted span is appended there as a `[dash0:trace] {...}` line. If spans are logged but don't reach Dash0, re-check `otlp_url` and `auth_token` in the config.

## Uninstall

```bash
curl -fsSL https://raw.githubusercontent.com/dash0hq/dash0-agent-plugin/main/uninstall-codex.sh | bash
```

Pass `-s -- --yes` to skip the confirmation prompt. The uninstaller removes Dash0's entries from `~/.codex/config.toml` — preserving any hooks and config you added yourself — along with the credential config and the cached binary under `~/.local/state/dash0-agent-plugin/codex/`.

Start a new Codex session afterward.
