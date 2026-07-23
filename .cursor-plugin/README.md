# Dash0 Agent Plugin — Cursor

Cursor plugin that emits agent activity as OpenTelemetry spans to your Dash0 endpoint — prompts and responses, tool calls, MCP calls, and sub-agent activity, with shared trace context across each turn.

## Installation

```bash
curl -fsSL https://raw.githubusercontent.com/dash0hq/dash0-agent-plugin/main/install-cursor.sh | bash
```

The installer lays the plugin down under `~/.cursor/plugins/local/dash0-agent-plugin/` — Cursor scans that directory on startup and picks up the plugin manifest and shipped skills. Hook registrations are merged into `~/.cursor/hooks.json` at the user scope (Cursor doesn't fire hooks from local-plugin manifests, only from `~/.cursor/hooks.json` and project-scope `.cursor/hooks.json`). Any hooks you already had in that file are preserved; only entries whose `command` references `cursor-on-event.sh` are managed by this installer. Credentials go to `~/.cursor/dash0-agent-plugin.local.md`, and the binary is fetched from [GitHub Releases](https://github.com/dash0hq/dash0-agent-plugin/releases) (verifying the checksum) into `~/.local/state/dash0-agent-plugin/cursor/bin/`.

Requires `jq` (`brew install jq` on macOS; your distro's package manager on Linux) so the installer can safely merge into your `~/.cursor/hooks.json`.

After install, **quit and relaunch Cursor.**

### Headless / non-interactive (CI, containers, fleet rollout)

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

## Upgrading

Re-run the installer; it fetches the latest release (or the release pinned by `DASH0_VERSION`) and leaves your credentials untouched. Quit and relaunch Cursor to pick up the update.

## Configuration

After installing, you'll need:

- **Auth token** — create one from your organization's [Auth Tokens settings page](https://app.dash0.com/settings/auth-tokens). Use an ingest-only token with permissions limited to the dataset you want to send data to.
- **OTLP endpoint URL** — find it in the [Endpoints settings page](https://app.dash0.com/settings/endpoints) under the OTLP via HTTP tab (e.g. `https://ingress.<region>.aws.dash0.com`).

### Config file

The config file lives at `~/.cursor/dash0-agent-plugin.local.md` (chmod 600 — it holds your token in cleartext). YAML frontmatter:

```yaml
---
otlp_url: "https://ingress.<region>.aws.dash0.com"
auth_token: "<your-dash0-auth-token>"
dataset: "default"            # optional
agent_name: "cursor"          # optional — used as service.name
team_name: "<your-team>"      # optional — tagged as dash0.team.name on every span
---
```

The installer writes this file for you. To reconfigure later, re-run the `dash0-configure` skill in Cursor, or edit the file directly — see [Options](#options) for every key. Config changes take effect on the next hook fire — no restart needed. (A restart is only needed after upgrading the plugin's registered event set, since Cursor reads `~/.cursor/hooks.json` at startup.)

Per-project overrides work: drop a `.cursor/dash0-agent-plugin.local.md` inside your repo and it takes precedence over the global file (the bootstrap script checks the workspace CWD first, then `$HOME/.cursor/`).

### Verify

Send a prompt that uses a tool. In Dash0 you should see one trace per turn with:

- one `chat default` span at turn end carrying `gen_ai.usage.input_tokens`, `output_tokens`, and `cache_read.input_tokens`
- one `execute_tool <Name>` span per tool call, with `parentSpanId` pointing at the chat span
- the same `traceId` on every span in the turn

### Options

| Option | Description | Default | Sensitive |
|---|---|---|---|
| `otlp_url` | Dash0 OTLP endpoint URL (e.g. `https://ingress.<region>.aws.dash0.com`) | — | No |
| `auth_token` | Dash0 authentication token | — | Yes (config file, chmod 600) |
| `dataset` | Dash0 dataset name | — | No |
| `agent_name` | Agent name (used as `service.name`) | `cursor` | No |
| `team_name` | Team name — all spans are tagged with `dash0.team.name` | — | No |
| `omit_io` | Omit prompt content and tool I/O | `true` | No |
| `omit_user_info` | Anonymize user identity | `false` | No |
| `debug` | Print OTel payloads to stderr (and `debug_file` if set) | `false` | No |
| `debug_file` | Write debug output to this file path | — | No |

Set `enabled: false` in the config file to disable the plugin for that scope without uninstalling it.

### Precedence

When a value is set in more than one source, highest wins:

1. Project-level config file (`.cursor/dash0-agent-plugin.local.md`)
2. User-level config file (`~/.cursor/dash0-agent-plugin.local.md`)
3. `DASH0_*` environment variables

The two config files do **not** merge: if a project-level file exists, it is used and the global file is ignored entirely.

### Environment variable fallback

The plugin falls back to `DASH0_*` environment variables when the config file doesn't set a value. Useful for CI or development.

| Variable | Description |
|---|---|
| `DASH0_OTLP_URL` | OTLP endpoint URL |
| `DASH0_DATASET` | Dataset name |
| `DASH0_AGENT_NAME` | Agent name |
| `DASH0_TEAM_NAME` | Team name |
| `DASH0_OMIT_USER_INFO` | Anonymize user identity (`true`/`false`) |
| `DASH0_OMIT_IO` | Omit prompts and tool I/O (`true`/`false`) |
| `DASH0_DEBUG` | Print OTel payloads to stderr (`true`/`false`) |
| `DASH0_DEBUG_FILE` | Write debug output to this file path |

> `auth_token` has **no `DASH0_AUTH_TOKEN` env var fallback** — it is never read from a `DASH0_*` variable to prevent leaking into tool-spawned shell environments. Set it via the config file's `auth_token:` field (the bootstrap passes it to the hook as `CURSOR_PLUGIN_OPTION_AUTH_TOKEN`).

## Privacy defaults

| Setting | Default | Behavior |
|---|---|---|
| `omit_user_info` | `false` | Real `user.name` and `user.email` are sent. When `true`, `user.name` is a SHA-256 hash, `user.email` is omitted, working directory is redacted. |
| `omit_io` | `true` | Prompt content and tool call inputs/outputs are stripped from spans. |

**Always collected** (regardless of settings): tool names, token counts, durations, model names, session structure, error status, VCS repository/branch info.

## Telemetry attributes

Spans follow [GenAI semantic conventions](https://opentelemetry.io/docs/specs/semconv/gen-ai/).
The OTLP pipeline is shared across runtimes, so the attribute set matches Claude Code apart from the per-runtime differences noted in [FEATURE_MATRIX.md](../FEATURE_MATRIX.md).

## Skills

| Skill | Description |
|---|---|
| `dash0-configure` | Walk through setting the OTLP URL, auth token, and other options, then write `~/.cursor/dash0-agent-plugin.local.md` (user-level) or `.cursor/dash0-agent-plugin.local.md` (project-level). |

## Troubleshooting

If no spans arrive:

- Confirm you **quit and relaunched** Cursor after installing (Cursor reads `~/.cursor/hooks.json` at startup).
- Enable the debug log — set `debug: true` and `debug_file: /tmp/dash0-cursor-debug.log` in the config, then watch it:

  ```bash
  tail -F /tmp/dash0-cursor-debug.log
  ```

  Every emitted span is appended there as a `[dash0:trace] {...}` line. If spans are logged but don't reach Dash0, re-check `otlp_url` and `auth_token` in the config.

## Uninstall

```bash
curl -fsSL https://raw.githubusercontent.com/dash0hq/dash0-agent-plugin/main/uninstall-cursor.sh | bash
```

Pass `-s -- --yes` to skip the confirmation prompt. The uninstaller removes the entire `~/.cursor/plugins/local/dash0-agent-plugin/` directory plus the credential config and cached binary, and strips Dash0's entries from `~/.cursor/hooks.json` while preserving any hooks you added yourself (if the file ends up with no entries, it's deleted). It also cleans up files left behind by pre-0.1.17 shell-installer versions (a legacy `~/.local/share/dash0-agent-plugin/` and `~/.cursor/skills-cursor/dash0-configure/`). `jq` must be installed.

After uninstalling, restart Cursor so it stops registering the hooks.

## Development

See [cursor/README.md](../cursor/README.md) for building and sideloading local changes,
and [DEVELOPMENT.md](../DEVELOPMENT.md) for releasing and cross-runtime reference.
