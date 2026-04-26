# Dash0 Agent Plugin

Claude Code plugin that captures all agent activity and logs hook events to a newline-delimited JSON file for observability.

## Installation

```bash
# Test locally during development
claude --plugin-dir /path/to/dash0-agent-plugin

# Note: To build the binary for local development purposes you can run: 
# go build -o ~/.claude/plugins/data/dash0-agent-plugin-inline/bin/on-event-0.1.0-darwin-arm64 ./cmd/on-event/

# Install for all projects
claude plugin install /path/to/dash0-agent-plugin --scope user

# Install for a single project
claude plugin install /path/to/dash0-agent-plugin --scope project
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
| Worktree | `WorktreeCreate`, `WorktreeRemove` |
| Elicitation | `Elicitation`, `ElicitationResult` |
| Notification | `Notification` |

## Configuration

Create `.claude/dash0-agent-plugin.local.md` in your project root:

```markdown
---
enabled: true
otlp_url: "https://ingress.us1.dash0.com"
auth_token: "your-dash0-auth-token"
dataset: "your-dataset"
agent_name: "my-coding-agent"
---
```

| Setting | Description | Required |
|---|---|---|
| `enabled` | Enable or disable the plugin for this project (`true`/`false`) | No (defaults to `true`) |
| `otlp_url` | Dash0 OTLP endpoint URL | Yes |
| `auth_token` | Dash0 authentication token | Yes |
| `dataset` | Dash0 dataset to send data to | No |
| `agent_name` | Name for this agent, used as `service.name` and `gen_ai.agent.name` resource attributes | No (defaults to `claude-code`) |

### Environment variables

These can also be set as environment variables instead of (or in addition to) the configuration file:

| Variable | Description |
|---|---|
| `DASH0_OTLP_URL` | Dash0 OTLP endpoint URL — must include scheme (e.g. `https://ingress.us1.dash0.com`) |
| `DASH0_AUTH_TOKEN` | Dash0 authentication token |
| `DASH0_DATASET` | Dash0 dataset |
| `DASH0_AGENT_NAME` | Agent name |
| `DASH0_OMIT_USER_INFO` | Omit `user.name` and `user.email` from telemetry (`true`/`false`) |
| `DASH0_OMIT_IO` | Omit tool inputs/outputs and prompt content (`true`/`false`) |
| `DASH0_DEBUG` | Print OTel payloads to stderr for local debugging (`true`/`false`) |
| `DASH0_DEBUG_FILE` | Also write debug output to this file path (e.g. `/tmp/dash0-debug.log`) |

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

If telemetry isn't arriving in Dash0, run Claude Code with `--debug` to see plugin error messages:

```bash
DASH0_OTLP_URL="https://ingress.us1.dash0.com:4318" \
  DASH0_AUTH_TOKEN="your-token" \
  claude --debug --plugin-dir /path/to/dash0-agent-plugin 2>&1 | grep "on-event:"
```

Plugin errors are prefixed with `on-event:` in the output.

## Releasing

Releases are automated with [GoReleaser](https://goreleaser.com/) via GitHub Actions. To create a new release:

```bash
git tag v0.1.0
git push --tags
```

This triggers the release workflow which cross-compiles binaries for `darwin/linux × amd64/arm64` and publishes them to [GitHub Releases](https://github.com/dash0hq/dash0-agent-plugin/releases). The `on-event.sh` script downloads the matching binary on first run.
