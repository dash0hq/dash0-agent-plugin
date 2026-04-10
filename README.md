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
