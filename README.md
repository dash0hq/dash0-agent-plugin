# Dash0 Agent Plugin

Claude Code plugin that captures all agent activity and logs hook events to a newline-delimited JSON file for observability.

## Installation

```bash
# Test locally during development
claude --plugin-dir /path/to/dash0-agent-plugin

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
otlp_url: "https://ingress.us1.dash0.com/v1/traces"
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
| `DASH0_OTLP_URL` | Dash0 OTLP endpoint URL |
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
