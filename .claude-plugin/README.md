# Dash0 Agent Plugin

Claude Code plugin that captures agent activity as OpenTelemetry traces — tool calls, LLM invocations, token usage, and errors.

## Requirements

- **Agent:** the Claude Code CLI.
- **Operating system:** macOS or Linux (Windows is not supported).
- **Architecture:** `amd64` (x86_64) or `arm64` (aarch64).
- **Shell tooling:** `bash`, `curl` or `wget`, and `sha256sum` or `shasum` — the
  bootstrap downloads and checksum-verifies the hook binary on first run.

## Installation

### From the official Claude Code marketplace (recommended)

```
/plugin install dash0@claude-plugins-official
```

### From the Dash0 marketplace

```
/plugin marketplace add dash0hq/claude-marketplace
/plugin install dash0-agent-plugin@dash0
```

> The plugin is registered as `dash0` in the official marketplace and `dash0-agent-plugin` in the Dash0 marketplace. Both install the same plugin; do not enable both at once or hooks will fire twice.

### Headless / CI

In environments without interactive access (containers, CI, scripts):

```bash
git config --global url."https://github.com/".insteadOf "git@github.com:"
claude plugin install dash0@claude-plugins-official --scope user
```

> Claude Code downloads plugins via SSH by default. The `git config` line forces HTTPS for environments without SSH keys.

### Project-level installation

You can commit the plugin enablement to your repository so that setup is minimal for each developer.

Add to `<repo-root>/.claude/settings.json`:

```json
{
  "enabledPlugins": {
    "dash0@claude-plugins-official": true
  }
}
```

