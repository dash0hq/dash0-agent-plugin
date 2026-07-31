# Dash0 Agent Plugin

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

## Organization-wide deployment

Codex has the most capable policy layer of the runtimes this plugin supports, and it is the only one where an administrator can declare the plugin's hooks as policy-trusted, skipping the per-user `/hooks` step entirely. It also has the sharpest failure mode: the flag a locked-down fleet is most likely to set disables every way this plugin currently installs.

*Researched against OpenAI's Codex documentation on 2026-07-31. Not executed against a live managed deployment — treat the payloads as documentation-derived and re-verify against the [configuration reference](https://developers.openai.com/codex/config-reference) before a rollout.*

### The two policy files

Administrators deliver [managed configuration](https://developers.openai.com/codex/enterprise/managed-configuration) through two files. `requirements.toml` holds hard constraints and composes across four layers, from a system path up through the cloud config bundle to macOS MDM. `managed_config.toml` holds soft defaults that merge onto the user's `config.toml` and override CLI `--config` flags. Take the paths, the MDM preference domain, and the current key list from OpenAI's docs rather than from here.

Two properties are worth knowing when qualifying a customer. Cloud-managed requirements are assignable **per user group** with a default fallback, so a staged rollout is possible without touching MDM. And the cloud bundle **fails closed**: if the fetch fails and no valid cache exists, the client errors rather than starting without the policy layer.

### The flag that turns this plugin off

`allow_managed_hooks_only = true` skips hooks from user, project, session, and plugin sources. **Both installation paths above are affected** — the installer registers hooks in `~/.codex/config.toml`, which is a user source, and the marketplace contributes plugin hooks. Neither survives. Only hooks declared in `requirements.toml` still load.

`features.plugins = false` separately disables the marketplace path, and `marketplaces.allowed_sources` with `restrict_to_allowed_sources` can allowlist `dash0hq/dash0-agent-plugin` if plugins stay enabled.

An enterprise that sets the lockdown flag therefore has to adopt the plugin as a managed hook, or it will not run at all.

### Deploying as a managed hook

Declare the hooks in `requirements.toml` and let your MDM place the script. Managed hooks are trusted by policy, cannot be disabled from the user hook browser, and survive `allow_managed_hooks_only`:

```toml
[features]
hooks = true

[hooks]
managed_dir = "/enterprise/hooks"

[[hooks.SessionStart]]
matcher = "*"
[[hooks.SessionStart.hooks]]
type = "command"
command = "/enterprise/hooks/codex-on-event.sh"

# Repeat for UserPromptSubmit, PreToolUse, PostToolUse, Stop, SubagentStart,
# SubagentStop, PreCompact, PostCompact, and PermissionRequest — the same set
# declared in codex/hooks.json.
```

Codex enforces the hook configuration but does **not** distribute the scripts, so your MDM has to deliver two files: `codex-on-event.sh` into `managed_dir`, and `~/.codex/dash0-agent-plugin.local.md` for the credentials. Hook handlers accept no `env` field, so the credentials file is the delivery mechanism; the bootstrap only overwrites a value when the file supplies one, so anything you inject through the environment still passes through. OpenAI advises against embedding secrets in MDM payloads, which is a further reason to ship the file rather than inline the token in the hook command.

### Or run the installer from your MDM

Where the lockdown flag is not in play, the [headless installer](#headless--non-interactive-ci-containers-fleet-rollout) is the simpler fleet path and needs no policy work: run it unattended with `--endpoint`, `--token`, and `--dataset`, or their environment-variable equivalents. It handles hook registration, binary download, and credentials in one step. It leaves the hooks in a user source, so it stops working the day the fleet adopts `allow_managed_hooks_only`.

### Codex's own OpenTelemetry is complementary, not an alternative

Codex can export its own telemetry, and an administrator can pin it from `managed_config.toml`:

```toml
[otel]
environment = "prod"
exporter = "otlp-http"
log_user_prompt = false
```

Unlike the equivalent on GitHub Copilot, this is not a substitute for the plugin. What Codex emits is a flat set of `codex.*` **log records** — `conversation_starts`, `api_request`, `sse_event`, `user_prompt`, `tool_decision`, `tool_result` — correlated only by a shared `conversation.id`, plus counters and histograms. There is no agent span tree: users pointing the gRPC exporter at a collector report receiving low-level HTTP/2 transport spans instead of `codex.*` events ([openai/codex#17687](https://github.com/openai/codex/discussions/17687), unanswered).

So the two signals do not overlap the way they do on other runtimes. Native export gives log events and metrics this plugin does not emit; the plugin gives the canonical span tree, shared with the Claude Code, Cursor, and Copilot runtimes, that native export cannot produce. Running both is additive rather than duplicative. The exporter supports static `headers` and mTLS, so pointing it at a Dash0 ingress alongside the plugin is a supported configuration.

### References

| Topic | Source |
|---|---|
| `requirements.toml`, `managed_config.toml`, MDM, precedence | [Managed configuration](https://developers.openai.com/codex/enterprise/managed-configuration) |
| Managed hooks, trust model, plugin-bundled hooks | [Hooks](https://developers.openai.com/codex/hooks) |
| Full key list, including `otel.*` and exporter TLS | [Configuration reference](https://developers.openai.com/codex/config-reference) |
| Assigning cloud policies per user group | [Managed configs console](https://chatgpt.com/codex/settings/managed-configs) |
| No agent span tree in native export | [openai/codex#17687](https://github.com/openai/codex/discussions/17687) (open) |

## Upgrading

Re-run the installer:

```bash
curl -fsSL https://raw.githubusercontent.com/dash0hq/dash0-agent-plugin/main/install-codex.sh | bash
```

It fetches the latest release (or the release pinned by `DASH0_VERSION`) and leaves your credentials untouched. Start a new Codex session to pick up the update.

## Configuration

After installing, you'll need:

- **Auth token** — create one from your organization's [Auth Tokens settings page](https://app.dash0.com/settings/auth-tokens). Use an ingest-only token with permissions limited to the dataset you want to send data to.
- **OTLP endpoint URL** — find it in the [Endpoints settings page](https://app.dash0.com/settings/endpoints) under the OTLP via HTTP tab (e.g. `https://ingress.<region>.aws.dash0.com`).

### Config file

The config file lives at `~/.codex/dash0-agent-plugin.local.md` (chmod 600 — it holds your token in cleartext). YAML frontmatter:

```yaml
---
otlp_url: "https://ingress.<region>.aws.dash0.com"
auth_token: "<your-dash0-auth-token>"
dataset: "default"            # optional
agent_name: "codex"           # optional — used as service.name
team_name: "<your-team>"      # optional — tagged as dash0.team.name on every span
---
```

The installer writes this file for you. To reconfigure later, edit the file directly — see [Options](#options) for every key. Changes take effect on the next hook fire — no restart needed.

Per-project overrides work: drop a `.codex/dash0-agent-plugin.local.md` inside your repo and it takes precedence over the global file (the bootstrap script checks the workspace CWD first, then `$HOME/.codex/`).

### Verify

Send a prompt that uses a tool. In Dash0 you should see one trace per turn with:

- one `chat <model>` span at turn end carrying `gen_ai.usage.input_tokens`, `output_tokens`, and `cache_read.input_tokens`
- one `execute_tool <Name>` span per tool call, with `parentSpanId` pointing at the chat span
- the same `traceId` on every span in the turn

Sub-agents appear as `invoke_agent` spans parenting their own tool calls, and MCP calls carry `dash0.gen_ai.tool.mcp_server`.

### Options

| Option | Description | Default | Sensitive |
|---|---|---|---|
| `otlp_url` | Dash0 OTLP endpoint URL (e.g. `https://ingress.<region>.aws.dash0.com`) | — | No |
| `auth_token` | Dash0 authentication token | — | Yes (config file, chmod 600) |
| `dataset` | Dash0 dataset name | — | No |
| `agent_name` | Agent name (used as `service.name`) | `codex` | No |
| `team_name` | Team name — all spans are tagged with `dash0.team.name` | — | No |
| `omit_io` | Omit prompt content and tool I/O | `true` | No |
| `omit_user_info` | Anonymize user identity | `false` | No |
| `debug` | Print OTel payloads to stderr (and `debug_file` if set) | `false` | No |
| `debug_file` | Write debug output to this file path | — | No |

Set `enabled: false` in the config file to disable the plugin for that scope without uninstalling it.

### Precedence

When a value is set in more than one source, highest wins:

1. Project-level config file (`.codex/dash0-agent-plugin.local.md`)
2. User-level config file (`~/.codex/dash0-agent-plugin.local.md`)
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

> `auth_token` has **no `DASH0_AUTH_TOKEN` env var fallback** — it is never read from a `DASH0_*` variable to prevent leaking into tool-spawned shell environments. Set it via the config file's `auth_token:` field (the bootstrap passes it to the hook as `CODEX_PLUGIN_OPTION_AUTH_TOKEN`).

## Privacy defaults

| Setting | Default | Behavior |
|---|---|---|
| `omit_user_info` | `false` | Real `user.name` and `user.email` are sent. When `true`, `user.name` is a SHA-256 hash, `user.email` is omitted, working directory is redacted. |
| `omit_io` | `true` | Prompt content and tool call inputs/outputs are stripped from spans. |

## Telemetry attributes

Spans follow [GenAI semantic conventions](https://opentelemetry.io/docs/specs/semconv/gen-ai/).
The OTLP pipeline is shared across runtimes, so the attribute set matches Claude Code apart from the per-runtime differences noted in [FEATURE_MATRIX.md](../FEATURE_MATRIX.md).

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

## Development

See [`codex/README.md`](../codex/README.md) for building and running local changes,
and [DEVELOPMENT.md](../DEVELOPMENT.md) for releasing and cross-runtime reference.
