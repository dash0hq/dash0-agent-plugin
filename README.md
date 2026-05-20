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

### Headless / CI installation

In environments without interactive access (containers, CI, scripts), use the CLI:

```bash
git config --global url."https://github.com/".insteadOf "git@github.com:"
claude plugin marketplace add dash0hq/claude-marketplace --scope user
claude plugin install dash0-agent-plugin@dash0 --scope user
```

> **Note:** Claude Code downloads marketplace plugins via SSH by default. If SSH keys are not configured for GitHub, the `git config` line above forces HTTPS. This is required in Docker containers, CI runners, or any environment without SSH access to GitHub.

### First-time setup

After installing, configure credentials using **one of these options**:

**Option A: Config file (recommended)**

Create a config file at one of these locations:

| Location | Scope |
|---|---|
| `.claude/dash0-agent-plugin.local.md` | Project-level (current directory) |
| `~/.claude/dash0-agent-plugin.local.md` | User-level (all projects) |

Example:

```markdown
---
otlp_url: "https://ingress.us1.dash0.com"
auth_token: "your-dash0-auth-token"
dataset: "default"
---
```

**Precedence order** (highest to lowest):
1. `/plugin → Configure` UI settings
2. Project-level config file
3. User-level config file
4. Environment variables (`DASH0_*`)

**Option B: Plugin UI**

Run `/plugin` → **Installed** → **dash0** → **Configure** and enter your credentials. Then run `/reload-plugins` to apply.

---

If credentials are missing, you'll see on session start:

```
dash0: telemetry is not active — configure the plugin to start sending data.
```

### Local development

```bash
# Test locally without marketplace
claude --plugin-dir /path/to/dash0-agent-plugin

# Build the binary locally (instead of downloading from GitHub Releases)
go build -o ~/.claude/plugins/data/dash0-agent-plugin-inline/bin/on-event-0.1.0-$(uname -s | tr '[:upper:]' '[:lower:]')-$(uname -m | sed 's/x86_64/amd64/') ./cmd/on-event/
```

## What it does

The plugin registers a hook for every supported Claude Code event. Each event's payload is written as a single JSON line (with a `timestamp` field added) to:

```
~/.claude/plugins/data/dash0-agent-plugin/events.jsonl
```

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
| `user.name`                                | Real name or SHA-256 hash depending on privacy setting |

**Span attributes (LLM / chat spans)**:

| Attribute                                  | Description |
|--------------------------------------------|---|
| `gen_ai.conversation.id`                   | Session identifier |
| `gen_ai.conversation.name`                 | Session title |
| `gen_ai.request.model`                     | Model used |
| `gen_ai.usage.input_tokens`                | Input tokens consumed |
| `gen_ai.usage.output_tokens`               | Output tokens produced |
| `gen_ai.usage.cache_read_input_tokens`     | Tokens read from prompt cache |
| `gen_ai.usage.cache_creation_input_tokens` | Tokens written to prompt cache |

**Span attributes (tool spans)**:

| Attribute | Description |
|---|---|
| `gen_ai.tool.name` | Tool name (e.g. `Bash`, `Read`, `mcp__server__tool`) |
| `gen_ai.tool.type` | Always `function` for Claude Code tools |
| `gen_ai.tool.call.arguments` | Tool input (omitted when `OMIT_IO=true`, truncated to 16KB otherwise) |
| `gen_ai.tool.call.result` | Tool output (omitted when `OMIT_IO=true`, truncated to 16KB otherwise) |

### Privacy defaults

By default, the plugin anonymizes telemetry:

| Setting | Default | Behavior |
|---|---|---|
| `OMIT_USER_INFO` | `true` | `user.name` is emitted as a SHA-256 hash (stable per-user grouping without revealing identity). `user.email` is omitted. |
| `OMIT_IO` | `true` | Prompt content and tool call inputs/outputs are stripped from spans. |

**What is always collected** (regardless of settings): tool names, token counts, durations, model names, session structure, error status, VCS repository/branch info.

**What is omitted by default**: real user name, email, prompt text, tool call arguments and responses.

To opt in to full data collection, set either option to `"false"` via `/plugin` → Installed → dash0-agent-plugin → Configure.

## Configuration

