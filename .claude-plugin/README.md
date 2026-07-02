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

You can commit the plugin config to your repository so that setup is minimal for each developer.

Add to `<repo-root>/.claude/settings.json`:

```json
{
  "enabledPlugins": {
    "dash0@claude-plugins-official": true
  },
  "pluginConfigs": {
    "dash0@claude-plugins-official": {
      "options": {
        "OTLP_URL": "https://ingress.<region>.aws.dash0.com",
        "DATASET": "default"
      }
    }
  }
}
```

> If using the Dash0 marketplace instead, add `extraKnownMarketplaces` and enable `dash0-agent-plugin@dash0` — see [From the Dash0 marketplace](#from-the-dash0-marketplace) above. Use `dash0-agent-plugin@dash0` as the key in `pluginConfigs` accordingly.

Both `enabledPlugins` and `pluginConfigs` are committed to git. Each developer then:

1. Installs the plugin once: `/plugin install dash0@claude-plugins-official`
2. Adds their auth token: `/plugin` → **dash0** → **Configure** → set `AUTH_TOKEN` (stored in OS keychain)

> **Worktree / multi-clone caveat:** Project-scoped installs are keyed to the repository's absolute path. If you use git worktrees or multiple clones, the plugin fails to load in the second checkout. Use `--scope user` instead (`claude plugin install dash0@claude-plugins-official --scope user`).

## Configuration

After installing, you'll need:

- **Auth token** — create one from your organization's [Auth Tokens settings page](https://app.dash0.com/settings/auth-tokens). Use an ingest-only token with permissions limited to the dataset you want to send data to.
- **OTLP endpoint URL** — find it in the [Endpoints settings page](https://app.dash0.com/settings/endpoints) under the OTLP via HTTP tab (e.g. `https://ingress.<region>.aws.dash0.com`).

### Settings file

Plugin options can be set directly in `.claude/settings.json` under `pluginConfigs`. This is the same file that `/plugin → Configure` writes to, and it works at both user and project level:

- **User-level** (`~/.claude/settings.json`) — applies to all projects
- **Project-level** (`<repo-root>/.claude/settings.json`) — shared via git, applies to everyone working on the repo

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

> `AUTH_TOKEN` can be set here for user-level config (`~/.claude/settings.json`), but **do not commit it** in a project-level settings file. For project-level setups, omit `AUTH_TOKEN` from the committed file and let each developer set it via `/plugin → Configure` (stored in OS keychain).

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
dash0: connected (v0.1.12)
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

1. `pluginConfigs` in project-level `.claude/settings.json`
2. `pluginConfigs` in user-level `~/.claude/settings.json` (same as `/plugin → Configure` UI)
3. Project-level config file (`.claude/dash0-agent-plugin.local.md`)
4. User-level config file (`~/.claude/dash0-agent-plugin.local.md`)
5. `DASH0_*` environment variables

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

**Resource attributes**:

| Attribute | Description |
|---|---|
| `service.name` | Agent name (defaults to `claude-code`) |

**Span attributes (all spans)**:

| Attribute | Description |
|---|---|
| `gen_ai.provider.name` | LLM provider |
| `gen_ai.agent.name` | Agent name (sub-agent type on sub-agent spans) |
| `gen_ai.harness.name` | Coding agent platform (e.g. `claude-code`, `cursor`) |
| `dash0.gen_ai.vcs.repository.name` | Git repository name |
| `dash0.gen_ai.vcs.ref.head.name` | Git branch |
| `dash0.gen_ai.vcs.repository.url.full` | Full repository URL |
| `dash0.team.name` | Team name (when configured) |
| `user.name` | Real name or SHA-256 hash |

**LLM / chat spans**:

| Attribute | Description |
|---|---|
| `gen_ai.conversation.id` | Session identifier |
| `gen_ai.conversation.name` | Session title |
| `gen_ai.request.model` | Model used |
| `gen_ai.usage.input_tokens` | Input tokens consumed |
| `gen_ai.usage.output_tokens` | Output tokens produced |
| `gen_ai.usage.cache_read.input_tokens` | Tokens read from prompt cache |
| `gen_ai.usage.cache_creation.input_tokens` | Tokens written to prompt cache |

**Tool spans**:

| Attribute | Description |
|---|---|
| `gen_ai.tool.name` | Tool name (e.g. `Bash`, `Read`, `mcp__server__tool`) |
| `gen_ai.tool.type` | Always `function` |
| `gen_ai.tool.call.arguments` | Tool input (omitted when `OMIT_IO=true`, truncated to 16KB) |
| `gen_ai.tool.call.result` | Tool output (omitted when `OMIT_IO=true`, truncated to 16KB) |
| `dash0.gen_ai.vcs.pull_request.url` | PR/MR URL (survives `OMIT_IO=true`) |
| `dash0.gen_ai.vcs.issue.url` | Issue URL (survives `OMIT_IO=true`) |
| `dash0.gen_ai.vcs.commit.sha` | Commit SHA (survives `OMIT_IO=true`) |

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

See [DEVELOPMENT.md](../DEVELOPMENT.md) for local development, building, and releasing.
