# Dash0 Agent Plugin

Claude Code plugin that captures agent activity as OpenTelemetry traces — tool calls, LLM invocations, token usage, and errors.

## Installation

### From the official Claude Code marketplace (recommended)

```
/plugin install dash0@claude-plugins-official
```

> If you get "Plugin not found in marketplace", the official marketplace may not be registered yet. Run `/plugin marketplace add anthropics/claude-plugins-official` first, then retry the install.

### From the Dash0 marketplace

```
/plugin marketplace add dash0hq/claude-marketplace
/plugin install dash0-agent-plugin@dash0
```

> The plugin is registered as `dash0` in the official marketplace and `dash0-agent-plugin` in the Dash0 marketplace. Both install the same plugin; do not enable both at once or hooks will fire twice.

### First-time setup

After installing, give the plugin your Dash0 credentials one of two ways. See [Configuration](#configuration) for the full reference (all options, precedence, and env-var equivalents).

Find your exact `otlp_url` (and auth token) in your Dash0 org settings — the region segment varies (e.g. `eu-west-1`, `us-west-2`).

**Config file (recommended)** — create `~/.claude/dash0-agent-plugin.local.md` (applies to all projects), or `.claude/dash0-agent-plugin.local.md` for a single project:

```markdown
---
otlp_url: "https://ingress.<region>.aws.dash0.com"
auth_token: "your-dash0-auth-token"
dataset: "default"
---
```

**Plugin UI** — run `/plugin` → **Installed** → **dash0** (or **dash0-agent-plugin** from the Dash0 marketplace — see the [naming note](#from-the-dash0-marketplace) above) → **Configure**, then `/reload-plugins` to apply.

If credentials are missing, you'll see this on session start:

```
dash0: telemetry is not active — configure the plugin to start sending data.
```

### Headless / CI installation

In environments without interactive access (containers, CI, scripts), use the CLI:

```bash
git config --global url."https://github.com/".insteadOf "git@github.com:"
claude plugin marketplace add dash0hq/claude-marketplace --scope user
claude plugin install dash0-agent-plugin@dash0 --scope user
```

> **Note:** Claude Code downloads marketplace plugins via SSH by default. If SSH keys are not configured for GitHub, the `git config` line above forces HTTPS. This is required in Docker containers, CI runners, or any environment without SSH access to GitHub.

This installs the plugin; configure credentials as in [First-time setup](#first-time-setup).

### Fleet / global deployment

Rolling the plugin out across many machines (MDM, golden image, dotfiles, config-management tooling) is two non-interactive steps: **install** the plugin, then give it **credentials**.

**1. Install + enable** with the non-interactive CLI shown in [Headless / CI installation](#headless--ci-installation) above (`claude plugin marketplace add` then `claude plugin install … --scope user`). The `on-event` binary is fetched from [GitHub Releases](https://github.com/dash0hq/dash0-agent-plugin/releases) on first run (checksum-verified), so each device needs outbound access to `github.com` and to your Dash0 ingress endpoint.

> **Hand-writing `~/.claude/settings.json` is _not_ enough to install the plugin.** `enabledPlugins` only *enables* an already-installed plugin and `extraKnownMarketplaces` only *registers* the marketplace — neither downloads anything. Run `claude plugin install` (above) to actually install it.

**2. Supply credentials** with any one of:

- **A config file** at `~/.claude/dash0-agent-plugin.local.md` — format in [First-time setup](#first-time-setup), full options in [Configuration file](#configuration-file). The token is stored in cleartext, so `chmod 600` it and keep it user-owned.

- **`--config` on the install command**, which stores the token in the OS keychain (`~/.claude/.credentials.json` fallback on Linux) instead of cleartext:

  ```bash
  claude plugin install dash0-agent-plugin@dash0 --scope user \
    --config OTLP_URL=https://ingress.<region>.aws.dash0.com \
    --config AUTH_TOKEN=<your-dash0-auth-token> \
    --config DATASET=default
  ```
  > Values passed via `--config` can appear in shell history and process listings during provisioning — if that's a concern, use the config file or a secret manager instead.

- **Environment variables** — `DASH0_OTLP_URL` plus `CLAUDE_PLUGIN_OPTION_AUTH_TOKEN` (and optionally `DASH0_DATASET`); see [Environment variable fallback](#environment-variable-fallback).

After credentials are in place, start a Claude Code session — you should see `dash0: connected (v0.1.8)`.

### Local development

```bash
# Test locally without marketplace
claude --plugin-dir /path/to/dash0-agent-plugin

# Build the binary locally (instead of downloading from GitHub Releases)
go build -o ~/.claude/plugins/data/dash0-agent-plugin-inline/bin/on-event-0.1.8-$(uname -s | tr '[:upper:]' '[:lower:]')-$(uname -m | sed 's/x86_64/amd64/') ./cmd/on-event/
```

## What it does

The plugin's job is to **emit OpenTelemetry spans to your Dash0 OTLP endpoint** for the events that represent agent activity — tool calls, LLM turns, and session lifecycle.

To do that it hooks every supported Claude Code event (see [Hooked events](#hooked-events) below). A subset of those events are turned into spans; the rest are recorded only as local scratch state used to reconstruct trace context across hook invocations. That scratch state is a per-session JSON-lines file (each event written as one line with a `timestamp` added):

```
$CLAUDE_PLUGIN_DATA/<session-id>/events.jsonl
```

This file is internal — it is cleaned up on `SessionEnd` and is not the telemetry the plugin produces.

### Hooked events

| Category | Events |
|---|---|
| Session | `SessionStart`, `SessionEnd` |
| Turn | `UserPromptSubmit`, `Stop`, `StopFailure` |
| Tool | `PreToolUse`, `PostToolUse`, `PostToolUseFailure` |
| Permission | `PermissionRequest`, `PermissionDenied` |
| Subagent | `SubagentStart`, `SubagentStop` |
| Task | `TaskCreated`, `TaskCompleted`, `TeammateIdle` |
| Config | `ConfigChange`, `CwdChanged`, `FileChanged`, `InstructionsLoaded` |
| Compaction | `PreCompact`, `PostCompact` |
| Elicitation | `Elicitation`, `ElicitationResult` |
| Notification | `Notification` |

> **Hooked vs. exported.** All events above are hooked (so trace context stays accurate), but only a subset is currently exported as spans: tool spans from `PostToolUse` / `PostToolUseFailure`, chat/LLM spans from `Stop` / `StopFailure`, and the connectivity check at `SessionStart`. `UserPromptSubmit` and `SessionEnd` drive trace state; the remaining events are recorded locally but do not yet produce telemetry.

### Telemetry attributes

The plugin emits OpenTelemetry spans following [GenAI semantic conventions](https://opentelemetry.io/docs/specs/semconv/gen-ai/). Key attributes:

**Resource attributes** (on all spans):

| Attribute | Description |
|---|---|
| `service.name` | Agent name (configurable via `AGENT_NAME`, defaults to `claude-code`) |
| `gen_ai.provider.name` | LLM provider |

**Span attributes (on all spans)**:

| Attribute                                  | Description |
|--------------------------------------------|---|
| `dash0.gen_ai.vcs.repository.name`         | Git repository name |
| `dash0.gen_ai.vcs.ref.head.name`                        | Git branch |
| `dash0.gen_ai.vcs.repository.url.full`                  | Full repository URL |
| `dash0.team.name`                          | Team name (only present when `TEAM_NAME` is configured) |
| `user.name`                                | Real name or SHA-256 hash depending on privacy setting |

**Span attributes (LLM / chat spans)**:

| Attribute                                  | Description |
|--------------------------------------------|---|
| `gen_ai.conversation.id`                   | Session identifier |
| `gen_ai.conversation.name`                 | Session title |
| `gen_ai.request.model`                     | Model used |
| `gen_ai.usage.input_tokens`                | Input tokens consumed |
| `gen_ai.usage.output_tokens`               | Output tokens produced |
| `gen_ai.usage.cache_read.input_tokens`     | Tokens read from prompt cache |
| `gen_ai.usage.cache_creation.input_tokens` | Tokens written to prompt cache |

**Span attributes (tool spans)**:

| Attribute | Description |
|---|---|
| `gen_ai.tool.name` | Tool name (e.g. `Bash`, `Read`, `mcp__server__tool`) |
| `gen_ai.tool.type` | Always `function` for Claude Code tools |
| `gen_ai.tool.call.arguments` | Tool input (omitted when `OMIT_IO=true`, truncated to 16KB otherwise) |
| `gen_ai.tool.call.result` | Tool output (omitted when `OMIT_IO=true`, truncated to 16KB otherwise) |
| `dash0.gen_ai.vcs.pull_request.url` | PR/MR URL extracted from tool response (survives `OMIT_IO=true`). Supports GitHub, GitLab, and Bitbucket. |
| `dash0.gen_ai.vcs.issue.url` | Issue URL extracted from tool response (survives `OMIT_IO=true`). |
| `dash0.gen_ai.vcs.commit.sha` | Commit SHA extracted from `git commit` output (survives `OMIT_IO=true`). |

### Privacy defaults

By default, the plugin sends real user identity and omits prompt/tool I/O content:

| Setting | Default | Behavior |
|---|---|---|
| `OMIT_USER_INFO` | `false` | Real `user.name` and `user.email` are sent. When set to `true`, `user.name` is emitted as a SHA-256 hash, `user.email` is omitted, and working directory has its home directory prefix replaced with `~`. |
| `OMIT_IO` | `true` | Prompt content and tool call inputs/outputs are stripped from spans. |

**What is always collected** (regardless of settings): tool names, token counts, durations, model names, session structure, error status, VCS repository/branch info.

**What is omitted by default**: prompt text, tool call arguments and responses.

To anonymize user identity, set `OMIT_USER_INFO` to `"true"` via `/plugin` → Installed → dash0 → Configure (the entry name depends on the marketplace — see [First-time setup](#first-time-setup)).

## Configuration

The plugin declares its configuration via Claude Code's `userConfig` mechanism. Values are entered in the Configure UI described in [First-time setup](#first-time-setup) above. Claude Code stores non-sensitive values in `~/.claude/settings.json` under `pluginConfigs[<plugin>@<marketplace>].options`; sensitive values go to the OS keychain (or `~/.claude/.credentials.json` as a fallback). The plugin's hook subprocess receives them as `CLAUDE_PLUGIN_OPTION_<KEY>` environment variables.

| Option | Description | Required | Sensitive |
|---|---|---|---|
| `OTLP_URL` | Dash0 OTLP endpoint URL (e.g. `https://ingress.<region>.aws.dash0.com`) | Yes | No |
| `AUTH_TOKEN` | Dash0 authentication token | Yes | Yes (stored in keychain) |
| `DATASET` | Dash0 dataset name | No | No |
| `AGENT_NAME` | Used as `service.name` and `gen_ai.agent.name` resource attributes (defaults to `claude-code`) | No | No |
| `TEAM_NAME` | When set, all spans are tagged with the `dash0.team.name` attribute | No | No |
| `OMIT_IO` | Omit prompt content and tool I/O (default `true`) — see [Privacy defaults](#privacy-defaults) | No | No |
| `OMIT_USER_INFO` | Anonymize user identity (default `false`) — see [Privacy defaults](#privacy-defaults) | No | No |

After changing any value via Configure, run `/reload-plugins` to apply it to the current session.

There are three ways to supply these values — the Configure UI, a [config file](#configuration-file), or [`DASH0_*` environment variables](#environment-variable-fallback). When a value is set in more than one, **precedence is, highest to lowest:**

1. `/plugin → Configure` UI
2. Project-level config file (`.claude/dash0-agent-plugin.local.md`)
3. User-level config file (`~/.claude/dash0-agent-plugin.local.md`)
4. `DASH0_*` environment variables

The two config files do **not** merge with each other (see [Configuration file](#configuration-file)); otherwise sources compose per key (see [Mixing sources](#mixing-sources)).

### Environment variable fallback

For non-sensitive options, the plugin falls back to `DASH0_*` environment variables when the `userConfig` value is not set. This is useful for `--plugin-dir` development or CI.

> **Note:** `AUTH_TOKEN` has **no `DASH0_AUTH_TOKEN` env var fallback** — unlike the other options, it is never read from a `DASH0_*` variable. This prevents the token from leaking into tool-spawned shell environments where other tools might pick it up. Configure it via either `/plugin → Configure` (stored in the OS keychain) or the [config file](#configuration-file)'s `auth_token:` field (passed to the hook as `CLAUDE_PLUGIN_OPTION_AUTH_TOKEN`). You can set the token via one source and the remaining options via `DASH0_*` env vars — see [Mixing sources](#mixing-sources).

| Variable | Description |
|---|---|
| `DASH0_OTLP_URL` | Dash0 OTLP endpoint URL — must include scheme (e.g. `https://ingress.<region>.aws.dash0.com`) |
| `DASH0_DATASET` | Dash0 dataset |
| `DASH0_AGENT_NAME` | Agent name |
| `DASH0_TEAM_NAME` | Team name — when set, all spans are tagged with the `dash0.team.name` attribute |
| `DASH0_OMIT_USER_INFO` | Anonymize user identity (default: `false`). When true, `user.name` is emitted as a hash and `user.email` is omitted. |
| `DASH0_OMIT_IO` | Omit prompts and tool I/O (default: `true`). When true, prompt content and tool call inputs/outputs are stripped from spans. Set to `false` to include full content. |
| `DASH0_DEBUG` | Print OTel payloads to stderr for local debugging (`true`/`false`) |
| `DASH0_DEBUG_FILE` | Also write debug output to this file path (e.g. `/tmp/dash0-debug.log`) |

### Configuration file

You can configure the plugin via a markdown file with YAML frontmatter. The plugin checks two locations:

1. **Project-level**: `.claude/dash0-agent-plugin.local.md` (in current directory)
2. **Global**: `~/.claude/dash0-agent-plugin.local.md` (user home)

The two config files do **not** merge: if a project-level file exists, it is used and the global file is ignored entirely — even for keys the project file leaves out. Keep all the keys you need in whichever file is active (don't, for example, put `auth_token` only in the global file and expect a project file to inherit it).

**Global config (recommended for personal use)**

Create `~/.claude/dash0-agent-plugin.local.md` to configure the plugin once for all projects:

```markdown
---
otlp_url: "https://ingress.<region>.aws.dash0.com"
auth_token: "your-dash0-auth-token"
dataset: "default"
agent_name: "claude-code"
omit_io: true
omit_user_info: false
---
```

**Project-level config**

Create `.claude/dash0-agent-plugin.local.md` in a project directory for project-specific overrides (e.g. a different dataset per repo):

```markdown
---
enabled: true
otlp_url: "https://ingress.<region>.aws.dash0.com"
auth_token: "your-dash0-auth-token"
dataset: "my-project-dataset"
agent_name: "my-coding-agent"
omit_io: false
omit_user_info: true
---
```

**Config file options**

| Option | Description | Default |
|---|---|---|
| `enabled` | Enable/disable the plugin for this project | `true` |
| `otlp_url` | Dash0 OTLP endpoint URL | — |
| `auth_token` | Dash0 authentication token | — |
| `dataset` | Dash0 dataset name | — |
| `agent_name` | Agent name (used as `service.name`) | `claude-code` |
| `team_name` | Team name — when set, all spans are tagged with `dash0.team.name` | — |
| `omit_io` | Omit prompt content and tool I/O | `true` |
| `omit_user_info` | Anonymize user identity | `false` |

Set `enabled: false` to disable the plugin for a single project without uninstalling it.

The config file sets environment variables for the hook subprocess, so it acts as a fallback after `/plugin → Configure` values and before `DASH0_*` environment variables.

### Mixing sources

A config file and `DASH0_*` environment variables compose **per key**: the active config file only sets the keys it actually contains, and any key it omits falls through to the environment. (The two config files themselves don't merge — see [Configuration file](#configuration-file).) So you can, for example, put just the `auth_token` in a config file and supply everything else via `DASH0_*` env vars:

```bash
# ~/.claude/dash0-agent-plugin.local.md contains only:  auth_token: "…"
DASH0_OTLP_URL="https://ingress.<region>.aws.dash0.com" \
  DASH0_DATASET="default" \
  claude
```

If the same key is set in more than one source, the highest-precedence one wins (see [Configuration](#configuration) for the full order).

### Debug mode

Set `DASH0_DEBUG=true` to print all OTel payloads to stderr. Works with or without an OTLP endpoint configured — useful for verifying what telemetry the plugin produces.

```bash
DASH0_DEBUG=true claude --debug --plugin-dir /path/to/dash0-agent-plugin
```

To write debug output to a file (useful for tailing in a separate terminal without `--debug`):

```bash
DASH0_DEBUG=true DASH0_DEBUG_FILE=/tmp/dash0-debug.log claude --plugin-dir /path/to/dash0-agent-plugin

# In another terminal:
tail -f /tmp/dash0-debug.log
```

Output is prefixed with `[dash0:trace]` or `[dash0:log]` for filtering:

```
[dash0:trace] {"resourceSpans":[...]}
[dash0:log]   {"resourceLogs":[...]}
```

### Troubleshooting

**No spans in Dash0 after install.** The plugin was likely installed but not configured, or configured but not reloaded. Check:

1. Look for a `dash0:` message in the Claude Code UI on session start:
   - `dash0: telemetry is not active` — OTLP URL is not configured. Set it via `/plugin → Configure` or in the config file.
   - `dash0: connectivity check failed` — URL is set but connection failed (e.g., invalid auth token returns 401).
2. If you've configured via `/plugin → Configure` but spans still don't appear, run `/reload-plugins`. Saved values are not picked up by an already-running session until reload.

**More verbose debugging.** Run Claude Code with `--debug` to see plugin error messages:

```bash
DASH0_OTLP_URL="https://ingress.<region>.aws.dash0.com" \
  CLAUDE_PLUGIN_OPTION_AUTH_TOKEN="your-token" \
  claude --debug --plugin-dir /path/to/dash0-agent-plugin 2>&1 | grep "on-event:\|dash0:"
```

> The auth token uses `CLAUDE_PLUGIN_OPTION_AUTH_TOKEN`, not `DASH0_AUTH_TOKEN` — there is no `DASH0_*` fallback for the token (see [Environment variable fallback](#environment-variable-fallback)).

Plugin errors are prefixed with `on-event:` or `dash0:` in the output.

## Releasing

Releases are automated with [GoReleaser](https://goreleaser.com/) via GitHub Actions. To create a new release:

```bash
git tag v0.1.0
git push --tags
```

This triggers the release workflow which cross-compiles binaries for `darwin/linux × amd64/arm64` and publishes them to [GitHub Releases](https://github.com/dash0hq/dash0-agent-plugin/releases). The `on-event.sh` script downloads the matching binary on first run.
