# Dash0 Agent Plugin

Claude Code plugin that captures all agent activity and logs hook events to a newline-delimited JSON file for observability.

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

### First-time setup

After installing, **the plugin does not start sending telemetry until you complete two steps**:

1. **Configure credentials.** Run `/plugin` → **Installed** → **dash0** → **Configure**. Enter:
   - `OTLP_URL` — your Dash0 OTLP endpoint, e.g. `https://ingress.us1.dash0.com:4318`
   - `AUTH_TOKEN` — your Dash0 auth token (sensitive — stored in your OS keychain, not in `settings.json`)
   - `DATASET` *(optional)*
   - `AGENT_NAME` *(optional)*
2. **Reload the running session.** Run `/reload-plugins`. Without this, the current session's hooks still have empty config and silently emit nothing.

If you start a session before completing setup, the plugin writes this line to stderr on `SessionStart`:

```
dash0: not configured — no OTLP_URL set. In Claude Code: /plugin → Installed → dash0 → Configure, then /reload-plugins.
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

### Per-project overrides

For project-specific overrides (e.g. a different dataset per repo), create `.claude/dash0-agent-plugin.local.md`:

```markdown
---
enabled: true
otlp_url: "https://ingress.us1.dash0.com"
auth_token: "your-dash0-auth-token"
dataset: "your-dataset"
agent_name: "my-coding-agent"
---
```

The local file sets `DASH0_*` env vars for the hook subprocess, so it acts as the lowest-priority fallback. Set `enabled: false` to disable the plugin for a single project without uninstalling it.

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

1. Look for this line in Claude Code's stderr on `SessionStart`:
   ```
   dash0: not configured — no OTLP_URL set. In Claude Code: /plugin → Installed → dash0 → Configure, then /reload-plugins.
   ```
   If you see it, follow [First-time setup](#first-time-setup).
2. If you've already configured but spans still don't appear, run `/reload-plugins`. Saved values are not picked up by an already-running session until reload.

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