> If using the Dash0 marketplace instead, add `extraKnownMarketplaces` and enable `dash0-agent-plugin@dash0` — see [From the Dash0 marketplace](#from-the-dash0-marketplace) above.

> **`pluginConfigs` does not work in project settings.** As of Claude Code v2.1.207, `pluginConfigs` is read only from user settings (`~/.claude/settings.json`), the `--settings` flag, and managed settings — entries in `.claude/settings.json` or `.claude/settings.local.json` are ignored, because a cloned repository could otherwise supply values that flow into plugin hook commands. They are ignored **silently, with no warning** — the plugin loads and appears configured but exports nothing. Commit `enabledPlugins` only (still honored at project scope), and have each developer configure their options locally.

`enabledPlugins` is committed to git. Each developer then:

1. Installs the plugin once: `/plugin install dash0@claude-plugins-official`
2. Sets their OTLP URL and auth token: `/plugin` → **dash0** → **Configure** (token stored in OS keychain), or uses the [config file](#config-file)

> **Worktree / multi-clone caveat:** Project-scoped installs are keyed to the repository's absolute path. If you use git worktrees or multiple clones, the plugin fails to load in the second checkout. Use `--scope user` instead (`claude plugin install dash0@claude-plugins-official --scope user`).

## Organization-wide deployment

Admins can install and configure the plugin for every member with no per-developer steps, using [Claude Code managed settings](https://code.claude.com/docs/en/server-managed-settings). Managed settings take top precedence and cannot be overridden by user or project settings.

*Verified on Claude Code 2.1.220 with plugin 0.1.22, via the Dash0 marketplace and server-managed settings.*

### 1. Install the plugin on members' machines

In the claude.ai **Organization plugins** admin page, set the plugin to **Required** (auto-installed, not removable) or **Installed by default**. That is what downloads the plugin; it takes effect on the member's next session.

Managed settings only enable and configure a plugin. Skipping this step is the most likely reason a rollout produces no telemetry: `enabledPlugins` names a plugin that was never fetched, so there is nothing to load.

### 2. Push the configuration

Add the payload in claude.ai → **Admin Settings** → **Claude Code** → **Managed settings**. Requires a Team or Enterprise plan and the Owner or Primary Owner role. Clients fetch it at startup and re-poll hourly, caching it at `~/.claude/remote-settings.json` and applying it in memory.

```json
{
  "$schema": "https://json.schemastore.org/claude-code-settings.json",
  "extraKnownMarketplaces": {
    "dash0": {
      "source": { "source": "github", "repo": "dash0hq/claude-marketplace" },
      "autoUpdate": true
    }
  },
  "enabledPlugins": { "dash0-agent-plugin@dash0": true },
  "pluginConfigs": {
    "dash0-agent-plugin@dash0": {
      "options": {
        "OTLP_URL": "https://ingress.<region>.aws.dash0.com",
        "AUTH_TOKEN": "auth_...",
        "DATASET": "default"
      }
    }
  }
}
```

Installing from the official marketplace instead? Drop `extraKnownMarketplaces` and key both `enabledPlugins` and `pluginConfigs` on `dash0@claude-plugins-official`. Enable one identity, never both: they ship the same hooks, so both enabled means every span is exported twice. Any option from [Options](#options) can go in `options`; the keys must match the identity in `enabledPlugins` exactly, or the plugin loads unconfigured.

### MDM alternative

If you manage devices with MDM, deploy the same JSON as an on-disk `managed-settings.json` instead of using the console. The path differs per OS and has changed between versions, so take it from [the settings reference](https://code.claude.com/docs/en/settings) rather than copying it from here.

> Server-managed and endpoint-managed settings do **not** merge. If the console delivers any keys, the on-disk file is ignored entirely. Pick one channel per organization.

> Server-managed settings are not delivered on Bedrock, Vertex, Foundry, or a custom `ANTHROPIC_BASE_URL`. Use the on-disk file there.

### The auth token: three options

`AUTH_TOKEN` in managed `pluginConfigs.options` is honored, and is the simplest zero-touch path. The cost is that the token is stored in plaintext on every machine in the fleet, in `~/.claude/remote-settings.json`, readable by any process running as that user. It is plaintext at rest in the console too. Nothing the plugin does can encrypt around this.

Two things make it defensible: use an ingest-only token scoped to the one dataset you send to, so a leak can write telemetry there and read nothing; and rotation is centralized, since changing the console value updates the fleet on the next poll.

If policy forbids plaintext credentials at rest, there are two alternatives:

- **Keychain reference** (macOS) — ship `AUTH_TOKEN_KEYCHAIN_SERVICE` instead of `AUTH_TOKEN` and provision the secret into each machine's keychain. Keeps the token out of the console and off disk in plaintext, but requires MDM and doesn't restrict local access. See [Managed / MDM rollout](#managed--mdm-rollout-macos-keychain).
- **Per-developer setup** — omit the token entirely and have each developer add it once via `/plugin` → **Configure**, which stores it encrypted in the OS keychain. No plaintext, but no longer zero-touch.

### Per-team attribution

Keep `TEAM_NAME` out of the payload. Managed values cannot be overridden, so setting it centrally tags every developer with the same team and leaves nobody able to correct it. Omitting it is safe: `pluginConfigs` merges per key, so the payload above still lets each developer's own `TEAM_NAME` through.

Per-developer values come from `team_name:` in `~/.claude/dash0-agent-plugin.local.md` or the `DASH0_TEAM_NAME` environment variable. The environment variable is the portable one, being the only form the Cursor, Codex, and Copilot runtimes read; the config file works even where no shell environment is inherited, as with Claude Desktop.

Distributing those per-user values is an enterprise concern the plugin cannot solve. Whichever system already tracks team membership has to write the file or export the variable, and run again when someone moves teams. One trap: the project-level and user-level config files do not merge, so a repository containing `.claude/dash0-agent-plugin.local.md` makes a provisioned team name disappear for anyone working in that repo.

### Locking it down

Managed settings can also restrict which marketplaces are usable, block sideload flags, and require a successful settings fetch before startup. The available keys change between releases, so see [the settings reference](https://code.claude.com/docs/en/settings) for the current set.

### Network prerequisites

Each machine needs HTTPS egress to `github.com`, where the plugin fetches its release binary on first run and verifies it against `checksums.txt`, and to your Dash0 OTLP ingress. Without GitHub access the plugin installs but never exports. For container images, pre-bake the marketplace and plugin cache with `CLAUDE_CODE_PLUGIN_SEED_DIR` so nothing is downloaded at runtime.

### Verify the rollout

A developer runs `/status` → **Setting sources**, which should list `Enterprise managed settings`. `claude plugin list` should show exactly one Dash0 identity with `Status: ✔ enabled` and `Scope: managed`, confirming the enablement came from managed settings rather than a local install. The session banner reads `dash0: connected`, and the session appears in Dash0 under the configured dataset.

> **Do not let developers self-install as well.** If someone already installed the other identity at user scope, both are enabled and every span is exported twice under two independent configurations. Remove the user-scoped one with `claude plugin uninstall dash0@claude-plugins-official --scope user` and let managed settings be the single source of truth.

## Configuration

After installing, you'll need:

- **Auth token** — create one from your organization's [Auth Tokens settings page](https://app.dash0.com/settings/auth-tokens). Use an ingest-only token with permissions limited to the dataset you want to send data to.
- **OTLP endpoint URL** — find it in the [Endpoints settings page](https://app.dash0.com/settings/endpoints) under the OTLP via HTTP tab (e.g. `https://ingress.<region>.aws.dash0.com`).

### Settings file

Plugin options can be set under `pluginConfigs` in **user-level** settings (`~/.claude/settings.json`) — the same file that `/plugin → Configure` writes to. This applies to all projects.

```json
{
  "pluginConfigs": {
    "dash0@claude-plugins-official": {
      "options": {
        "OTLP_URL": "https://ingress.<region>.aws.dash0.com",
        "AUTH_TOKEN": "your-dash0-auth-token",
        "DATASET": "default"
      }
    }
  }
}
```

> Claude Code reads `pluginConfigs` from only three sources: user settings, the `--settings` flag, and enterprise-managed settings. Project-level `.claude/settings.json` and `.claude/settings.local.json` entries are ignored (v2.1.207+) — see [Project-level installation](#project-level-installation).

> Setting `AUTH_TOKEN` here writes it in plaintext. Prefer `/plugin → Configure`, which stores it in the OS keychain. Never commit a settings file containing `AUTH_TOKEN`.

### Plugin UI

`/plugin` → **Installed** → **dash0** (or **dash0-agent-plugin** from the Dash0 marketplace) → **Configure**, then `/reload-plugins` to apply. Values are written to `pluginConfigs` in `~/.claude/settings.json`; sensitive values are stored in the OS keychain.

> **Claude Desktop limitation:** The Plugin UI writes config keyed to the marketplace plugin identity. Claude Desktop loads plugins under a different internal identity, so Plugin UI configuration is not applied in Desktop sessions. Use the [config file](#config-file) or [settings file](#settings-file) method instead — both work across CLI and Desktop.

### Config file

Create `~/.claude/dash0-agent-plugin.local.md` (applies to all projects), or `.claude/dash0-agent-plugin.local.md` in a project directory for project-specific config:

```markdown
---
otlp_url: "https://ingress.<region>.aws.dash0.com"
auth_token: "your-dash0-auth-token"
dataset: "default"
---
```

Or run `/dash0-configure` to walk through the values interactively — the skill writes the same file for you.

A project file replaces the user-level one entirely; the two are never merged. Where a value is set both here and in `pluginConfigs` (what `/plugin` → **Configure** writes), the `pluginConfigs` value wins.


### Verify

On session start you should see:

```
dash0: connected (v0.1.22)
```

If credentials are missing: `dash0: telemetry is not active — configure the plugin to start sending data.`

If telemetry works but `TEAM_NAME` is unset, a second line names the gap:

```
dash0: no team configured — spans carry no dash0.team.name. Set Team Name via /plugin → Configure.
```

Claude also receives this as context, so you can just ask it to finish the setup and it runs `/dash0-agent-plugin:dash0-configure` for you. The warning cannot be silenced — setting a team name is what stops it.

### Options

| Option | Description | Default | Sensitive |
|---|---|---|---|
| `OTLP_URL` | Dash0 OTLP endpoint URL (e.g. `https://ingress.<region>.aws.dash0.com`) | — | No |
| `AUTH_TOKEN` | Dash0 authentication token | — | Yes (stored in keychain) |
| `AUTH_TOKEN_KEYCHAIN_SERVICE` | macOS keychain service holding the token, read at runtime instead of storing it ([managed rollout](#managed--mdm-rollout-macos-keychain)) | — | No |
| `AUTH_TOKEN_KEYCHAIN_ACCOUNT` | Optional account for that keychain item | — | No |
| `DATASET` | Dash0 dataset name | — | No |
| `AGENT_NAME` | Agent name (used as `service.name`) | `claude-code` | No |
| `TEAM_NAME` | Team name — all spans are tagged with `dash0.team.name` | — | No |
| `OMIT_IO` | Omit prompt content and tool I/O | `true` | No |
| `OMIT_USER_INFO` | Anonymize user identity | `false` | No |
| `OMIT_IDENTITY_FALLBACK` | Require a real git identity | `false` | No |
| `SHOW_SESSION_LINK` | Print the session URL after every turn | `false` | No |

The config file uses lowercase equivalents (`otlp_url`, `auth_token`, `dataset`, etc.) plus an additional `enabled` option to disable the plugin per-project without uninstalling it.

### Precedence

When a value is set in more than one source, highest wins:

1. `pluginConfigs` in [enterprise-managed settings](#organization-wide-deployment) (cannot be overridden by users)
2. `pluginConfigs` in user-level `~/.claude/settings.json` (same as `/plugin → Configure` UI)
3. Project-level config file (`.claude/dash0-agent-plugin.local.md`)
4. User-level config file (`~/.claude/dash0-agent-plugin.local.md`)
5. `DASH0_*` environment variables

`pluginConfigs` in project-level `.claude/settings.json` is **not** a source — Claude Code ignores it (v2.1.207+).

The two config files do **not** merge: if a project-level file exists, it is used and the global file is ignored entirely.

### Environment variable fallback

The plugin falls back to `DASH0_*` environment variables when `userConfig` values are not set. Useful for `--plugin-dir` development or CI.

| Variable | Description |
|---|---|
| `DASH0_OTLP_URL` | OTLP endpoint URL |
| `DASH0_DATASET` | Dataset name |
| `DASH0_AGENT_NAME` | Agent name |
| `DASH0_TEAM_NAME` | Team name |
| `DASH0_AUTH_TOKEN_KEYCHAIN_SERVICE` | macOS keychain service to read the token from |
| `DASH0_AUTH_TOKEN_KEYCHAIN_ACCOUNT` | Optional account for that keychain item |
| `DASH0_OMIT_USER_INFO` | Anonymize user identity (`true`/`false`) |
| `DASH0_OMIT_IDENTITY_FALLBACK` | Require a real git identity (`true`/`false`) |
| `DASH0_OMIT_IO` | Omit prompts and tool I/O (`true`/`false`) |
| `DASH0_SHOW_SESSION_LINK` | Print session URL after every turn (`true`/`false`) |
| `DASH0_DEBUG` | Print OTel payloads to stderr (`true`/`false`) |
| `DASH0_DEBUG_FILE` | Write debug output to this file path |

> `AUTH_TOKEN` has **no `DASH0_AUTH_TOKEN` env var fallback** — it is never read from a `DASH0_*` variable to prevent leaking into tool-spawned shell environments. Use `/plugin → Configure` (OS keychain) or the config file's `auth_token:` field. The two `*_KEYCHAIN_*` variables above are exempt: they name an item rather than carrying the secret.

### Managed / MDM rollout (macOS keychain)

Push a *reference* to the token in managed config and provision the secret itself into each machine's keychain, so the token never appears in the console or in `~/.claude/remote-settings.json`.

Scope: this keeps the token out of the cloud and off disk in plaintext. It does **not** restrict local access — the item below carries a `don't-require-password` ACL, so any process running as that developer can read it, just as it could read `remote-settings.json`. The keychain is also per-machine and never synced, so you need MDM (or another per-machine channel) to place the item; the console alone cannot. It remains the same shared ingest token for everyone.

A keychain item is addressed by two labels you invent when storing it — a **service** and an **account**. The same strings go in the plugin config; if they don't match, the lookup fails silently and the plugin falls back to any inline `AUTH_TOKEN`.

1. Store the token on each machine, from your MDM or onboarding script (`-U` makes it re-runnable):

   ```bash
   security add-generic-password -s "dash0-auth-token" -a "dash0" -w "auth_abc123…" -U
   ```

2. Point the plugin at it — same two strings, no `AUTH_TOKEN`:

   ```json
   "options": {
     "OTLP_URL": "https://ingress.<region>.aws.dash0.com",
     "AUTH_TOKEN_KEYCHAIN_SERVICE": "dash0-auth-token",
     "AUTH_TOKEN_KEYCHAIN_ACCOUNT": "dash0"
   }
   ```

   `AUTH_TOKEN_KEYCHAIN_ACCOUNT` is optional; omit it to match by service alone.

3. Verify on one machine before rolling out — this is the exact lookup the plugin performs:

   ```bash
   security find-generic-password -s "dash0-auth-token" -a "dash0" -w
   ```

   Printing the token means the plugin will find it. `SecKeychainSearchCopyNext` means no item exists under those labels.

A successful lookup takes precedence over an inline `AUTH_TOKEN`. macOS only, and Claude Code only — Cursor, Codex, and Copilot read the token from `dash0-agent-plugin.local.md` in plaintext.

## Privacy defaults

| Setting | Default | Behavior |
|---|---|---|
| `OMIT_USER_INFO` | `false` | Real `user.name` and `user.email` are sent. When `true`, `user.name` is a SHA-256 hash, `user.email` is omitted, working directory is redacted. |
| `OMIT_IDENTITY_FALLBACK` | `false` | The OS account is used when `git config user.name` is unset. When `true`, only a git identity is reported and the fallback is dropped. |
| `OMIT_IO` | `true` | Prompt content and tool call inputs/outputs are stripped from spans. |

**Always collected** (regardless of settings): tool names, token counts, durations, model names, session structure, error status, VCS repository/branch info.

### User identity

`user.name` comes from `git config user.name`. When that is unset, the plugin falls back to the OS account (display name, then username) so the session is still attributable instead of arriving anonymous. `user.email` has no fallback — it is only ever the git value.

Every span carrying a name also carries `dash0.gen_ai.user.identity.source`, either `git` or `os`, so a fallback is never mistaken for a configured identity. The fallback is skipped in CI and for shared accounts (`root`, `runner`, ...), where the OS account names a machine rather than a person; those sessions report no name at all. Set `OMIT_IDENTITY_FALLBACK` to require a real git identity and drop the fallback entirely.

## Telemetry attributes

Spans follow [GenAI semantic conventions](https://opentelemetry.io/docs/specs/semconv/gen-ai/).
The OTLP pipeline is shared across runtimes, so the attribute set matches Claude Code apart from the per-runtime differences noted in [FEATURE_MATRIX.md](../FEATURE_MATRIX.md).

## Commands

| Command | Description |
|---|---|
| `/open-session` | Print and open the Dash0 session details URL for the current session |
| `/audit-usage` | Reconstruct a session's per-model token counts and expected span counts from the local transcripts, to compare against `/usage` and against the spans in Dash0. Takes an optional session id, defaulting to the current session |

## Skills

| Skill | Description |
|---|---|
| `/dash0-configure` | Walk through setting the OTLP URL, auth token, and other options, then write `~/.claude/dash0-agent-plugin.local.md` (user-level) or `.claude/dash0-agent-plugin.local.md` (project-level). Prefer `/plugin → Configure` if you want the auth token stored in the OS keychain. |

## Troubleshooting

### Every hook fails with a 404

The hook is trying to download a binary for an unsupported platform. Run
`uname -s -m` — anything other than `Darwin` or `Linux` on
`x86_64`/`arm64`/`aarch64` is unsupported, in particular `MINGW64_NT-…` or
`MSYS_NT-…`, which is Windows under Git Bash. See [Requirements](#requirements).

A refused download reports separately. The bootstrap verifies every binary it
fetches and never runs one it cannot verify, exiting non-zero with the reason:
`refusing to run an unverified binary` (no entry for the asset in
`checksums.txt`), `checksum mismatch`, or `no sha256 tool` — see
[Requirements](#requirements).

### No spans in Dash0 after install

Check the `dash0:` message on session start:

- `dash0: telemetry is not active` — OTLP URL is not configured.
- `dash0: connectivity check failed` — URL is set but connection failed (e.g. invalid auth token).
- No message at all — run `/reload-plugins`, or restart Claude Code.

### Token or cost numbers look wrong

Run `/audit-usage` to reconstruct the session's token counts from the transcripts
on disk, then compare them with `/usage` and with the spans in Dash0. It also
reports how many spans of each kind Dash0 should hold, so missing telemetry shows
up as a gap rather than a guess.

### Debug mode

Set `DASH0_DEBUG=true` to print all OTel payloads to stderr:

```bash
DASH0_DEBUG=true claude
```

To write debug output to a file:

```bash
DASH0_DEBUG=true DASH0_DEBUG_FILE=/tmp/dash0-debug.log claude
```

Output is prefixed with `[dash0:trace]` or `[dash0:log]` for filtering.

## Development

See [claude/README.md](../claude/README.md) for local development and building,
and [DEVELOPMENT.md](../DEVELOPMENT.md) for releasing and cross-runtime reference.