The plugin declares its configuration via Claude Code's `userConfig` mechanism. Values are entered in the Configure UI described in [First-time setup](#first-time-setup) above. Claude Code stores non-sensitive values in `~/.claude/settings.json` under `pluginConfigs[<plugin>@<marketplace>].options`; sensitive values go to the OS keychain (or `~/.claude/.credentials.json` as a fallback). The plugin's hook subprocess receives them as `CLAUDE_PLUGIN_OPTION_<KEY>` environment variables.

| Option | Description | Required | Sensitive |
|---|---|---|---|
| `OTLP_URL` | Dash0 OTLP endpoint URL (e.g. `https://ingress.us1.dash0.com:4318`) | Yes | No |
| `AUTH_TOKEN` | Dash0 authentication token | Yes | Yes (stored in keychain) |
| `DATASET` | Dash0 dataset name | No | No |
| `AGENT_NAME` | Used as `service.name` and `gen_ai.agent.name` resource attributes (defaults to `claude-code`) | No | No |

After changing any value via Configure, run `/reload-plugins` to apply it to the current session.

### Environment variable fallback

For non-sensitive options, the plugin falls back to `DASH0_*` environment variables when the `userConfig` value is not set. This is useful for `--plugin-dir` development or CI.

> **Note:** `AUTH_TOKEN` has no env var fallback — it must be configured via `/plugin → Configure` (stored in the OS keychain). This prevents the token from leaking into tool-spawned shell environments where other tools (e.g. Dash0 CLI) might pick it up.

| Variable | Description |
|---|---|
| `DASH0_OTLP_URL` | Dash0 OTLP endpoint URL — must include scheme (e.g. `https://ingress.us1.dash0.com`) |
| `DASH0_DATASET` | Dash0 dataset |
| `DASH0_AGENT_NAME` | Agent name |
| `DASH0_OMIT_USER_INFO` | Anonymize user identity (default: `true`). When true, `user.name` is emitted as a hash and `user.email` is omitted. Set to `false` to include real identity. |
| `DASH0_OMIT_IO` | Omit prompts and tool I/O (default: `true`). When true, prompt content and tool call inputs/outputs are stripped from spans. Set to `false` to include full content. |
| `DASH0_DEBUG` | Print OTel payloads to stderr for local debugging (`true`/`false`) |
| `DASH0_DEBUG_FILE` | Also write debug output to this file path (e.g. `/tmp/dash0-debug.log`) |

### Configuration file

You can configure the plugin via a markdown file with YAML frontmatter. The plugin checks two locations:

1. **Project-level**: `.claude/dash0-agent-plugin.local.md` (in current directory)
2. **Global**: `~/.claude/dash0-agent-plugin.local.md` (user home)

Project-level config takes precedence over global config.

**Global config (recommended for personal use)**

Create `~/.claude/dash0-agent-plugin.local.md` to configure the plugin once for all projects:

```markdown
---
otlp_url: "https://ingress.us1.dash0.com"
auth_token: "your-dash0-auth-token"
dataset: "default"
agent_name: "claude-code"
omit_io: true
omit_user_info: true
---
```

**Project-level config**

Create `.claude/dash0-agent-plugin.local.md` in a project directory for project-specific overrides (e.g. a different dataset per repo):

```markdown
---
enabled: true
otlp_url: "https://ingress.us1.dash0.com"
auth_token: "your-dash0-auth-token"
dataset: "my-project-dataset"
agent_name: "my-coding-agent"
omit_io: false
omit_user_info: false
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
| `omit_io` | Omit prompt content and tool I/O | `true` |
| `omit_user_info` | Anonymize user identity | `true` |

Set `enabled: false` to disable the plugin for a single project without uninstalling it.

The config file sets environment variables for the hook subprocess, so it acts as a fallback after `/plugin → Configure` values and before `DASH0_*` environment variables.

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
DASH0_OTLP_URL="https://ingress.us1.dash0.com:4318" \
  DASH0_AUTH_TOKEN="your-token" \
  claude --debug --plugin-dir /path/to/dash0-agent-plugin 2>&1 | grep "on-event:\|dash0:"
```

Plugin errors are prefixed with `on-event:` or `dash0:` in the output.

## Releasing

Releases are automated with [GoReleaser](https://goreleaser.com/) via GitHub Actions. To create a new release:

```bash
git tag v0.1.0
git push --tags
```

This triggers the release workflow which cross-compiles binaries for `darwin/linux × amd64/arm64` and publishes them to [GitHub Releases](https://github.com/dash0hq/dash0-agent-plugin/releases). The `on-event.sh` script downloads the matching binary on first run.
