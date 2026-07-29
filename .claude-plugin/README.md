# Dash0 Agent Plugin

Claude Code plugin that captures agent activity as OpenTelemetry traces — tool calls, LLM invocations, token usage, and errors.

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

To roll the plugin out to a whole organization without asking each developer to install and configure it, deliver the marketplace, the plugin enablement, and the plugin options together through Claude Code's **managed settings**. Managed settings take precedence over every other settings source and cannot be overridden by users.

*Verified on Claude Code 2.1.220 with plugin 0.1.22, deployed via the Dash0 marketplace through server-managed settings.*

### Two delivery channels

Claude Code reads managed settings from one of two places, and **they do not merge**:

| | Server-managed | Endpoint-managed |
|---|---|---|
| Delivered by | claude.ai → **Admin Settings** → **Claude Code** → **Managed settings** | MDM/config management, as a file on each machine |
| Requires | Team or Enterprise plan; Owner or Primary Owner role | Root/admin write access to the managed path |
| Location on disk | Cached at `~/.claude/remote-settings.json` | macOS `/Library/Application Support/ClaudeCode/managed-settings.json`, Linux `/etc/claude-code/managed-settings.json`, Windows `%ProgramData%\ClaudeCode\managed-settings.json` |
| Refresh | Polled automatically | On next session start |
| Zero-touch | Yes — nothing to pre-install on the machine | Requires an MDM push |

> **If server-managed settings deliver any key at all, they replace endpoint-managed settings entirely** — the two are not combined. Pick one channel and put everything in it.

> Server-managed settings are not available when Claude Code runs against Bedrock, Vertex, Foundry, or a custom `ANTHROPIC_BASE_URL`. Use the endpoint-managed file in those environments.

### Managed settings payload

The same JSON works in either channel. Which payload you use depends on the marketplace you install from — the plugin identity differs, and the two identities do not share configuration.

#### From the official Claude Code marketplace

The official marketplace is known to Claude Code already, so no `extraKnownMarketplaces` entry is needed:

```json
{
  "$schema": "https://json.schemastore.org/claude-code-settings.json",
  "enabledPlugins": {
    "dash0@claude-plugins-official": true
  },
  "pluginConfigs": {
    "dash0@claude-plugins-official": {
      "options": {
        "OTLP_URL": "https://ingress.<region>.aws.dash0.com",
        "AUTH_TOKEN": "auth_...",
        "DATASET": "default",
        "AGENT_NAME": "claude-code",
        "OMIT_IO": "true",
        "OMIT_USER_INFO": "false",
        "SHOW_SESSION_LINK": "false"
      }
    }
  }
}
```

#### From the Dash0 marketplace

Add `extraKnownMarketplaces` to make the marketplace resolvable, and key everything on `dash0-agent-plugin@dash0`:

```json
{
  "$schema": "https://json.schemastore.org/claude-code-settings.json",
  "extraKnownMarketplaces": {
    "dash0": {
      "source": { "source": "github", "repo": "dash0hq/claude-marketplace" },
      "autoUpdate": true
    }
  },
  "enabledPlugins": {
    "dash0-agent-plugin@dash0": true
  },
  "pluginConfigs": {
    "dash0-agent-plugin@dash0": {
      "options": {
        "OTLP_URL": "https://ingress.<region>.aws.dash0.com",
        "AUTH_TOKEN": "auth_...",
        "DATASET": "default",
        "AGENT_NAME": "claude-code",
        "OMIT_IO": "true",
        "OMIT_USER_INFO": "false",
        "SHOW_SESSION_LINK": "false"
      }
    }
  }
}
```

