# Dash0 Agent Plugin

Emit GitHub Copilot CLI agent activity as OpenTelemetry spans to your Dash0 endpoint — prompts and responses, tool calls, MCP calls, and sub-agent activity, with shared trace context across each turn.

## Requirements

- **Agent:** the GitHub Copilot CLI.
- **Operating system:** macOS, Linux, or Windows.
- **Architecture:** `amd64` (x86_64) or `arm64` (aarch64).
- **Shell tooling:**
  - macOS and Linux: `bash`, `curl` or `wget`, and `sha256sum` or `shasum` — the
    bootstrap downloads and checksum-verifies the hook binary on first run.
  - Windows: nothing extra

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

1. Writes your Dash0 credentials to `~/.copilot/dash0-agent-plugin.local.md`, restricted to your user (chmod 600, or owner-only ACLs on Windows) — or, if you choose project scope, to `.copilot/dash0-agent-plugin.local.md` in the current workspace.
2. Installs a **launch shell function** that shadows `copilot` to enable Copilot's native OpenTelemetry into a per-session file. Open a new shell afterward.

**Why the launch function matters:** Copilot's native OTel is the source of per-turn token/cost/model usage, the agent response, and all tool spans — Copilot cannot enable it from a hook, and it does not hand the file path to hooks, so the launcher owns it. A `copilot` started from a shell without the function still emits one `chat` span per turn, just without usage, response, or tool detail (graceful — never an error).

> **Note:** on Windows the skill installs the function into the file `$PROFILE` names, so it applies to PowerShell sessions. A `copilot` started from `cmd.exe` has no equivalent and emits `chat` spans without usage, response, or tool detail.

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

The config file lives at `~/.copilot/dash0-agent-plugin.local.md`, restricted to your user (chmod 600, or owner-only ACLs on Windows) because it holds your token in cleartext. YAML frontmatter:

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
| `auth_token` | Dash0 authentication token | — | Yes (config file, owner-only) |
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

> `auth_token` has **no `DASH0_AUTH_TOKEN` env var fallback** — it is never read from a `DASH0_*` variable to prevent leaking into tool-spawned shell environments. Set it via the config file's `auth_token:` field.

## Organization-wide deployment

Copilot CLI has an enterprise policy layer that mirrors Claude Code's managed settings, down to the filename. It can install and enable this plugin across a fleet without any user action. It cannot deliver the plugin's configuration, so a Copilot rollout is a two-channel operation — policy for the install, device provisioning for the credentials — where the equivalent Claude Code rollout is a single JSON payload.

