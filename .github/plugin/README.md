# Dash0 Agent Plugin

Emit GitHub Copilot CLI agent activity as OpenTelemetry spans to your Dash0 endpoint — prompts and responses, tool calls, MCP calls, and sub-agent activity, with shared trace context across each turn.

## Requirements

- **Agent:** the GitHub Copilot CLI.
- **Operating system:** macOS or Linux (Windows is not supported).
- **Architecture:** `amd64` (x86_64) or `arm64` (aarch64).
- **Shell tooling:** `bash`, `curl` or `wget`, and `sha256sum` or `shasum` — the
  bootstrap downloads and checksum-verifies the hook binary on first run.

## Installation

Add the Dash0 marketplace, then install the plugin from it:

```bash
copilot plugin marketplace add dash0hq/dash0-agent-plugin
copilot plugin install dash0-agent-plugin@dash0
```

The marketplace entry (`.github/plugin/marketplace.json`) points at the `copilot/` package, so `@dash0` installs exactly it — versioned, so `copilot plugin update dash0-agent-plugin` picks up new releases. On the first hook fire the `copilot-on-event` binary is fetched from [GitHub Releases](https://github.com/dash0hq/dash0-agent-plugin/releases) — verifying the checksum — into `~/.local/state/dash0-agent-plugin/copilot/bin/`.

After installing, **restart `copilot`** (hooks load at startup).

## Configure

Run the configure skill inside Copilot:

```
/dash0-configure
```

It does two things:

1. Writes your Dash0 credentials to `~/.copilot/dash0-agent-plugin.local.md` (chmod 600) — or, if you choose project scope, to `.copilot/dash0-agent-plugin.local.md` in the current workspace.
2. Installs a **launch shell function** that shadows `copilot` to enable Copilot's native OpenTelemetry into a per-session file. Open a new shell afterward.

**Why the launch function matters:** Copilot's native OTel is the source of per-turn token/cost/model usage, the agent response, and all tool spans — Copilot cannot enable it from a hook, and it does not hand the file path to hooks, so the launcher owns it. A `copilot` started from a shell without the function still emits one `chat` span per turn, just without usage, response, or tool detail (graceful — never an error).

Prompt mode (`copilot -p`) fires the hooks too, so headless runs are instrumented when launched via the function.

## Upgrading

```bash
copilot plugin update dash0-agent-plugin
```

It fetches the latest release and leaves your credentials and launch function untouched. Restart `copilot` to pick up the update.

## Configuration

After installing, you'll need:

- **Auth token** — create one from your organization's [Auth Tokens settings page](https://app.dash0.com/settings/auth-tokens). Use an ingest-only token with permissions limited to the dataset you want to send data to.
- **OTLP endpoint URL** — find it in the [Endpoints settings page](https://app.dash0.com/settings/endpoints) under the OTLP via HTTP tab (e.g. `https://ingress.<region>.aws.dash0.com`).

### Config file

The config file lives at `~/.copilot/dash0-agent-plugin.local.md` (chmod 600 — it holds your token in cleartext). YAML frontmatter:

```yaml
---
otlp_url: "https://ingress.<region>.aws.dash0.com"
auth_token: "<your-dash0-auth-token>"
dataset: "default"                  # optional
agent_name: "github-copilot-cli"    # optional — used as service.name
team_name: "<your-team>"            # optional — tagged as dash0.team.name on every span
---
```

`/dash0-configure` writes this file for you. To reconfigure later, edit it directly — see [Options](#options) for every key. Changes take effect on the next hook fire — no restart needed.

Config can be **user-level** (`~/.copilot/dash0-agent-plugin.local.md`, applies to all projects) or **project-level** (`.copilot/dash0-agent-plugin.local.md` in a workspace). A project-level file takes precedence over the user-level one and replaces it entirely — the two are not merged.

### Verify

Send a prompt that uses a tool. In Dash0 you should see one trace per turn with:

- one `chat <model>` span at turn end carrying `gen_ai.usage.input_tokens`, `output_tokens`, and `cache_read.input_tokens`
- one `execute_tool <Name>` span per tool call, with `parentSpanId` pointing at the chat span
- the same `traceId` on every span in the turn

Sub-agent tool calls (spawned via the `task` tool) nest under their spawning `task` span, and MCP calls carry `dash0.gen_ai.tool.mcp_server`. If you see `chat` spans but no usage or `execute_tool` spans, the launch function isn't active — open a new shell (or re-run `/dash0-configure`).

### Options

| Option | Description | Default | Sensitive |
|---|---|---|---|
| `otlp_url` | Dash0 OTLP endpoint URL (e.g. `https://ingress.<region>.aws.dash0.com`) | — | No |
| `auth_token` | Dash0 authentication token | — | Yes (config file, chmod 600) |
| `dataset` | Dash0 dataset name | — | No |
| `agent_name` | Agent name (used as `service.name`) | `github-copilot-cli` | No |
| `team_name` | Team name — all spans are tagged with `dash0.team.name` | — | No |
| `omit_io` | Omit prompt content and tool I/O | `true` | No |
| `omit_user_info` | Anonymize user identity | `false` | No |
| `debug` | Print OTel payloads to stderr (and `debug_file` if set) | `false` | No |
| `debug_file` | Write debug output to this file path | — | No |

Set `enabled: false` in the config file to disable the plugin without uninstalling it.

### Precedence

When a value is set in more than one source, highest wins:

1. Project-level config file (`.copilot/dash0-agent-plugin.local.md`)
2. User-level config file (`~/.copilot/dash0-agent-plugin.local.md`)
3. `DASH0_*` environment variables

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

> `auth_token` has **no `DASH0_AUTH_TOKEN` env var fallback** — it is never read from a `DASH0_*` variable to prevent leaking into tool-spawned shell environments. Set it via the config file's `auth_token:` field (the bootstrap passes it to the hook as `COPILOT_PLUGIN_OPTION_AUTH_TOKEN`).

## Privacy defaults

| Setting | Default | Behavior |
|---|---|---|
| `omit_user_info` | `false` | Real `user.name` and `user.email` are sent. When `true`, `user.name` is a SHA-256 hash, `user.email` is omitted, working directory is redacted. |
| `omit_identity_fallback` | `false` | The OS account is used when `git config user.name` is unset. When `true`, only a git identity is reported and the fallback is dropped. |
| `omit_io` | `true` | Prompt content and tool call inputs/outputs are stripped from spans. |

### User identity

`user.name` comes from `git config user.name`. When that is unset, the plugin falls back to the OS account (display name, then username) so the session is still attributable instead of arriving anonymous. `user.email` has no fallback — it is only ever the git value.

Every span carrying a name also carries `dash0.gen_ai.user.identity.source`, either `git` or `os`, so a fallback is never mistaken for a configured identity. The fallback is skipped in CI and for shared accounts (`root`, `runner`, ...), where the OS account names a machine rather than a person; those sessions report no name at all. Set `OMIT_IDENTITY_FALLBACK` to require a real git identity and drop the fallback entirely.

## Telemetry attributes

Spans follow [GenAI semantic conventions](https://opentelemetry.io/docs/specs/semconv/gen-ai/).
The OTLP pipeline is shared across runtimes, so the attribute set matches Claude Code apart from the per-runtime differences noted in [FEATURE_MATRIX.md](../../FEATURE_MATRIX.md).

## Troubleshooting

### No telemetry, and the debug log shows a failed binary download

The hook is trying to download a binary for an unsupported platform (the plugin
fails open, so `copilot` itself keeps working). Run `uname -s -m` — anything
other than `Darwin` or `Linux` on `x86_64`/`arm64`/`aarch64` is unsupported, in
particular `MINGW64_NT-…` or `MSYS_NT-…`, which is Windows under Git Bash. See
[Requirements](#requirements).

### No traces arrive

- Confirm you **restarted `copilot`** after installing (hooks load at startup).
- Confirm you opened a **new shell** after `/dash0-configure` so the launch function is active — without it, `chat` spans emit but carry no usage or tool spans.
- Enable the debug log — add `debug: true` and `debug_file: /tmp/dash0-copilot-debug.log` to `~/.copilot/dash0-agent-plugin.local.md`, then run Copilot and watch it:

  ```bash
  tail -F /tmp/dash0-copilot-debug.log
  ```

  Every emitted span is appended there as a `[dash0:trace] {...}` line. If spans are logged but don't reach Dash0, re-check `otlp_url` and `auth_token` in the config.

## Uninstall

```bash
copilot plugin uninstall dash0-agent-plugin
```

Then remove what the configure step added:

- delete the `# >>> dash0-agent-plugin (copilot) >>>` … `<<<` block from your shell profile (`~/.zshrc`, `~/.bashrc`, …),
- `rm ~/.copilot/dash0-agent-plugin.local.md`,
- `rm -rf ~/.local/state/dash0-agent-plugin/copilot` (cached binary + native-OTel files).

## Development

See [`copilot/README.md`](../../copilot/README.md) for how the runtime works and building/running local changes,
and [DEVELOPMENT.md](../../DEVELOPMENT.md) for releasing and cross-runtime reference.