> Pick one. Enabling both identities registers the hooks twice and exports every span twice — see [Every span appears twice](#troubleshooting).

#### What each key does

- **`extraKnownMarketplaces`** (Dash0 marketplace only) makes the marketplace resolvable. On its own it installs nothing.
- **`enabledPlugins`** activates the plugin. **Without this key nothing happens** — a registered marketplace and staged options produce no telemetry, silently.
- **`pluginConfigs`** supplies the options. The key must match the identity used in `enabledPlugins` exactly; config is not shared between the two identities, so a typo here leaves the plugin enabled and unconfigured.

`AUTH_TOKEN` delivered in managed `pluginConfigs.options` is honored, so credentials ship with the rollout and developers never handle a token. Note that this writes the token in plaintext into the managed settings payload — use an ingest-only token scoped to the target dataset, as [described below](#configuration).

### Locking the configuration down

Optional managed-only keys that prevent developers from working around the rollout:

| Key | Effect |
|---|---|
| `strictKnownMarketplaces` | Only marketplaces listed in managed settings may be used |
| `blockedMarketplaces` | Denies specific marketplaces |
| `disableSideloadFlags` | Blocks `--plugin-dir` and similar local-override flags |
| `allowManagedPermissionRulesOnly` | Ignores user and project permission rules |
| `pluginSuggestionMarketplaces` | Restricts which marketplaces can suggest plugins |
| `forceRemoteSettingsRefresh` | Requires a successful managed-settings fetch before starting |

### Network prerequisites

Each machine needs egress to:

- **`github.com`** — the plugin downloads its release binary from GitHub Releases on first run, and verifies it against `checksums.txt`. Without this the plugin installs but never exports.
- **Your Dash0 OTLP ingress** (`https://ingress.<region>.aws.dash0.com`) over HTTPS.

For containers and CI images, pre-bake the marketplace and plugin cache with `CLAUDE_CODE_PLUGIN_SEED_DIR` so images do not clone or download at runtime.

### Verifying a rollout on one machine

```bash
claude plugin list
```

The plugin (`dash0@claude-plugins-official` or `dash0-agent-plugin@dash0`, matching your payload) should appear with `Status: ✔ enabled` and `Scope: managed` — `managed` confirms the enablement came from managed settings rather than a local install. Exactly one identity should be listed. Then start a session and look for `dash0: connected (v0.1.22)`.

> **Do not let developers self-install as well.** If someone has already installed the other identity at user scope, both are enabled and every span is exported twice, with two independent configurations. See [Every span appears twice](#troubleshooting).

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


### Verify

On session start you should see:

```
dash0: connected (v0.1.22)
```

If credentials are missing: `dash0: telemetry is not active — configure the plugin to start sending data.`

### Options

| Option | Description | Default | Sensitive |
|---|---|---|---|
| `OTLP_URL` | Dash0 OTLP endpoint URL (e.g. `https://ingress.<region>.aws.dash0.com`) | — | No |
| `AUTH_TOKEN` | Dash0 authentication token | — | Yes (stored in keychain) |
| `DATASET` | Dash0 dataset name | — | No |
| `AGENT_NAME` | Agent name (used as `service.name`) | `claude-code` | No |
| `TEAM_NAME` | Team name — all spans are tagged with `dash0.team.name` | — | No |
| `OMIT_IO` | Omit prompt content and tool I/O | `true` | No |
| `OMIT_USER_INFO` | Anonymize user identity | `false` | No |
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
| `DASH0_OMIT_USER_INFO` | Anonymize user identity (`true`/`false`) |
| `DASH0_OMIT_IO` | Omit prompts and tool I/O (`true`/`false`) |
| `DASH0_SHOW_SESSION_LINK` | Print session URL after every turn (`true`/`false`) |
| `DASH0_DEBUG` | Print OTel payloads to stderr (`true`/`false`) |
| `DASH0_DEBUG_FILE` | Write debug output to this file path |

> `AUTH_TOKEN` has **no `DASH0_AUTH_TOKEN` env var fallback** — it is never read from a `DASH0_*` variable to prevent leaking into tool-spawned shell environments. Use `/plugin → Configure` (OS keychain) or the config file's `auth_token:` field.

## Privacy defaults

| Setting | Default | Behavior |
|---|---|---|
| `OMIT_USER_INFO` | `false` | Real `user.name` and `user.email` are sent. When `true`, `user.name` is a SHA-256 hash, `user.email` is omitted, working directory is redacted. |
| `OMIT_IO` | `true` | Prompt content and tool call inputs/outputs are stripped from spans. |

**Always collected** (regardless of settings): tool names, token counts, durations, model names, session structure, error status, VCS repository/branch info.

## Telemetry attributes

Spans follow [GenAI semantic conventions](https://opentelemetry.io/docs/specs/semconv/gen-ai/).
The OTLP pipeline is shared across runtimes, so the attribute set matches Claude Code apart from the per-runtime differences noted in [FEATURE_MATRIX.md](../FEATURE_MATRIX.md).

## Commands

| Command | Description |
|---|---|
| `/open-session` | Print and open the Dash0 session details URL for the current session |

## Skills

| Skill | Description |
|---|---|
| `/dash0-configure` | Walk through setting the OTLP URL, auth token, and other options, then write `~/.claude/dash0-agent-plugin.local.md` (user-level) or `.claude/dash0-agent-plugin.local.md` (project-level). Prefer `/plugin → Configure` if you want the auth token stored in the OS keychain. |

## Troubleshooting

**No spans in Dash0 after install.** Check the `dash0:` message on session start:
- `dash0: telemetry is not active` — OTLP URL is not configured.
- `dash0: connectivity check failed` — URL is set but connection failed (e.g. invalid auth token).
- No message at all — run `/reload-plugins`, or restart Claude Code.

**`Plugin "dash0-agent-plugin" not cached at (not recorded)`.** Enabling a plugin is not the same as installing it. When `enabledPlugins` names the plugin — typically from enterprise-managed settings or a project's `.claude/settings.json` — but the plugin has never been fetched on that machine, Claude Code has no install path to load from, and `(not recorded)` is the missing path being printed. Claude Code installs the plugin on session startup; restart Claude Code, and if the message persists run `/plugin` to refresh the cache.

To confirm the install landed:

```bash
claude plugin list
```

You should see `dash0-agent-plugin@dash0` (or `dash0@claude-plugins-official`) with a version and `Status: ✔ enabled`. The `Scope:` column tells you where the enablement came from — `managed` means enterprise-managed settings, which users cannot override.

**Every span appears twice.** The plugin is published under two identities — `dash0@claude-plugins-official` and `dash0-agent-plugin@dash0` — and both ship the same hooks. Enabling both registers every hook twice, so each event is exported twice. This happens easily when a developer installs the plugin themselves and their organization later pushes the other identity via managed settings. Check with `claude plugin list`; if both appear, disable one:

```bash
claude plugin uninstall dash0@claude-plugins-official --scope user
```

A managed-scope entry cannot be removed this way — remove the user-scoped one instead and let managed settings be the single source of truth. Note that the two identities keep separate configuration, so they can also be sending to different datasets or with different privacy settings.

**Debug mode.** Set `DASH0_DEBUG=true` to print all OTel payloads to stderr:

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
