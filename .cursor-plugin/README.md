# Dash0 Agent Plugin — Cursor

Cursor plugin that emits agent activity as OpenTelemetry spans to your Dash0 endpoint — prompts and responses, tool calls, MCP calls, and sub-agent activity, with shared trace context across each turn.

## Installation

```bash
curl -fsSL https://raw.githubusercontent.com/dash0hq/dash0-agent-plugin/main/install-cursor.sh | bash
```

The installer lays the plugin down under `~/.cursor/plugins/local/dash0-agent-plugin/` — Cursor scans that directory on startup and picks up the plugin manifest, hook registrations, and shipped skills automatically. Credentials go to `~/.cursor/dash0-agent-plugin.local.md`, and the binary is fetched from [GitHub Releases](https://github.com/dash0hq/dash0-agent-plugin/releases) (verifying the checksum) into `~/.local/state/dash0-agent-plugin/cursor/bin/`.

Pre-supply credentials to skip the prompts. Either pass them as flags:

```bash
curl -fsSL https://raw.githubusercontent.com/dash0hq/dash0-agent-plugin/main/install-cursor.sh | bash -s -- \
  --endpoint https://ingress.<region>.aws.dash0.com \
  --token <your-token> \
  --dataset default
```

Or via environment variables:

```bash
DASH0_OTLP_URL=https://ingress.<region>.aws.dash0.com \
DASH0_AUTH_TOKEN=<your-token> \
DASH0_DATASET=default \
  curl -fsSL https://raw.githubusercontent.com/dash0hq/dash0-agent-plugin/main/install-cursor.sh | bash
```

Each flag (and its env-var equivalent) skips the corresponding prompt. The team-name prompt has no flag — set `DASH0_TEAM_NAME` if you want to provide it non-interactively. `DASH0_VERSION` pins a specific release; default is the latest GitHub release.

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

To reconfigure later, re-run the `dash0-configure` skill in Cursor, or edit the file directly. Config changes take effect on the next hook fire — no restart needed. (Restart is only needed when the plugin's hook manifest itself changes, e.g. after upgrading to a new version, since Cursor reads `~/.cursor/plugins/local/dash0-agent-plugin/cursor/plugin-hooks.json` at startup.)

> Under the native-plugin layout Cursor invokes hooks with the plugin directory as CWD. Per-project overrides (`.cursor/dash0-agent-plugin.local.md` inside a repo) are no longer picked up — only the global config file above is honored.

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
curl -fsSL https://raw.githubusercontent.com/dash0hq/dash0-agent-plugin/main/uninstall-cursor.sh | bash
```

Pass `-s -- --yes` to skip the confirmation prompt. The uninstaller removes the entire `~/.cursor/plugins/local/dash0-agent-plugin/` directory plus the credential config and cached binary. It also cleans up files left behind by pre-0.1.17 shell-installer versions (a legacy `~/.local/share/dash0-agent-plugin/`, `~/.cursor/skills-cursor/dash0-configure/`, and Dash0 entries in `~/.cursor/hooks.json`). If `~/.cursor/hooks.json` also holds non-Dash0 hooks, the uninstaller warns and leaves the file in place for you to edit by hand.

After uninstalling, restart Cursor so it stops registering the hooks.
