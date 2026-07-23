# Claude Code source — developer reference

This directory holds the Claude Code-side plugin surface (slash commands and
skills) for the Claude Code → Dash0 integration. This file is the developer
reference: how to build and run local changes.

End-user install / configure / uninstall docs live in
[.claude-plugin/README.md](../.claude-plugin/README.md). Releasing is shared
across runtimes — see [DEVELOPMENT.md](../DEVELOPMENT.md#releasing).

## Local development

```bash
# Test locally without marketplace
claude --plugin-dir /path/to/dash0-agent-plugin

# Build the binary locally (instead of downloading from GitHub Releases)
VERSION=$(grep '^VERSION=' scripts/on-event.sh | cut -d'"' -f2)
go build -o ~/.claude/plugins/data/dash0-agent-plugin-inline/bin/on-event-${VERSION}-$(uname -s | tr '[:upper:]' '[:lower:]')-$(uname -m | sed 's/x86_64/amd64/') ./cmd/on-event/
```

### Running hooks from source

This repo ships a `.claude/settings.json` that wires every hook to run the Go source directly (`CLAUDE_PLUGIN_DATA=/tmp/dash0-dev go run ./cmd/on-event/`), so a Claude Code session started **inside this repo** exercises your local code instead of the released binary.

These are plain project-level command hooks, **not** plugin-managed hooks — the plugin itself is not installed as a plugin in this session.

In this case `CLAUDE_PLUGIN_DATA` is the filesystem root for per-session state, written to `<CLAUDE_PLUGIN_DATA>/<session_id>/` (`started`, `trace_context.json`, `events.jsonl`).
It is deliberately pointed at `/tmp/dash0-dev` to not pollute the repository.