*Researched against GitHub documentation and the `github/copilot-cli` issue tracker on 2026-07-31; bug reports below were filed against Copilot CLI v1.0.75. Unlike the Claude Code guide, none of this has been executed against a live enterprise account — treat the payloads as documentation-derived, and re-verify the schema against the [managed settings reference](https://docs.github.com/en/copilot/reference/enterprise-managed-settings-reference) before a customer rollout.*

### How Copilot resolves managed settings

Settings reach a machine over three channels: MDM, server-managed from `copilot/managed-settings.json` in the enterprise's `.github-private` repository, and a file-based path per platform. They resolve **per key**, so MDM wins for the keys it sets and the server supplies the rest, which lets one policy be split across both. For the platform paths, refresh cadence, and file-ownership rules, see [Configure enterprise managed settings](https://docs.github.com/en/copilot/how-tos/administer-copilot/manage-for-enterprise/manage-agents/configure-enterprise-managed-settings) and the [CLI config reference](https://docs.github.com/en/copilot/reference/copilot-cli-reference/cli-config-dir-reference).

> **Do not promise enforcement without testing it.** GitHub's own pages disagree: the [enterprise reference](https://docs.github.com/en/copilot/reference/enterprise-managed-settings-reference) puts managed values above anything a user sets, while the [CLI config reference](https://docs.github.com/en/copilot/reference/copilot-cli-reference/cli-config-dir-reference) treats MDM as a baseline that "user settings can override" for every key except `permissions.disableBypassPermissionsMode`. Only `telemetry` is settled in favor of enforcement (see [below](#routing-copilots-native-telemetry-instead)), and [#4283](https://github.com/github/copilot-cli/issues/4283) is a live case of the user-settings side winning.

Three limits are worth confirming before scoping a rollout, all of them GitHub's rather than ours: settings apply enterprise-wide with **no organization-level override**, so one organization inside a larger enterprise cannot deploy this for itself; [content exclusion does not apply to Copilot CLI](https://docs.github.com/en/copilot/how-tos/configure-content-exclusion/exclude-content-from-copilot); and a dedicated Copilot Business enterprise has no organization to hold `.github-private`, so the server-managed channel there costs one GitHub Enterprise license for whoever creates it. MDM and file-based delivery need neither.

### Installing the plugin fleet-wide

Two keys do the work: `extraKnownMarketplaces` makes the Dash0 marketplace resolvable, and `enabledPlugins` makes each client [install it automatically](https://docs.github.com/en/copilot/concepts/agents/about-enterprise-plugin-standards). Place this at `copilot/managed-settings.json` in the enterprise's `.github-private` repository:

```json
{
  "extraKnownMarketplaces": {
    "dash0": {
      "source": { "source": "github", "repo": "dash0hq/dash0-agent-plugin" }
    }
  },
  "enabledPlugins": {
    "dash0-agent-plugin@dash0": true
  }
}
```

> Note the doubled `source`: the marketplace object holds a `source` property whose own type discriminator is also `source`. No `path` is needed because [`.github/plugin/marketplace.json`](marketplace.json) is one of the locations Copilot already checks. Pin to a `ref` or a full-SHA `sha` if the enterprise wants installs immune to tag moves.

To make Dash0 the only marketplace developers can install from, pair the above with `strictKnownMarketplaces`, an array of marketplace objects where an empty array is a full lockdown:

```json
{
  "strictKnownMarketplaces": [
    { "source": "github", "repo": "dash0hq/dash0-agent-plugin" }
  ]
}
```

`enabledPlugins` installs on behalf of each user, so a privately hosted plugin can fail for exactly the developers it targeted. Ours is a public repository, so authorization is never the failure mode — worth knowing if anyone proposes forking it internally.

Verify the install half before relying on it. [#4283](https://github.com/github/copilot-cli/issues/4283) is open against v1.0.75: server-managed `enabledPlugins` installs the plugin but persists `enabled: false`, because an empty `enabledPlugins: {}` in the user's local `settings.json` is treated as authoritative. Hooks never load, and the documented workaround of adding the same entry to every user's local settings defeats the purpose.

### What policy cannot deliver

The [supported key set](https://docs.github.com/en/copilot/reference/enterprise-managed-settings-reference#supported-keys) governs permissions, models, plugins, marketplaces, telemetry, and remote control. What it has no key for is configuration: there is no `env` block, and `enabledPlugins` entries are booleans that enable or disable a plugin without carrying a payload. Copilot's [plugin manifest](https://docs.github.com/en/copilot/reference/copilot-cli-reference/cli-plugin-reference#pluginjson) has no counterpart to Claude Code's `userConfig` either, so there is no schema for a plugin to declare options against and no channel to fill them from.

So this plugin's [`copilot/plugin.json`](../../copilot/plugin.json) declares no options at all, and every value is read at hook time from the config file that [`copilot-on-event.sh`](../../copilot/copilot-on-event.sh) parses. A fleet-wide `enabledPlugins` push therefore installs a plugin that starts up configured with nothing: policy delivers the install, while credentials and the launch wrapper both have to arrive by device provisioning.

The gap is tracked as [#3909](https://github.com/github/copilot-cli/issues/3909), which names Claude Code's server-managed settings as its parity target and where Dash0's requirements are on record. It remains open.

Credentials are the easy half: place `~/.copilot/dash0-agent-plugin.local.md` with mode 600 using whatever tooling already seeds dotfiles. The launch wrapper is the trap, because it cannot be reduced to exported variables. Copilot reads `COPILOT_OTEL_ENABLED` and `COPILOT_OTEL_FILE_EXPORTER_PATH` from its own process environment rather than the hook's, and the exporter path must be unique per session or concurrent sessions interleave their writes into one file. Provisioning therefore has to install the shell function that [`/dash0-configure`](../../copilot/skills/dash0-configure/SKILL.md) writes, into a system-wide profile such as `/etc/profile.d/dash0-copilot.sh`. Omitting it degrades gracefully rather than failing — see [`copilot/README.md`](../../copilot/README.md).

### Routing Copilot's native telemetry instead

The [`telemetry`](https://docs.github.com/en/copilot/reference/enterprise-managed-settings-reference#telemetry) managed key is the one place GitHub already ships an endpoint and credentials centrally. It went [generally available on 2026-07-08](https://github.blog/changelog/2026-07-08-enterprise-managed-opentelemetry-export-for-vs-code-and-cli/). Since Dash0's OTLP contract is a base URL plus `Authorization` and `Dash0-Dataset` headers, it maps onto the schema directly:

```json
{
  "telemetry": {
    "enabled": true,
    "endpoint": "https://ingress.<region>.aws.dash0.com",
    "protocol": "http/json",
    "serviceName": "github-copilot",
    "captureContent": false,
    "lockCaptureContent": true,
    "resourceAttributes": { "deployment.environment": "production" },
    "headers": {
      "Authorization": "Bearer <DASH0_AUTH_TOKEN>",
      "Dash0-Dataset": "default"
    }
  }
}
```

This is the only genuinely zero-touch part of a Copilot rollout: no plugin, no config file, no launch wrapper, and it reaches the VS Code extension as well as the CLI. It is also the only key whose enforcement is documented rather than ambiguous. The changelog states that "a managed value always wins, taking precedence over environment variables and user settings," so a developer cannot redirect the export with `OTEL_EXPORTER_OTLP_ENDPOINT`, and `lockCaptureContent` pins prompt capture the same way. Managed headers are also never passed through environment variables, so the token cannot leak into tool subprocesses the way a `DASH0_*` variable would.

Three things to know before recommending it:

- **`http/json`, not `http/protobuf`.** The reference lists protobuf as valid but [#2934](https://github.com/github/copilot-cli/issues/2934) is still open against it, so the two sources disagree. Dash0 accepts JSON — it is what this plugin's own exporter posts — so JSON costs nothing and cannot be the thing that breaks a first rollout.
- **Headers are static and never refreshed** ([#3477](https://github.com/github/copilot-cli/issues/3477)). Rotating the token means re-pushing managed settings and waiting out the refresh. Tolerable here only because Dash0 ingest tokens are long-lived.
- **The token is plaintext in `.github-private`** under the server-managed channel, including in that repository's history. Use an ingest-only token scoped to the target dataset, and prefer MDM delivery where repository read access is broad.

#### Choosing between the two

The plugin path stays the default, because it produces the span shape this plugin's other runtimes produce. Native export wins when zero-touch matters more than enrichment, or as a first step while device provisioning is still being built. Since the plugin consumes Copilot's native OTel as its own upstream, the two are the same data with and without enrichment, so comparing them is cheap — provided only one targets a given dataset at a time.

| | Plugin (policy install + provisioning) | Managed `telemetry` |
|---|---|---|
| Clients covered | Copilot CLI | Copilot CLI and VS Code |
| Developer action required | Credentials and launch wrapper per machine | None |
| Enforceable against a determined user | Unresolved, see [#4283](https://github.com/github/copilot-cli/issues/4283) | Yes, documented to beat env vars and user settings |
| Span shape | Canonical, shared with Claude Code, Cursor, and Codex | GitHub's GenAI semantic-convention shape |
| VCS and code enrichment (repo, branch, PR, issue, commit) | Yes | No |
| Signals | Traces only | Traces and metrics |
| Dataset routing | Per user or per project | Enterprise-wide, one `Dash0-Dataset` header |
| Token rotation | Re-provision the config file or policy hook | Re-push managed settings, static until then |
| Token exposure | Config file on disk, mode 600 | Plaintext in `.github-private`, or MDM payload |
| Sub-agent parenting | As described in [`copilot/README.md`](../../copilot/README.md) | Native `invoke_agent` spans, no plugin-side re-parenting |

### Where this leaves a Copilot rollout

Install is centrally governable; configuration is not, and the launch wrapper makes device provisioning unavoidable for complete telemetry. Describe it to an enterprise as policy-managed installation plus MDM-delivered credentials and shell profile, noting that #4283 currently undermines even the install half. Where that provisioning does not exist yet, managed `telemetry` is a working zero-touch fallback that gives up enrichment rather than correctness.

Claude Code reaches the full-enrichment outcome with one payload and no device management. Copilot's gap is a tracked feature request rather than a design decision, so revisit this section when [#3909](https://github.com/github/copilot-cli/issues/3909) closes and re-test the install half whenever #4283 moves.

### References

| Topic | Source |
|---|---|
| Managed settings keys and precedence | [Enterprise managed settings reference](https://docs.github.com/en/copilot/reference/enterprise-managed-settings-reference) |
| Delivery channels and `.github-private` setup | [Configure enterprise managed settings](https://docs.github.com/en/copilot/how-tos/administer-copilot/manage-for-enterprise/manage-agents/configure-enterprise-managed-settings) |
| Config directory and managed paths | [CLI config dir reference](https://docs.github.com/en/copilot/reference/copilot-cli-reference/cli-config-dir-reference) |
| Auto-install semantics | [About enterprise plugin standards](https://docs.github.com/en/copilot/concepts/agents/copilot-cli/about-enterprise-plugin-standards) |
| Plugin manifest fields | [CLI plugin reference](https://docs.github.com/en/copilot/reference/copilot-cli-reference/cli-plugin-reference) |
| Content exclusion scope | [Exclude content from Copilot](https://docs.github.com/en/copilot/how-tos/configure-content-exclusion/exclude-content-from-copilot) |
| Native OTel span, metric, and event schema | [CLI command reference, OpenTelemetry monitoring](https://docs.github.com/en/copilot/reference/copilot-cli-reference/cli-command-reference#opentelemetry-monitoring) |
| Managed `telemetry` GA, precedence over env vars | [Enterprise-managed OpenTelemetry export changelog](https://github.blog/changelog/2026-07-08-enterprise-managed-opentelemetry-export-for-vs-code-and-cli/) (2026-07-08) |
| Central config gap | [copilot-cli#3909](https://github.com/github/copilot-cli/issues/3909) (open) |
| `enabledPlugins` enablement bug | [copilot-cli#4283](https://github.com/github/copilot-cli/issues/4283) (open) |
| Static exporter headers, no refresh | [copilot-cli#3477](https://github.com/github/copilot-cli/issues/3477) (open) |
| `http/protobuf` export support | [copilot-cli#2934](https://github.com/github/copilot-cli/issues/2934) (open, contradicts the reference) |
| Plugin cache sync bug | [copilot-cli#4039](https://github.com/github/copilot-cli/issues/4039) (fixed, v1.0.68) |

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
fails open, so `copilot` itself keeps working). Releases carry macOS, Linux, and
Windows binaries for `amd64` and `arm64` only — check what your machine reports
with `uname -s -m`, or with `$env:PROCESSOR_ARCHITECTURE` on Windows. See
[Requirements](#requirements).

A refused download looks the same. The bootstrap verifies every binary it fetches
and never runs one it cannot verify, but it still exits 0, so telemetry just
stops: look for `refusing to run an unverified binary` (no entry for the asset in
`checksums.txt`), `checksum mismatch`, or `no sha256 tool` on the hook's stderr or
in the debug log.

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

- delete the `# >>> dash0-agent-plugin (copilot) >>>` … `<<<` block from your shell profile (`~/.zshrc`, `~/.bashrc`, … or the file `$PROFILE` names on Windows),
- `rm ~/.copilot/dash0-agent-plugin.local.md`,
- `rm -rf ~/.local/state/dash0-agent-plugin/copilot` (cached binary + native-OTel files).

On Windows those two paths are `%USERPROFILE%\.copilot\dash0-agent-plugin.local.md` and `%USERPROFILE%\.local\state\dash0-agent-plugin\copilot`.

## Development

See [`copilot/README.md`](../../copilot/README.md) for how the runtime works and building/running local changes,
and [DEVELOPMENT.md](../../DEVELOPMENT.md) for releasing and cross-runtime reference.
