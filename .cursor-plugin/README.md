# Dash0 Agent Plugin — Cursor

Cursor plugin that emits agent activity as OpenTelemetry spans to your Dash0 endpoint — prompts and responses, tool calls, MCP calls, and sub-agent activity, with shared trace context across each turn.

## Installation

Two install paths. Both end up with the same on-device layout (`cursor-on-event` binary + bootstrap script + config file + hooks registration).

### Cursor Marketplace (recommended)

1. Install `dash0-agent-plugin` from the Cursor Marketplace. Cursor reads `.cursor-plugin/plugin.json` and registers the hooks declared in `cursor/plugin-hooks.json`.
2. Ask the agent to *configure Dash0* — the `dash0-configure` skill walks you through writing `~/.cursor/dash0-agent-plugin.local.md` with your OTLP URL, auth token, and optional dataset / team / agent name.
3. Quit and relaunch Cursor (Cmd+Q on macOS) — Cursor reads `hooks.json` only at startup.

The plugin ships no binary. The bootstrap script downloads `cursor-on-event-<os>-<arch>` from [GitHub Releases](https://github.com/dash0hq/dash0-agent-plugin/releases) on first hook fire and verifies the checksum.

### Shell installer

Non-interactive single-command install. Useful for CI, provisioning, or skipping the configure skill.

```bash
curl -fsSL https://raw.githubusercontent.com/dash0hq/dash0-agent-plugin/main/install-cursor.sh | bash
```

Pre-supply credentials via env vars to skip the prompts:

```bash
DASH0_OTLP_URL=https://ingress.<region>.aws.dash0.com \
DASH0_AUTH_TOKEN=<your-token> \
DASH0_DATASET=default \
  curl -fsSL https://raw.githubusercontent.com/dash0hq/dash0-agent-plugin/main/install-cursor.sh | bash
```

Optional env vars: `DASH0_DATASET`, `DASH0_AGENT_NAME`, `DASH0_TEAM_NAME`, `DASH0_VERSION` (pin a specific release; default: latest GitHub release).

> **Note:** `DASH0_AUTH_TOKEN` is read by the installer only — it writes the token into the config file. The runtime hook does **not** read `DASH0_AUTH_TOKEN` from the shell; it reads `auth_token:` from `~/.cursor/dash0-agent-plugin.local.md` (which the bootstrap script then passes to the hook as `CURSOR_PLUGIN_OPTION_AUTH_TOKEN`). This prevents the token from leaking into tool-spawned shell environments where other Dash0 tools might pick it up.

After install, **quit and relaunch Cursor.**

## Configuration

The config file lives at `~/.cursor/dash0-agent-plugin.local.md` (chmod 600 — it holds your token in cleartext). YAML frontmatter:

```yaml
---
otlp_url: "https://ingress.<region>.aws.dash0.com"
auth_token: "<your-dash0-auth-token>"
dataset: "default"            # optional
agent_name: "cursor"          # optional — used as service.name
team_name: "<your-team>"      # optional — tagged as dash0.team.name on every span
omit_io: false                # set true to redact prompts and tool input/output
omit_user_info: false         # set true to hash user.name and omit user.email
---
```

To reconfigure later, re-run the `dash0-configure` skill in Cursor, or edit the file directly. Config changes take effect on the next hook fire — no restart needed. (Restart is only needed if `hooks.json` itself changes, since Cursor reads it at startup.)

## Privacy defaults

| Setting | Default | Behavior |
|---|---|---|
| `omit_user_info` | `false` | Real `user.name` and `user.email` are sent. When `true`, `user.name` is a SHA-256 hash, `user.email` is omitted, working directory is redacted. |
| `omit_io` | `false` | When `true`, prompt content and tool call inputs/outputs are stripped from spans. |

**Always collected** (regardless of settings): tool names, token counts, durations, model names, session structure, error status, VCS repository/branch info.

For the full list of telemetry attributes emitted, see the [Claude Code plugin README](../.claude-plugin/README.md#telemetry-attributes).

## Verify

Send a prompt that uses a tool. In Dash0 you should see one trace per turn with:

- one `chat default` span at turn end carrying `gen_ai.usage.input_tokens`, `output_tokens`, and `cache_read.input_tokens`
- one `execute_tool <Name>` span per tool call, with `parentSpanId` pointing at the chat span
- the same `traceId` on every span in the turn

If nothing arrives, set `debug: true` and `debug_file: /tmp/dash0-cursor-debug.log` in the config. Every emitted span is also appended there as a `[dash0:trace] {...}` line:

```bash
tail -F /tmp/dash0-cursor-debug.log
```

## Uninstall

```bash
rm -rf ~/.local/state/dash0-agent-plugin \
       ~/.local/share/dash0-agent-plugin \
       ~/.cursor/dash0-agent-plugin.local.md
```

If you installed via `install-cursor.sh`, also remove `~/.cursor/hooks.json`. If you installed via the Marketplace, uninstall the plugin from Cursor's plugin UI.
