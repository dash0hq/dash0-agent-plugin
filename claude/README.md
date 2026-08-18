# Claude Code source — developer reference

This directory holds everything shipped to Claude Code — the bootstrap wrapper
(`claude-on-event.sh`), the hook registration (`hooks.json`, declared as the
`hooks` path in `.claude-plugin/plugin.json`), slash commands, skills, and the
diagnostic scripts the commands invoke (`tools/`). This file is the developer
reference: how to build and run local changes.

End-user install / configure / uninstall docs live in
[.claude-plugin/README.md](../.claude-plugin/README.md). Releasing is shared
across runtimes — see [DEVELOPMENT.md](../DEVELOPMENT.md#releasing).

## Local development

Load the working copy as a plugin, and build the binary to the exact path its
bootstrap resolves so nothing is downloaded from GitHub Releases:

```bash
# Test locally without marketplace
claude --plugin-dir /path/to/dash0-agent-plugin

# Build the binary locally (instead of downloading from GitHub Releases)
VERSION=$(grep '^VERSION=' claude/claude-on-event.sh | cut -d'"' -f2)
go build -o ~/.claude/plugins/data/dash0-agent-plugin-inline/bin/on-event-${VERSION}-$(uname -s | tr '[:upper:]' '[:lower:]')-$(uname -m | sed 's/x86_64/amd64/') ./cmd/claude-on-event/
```

For a `--plugin-dir` load, Claude computes
`CLAUDE_PLUGIN_DATA=~/.claude/plugins/data/<plugin-name>-inline` itself and
ignores any preset value, which is why the build target above is that exact path.
Per-session state lands in `<CLAUDE_PLUGIN_DATA>/<session_id>/` (`started`,
`trace_context.json`, `events.jsonl`).

> Do **not** wire the hooks up through a project-level `.claude/settings.json` in
> this repo. If you also have the plugin installed — which most of us do, since we
> dogfood it — both registrations fire for every event and you get duplicate
> spans, with no obvious sign that is what is happening. `--plugin-dir` replaces
> the installed copy for that session instead of stacking on top of it.

## Known limitations

### Claude Code's own model calls are not captured

Token usage is read from the session transcript (`transcript.ReadTurnUsage`, called
from `internal/pipeline/pipeline.go`), which only walks `type == "assistant"` entries.
Claude Code also makes its own auxiliary model calls — session-title generation is the
one we have confirmed — and those are never written to the transcript as assistant
entries. They have no hook event either, so the pipeline cannot see them at all.

The result: their tokens and cost are missing from the `chat` span, and no span is
emitted for them. `/usage` in the same session does report them, because Claude Code
tracks cost in-process. A one-turn session can therefore show two models in `/usage`
but only one in Dash0:

```
claude-haiku-4-5:  536 input, 18 output, 0 cache read, 0 cache write ($0.0006)
   claude-opus-5:  2 input, 186 output, 22.6k cache read, 16.8k cache write ($0.1838)
```

The title call is doubly invisible: the pipeline reads its *output* — the transcript's
`ai-title` entry becomes `gen_ai.conversation.name` — while its usage stays unreported.

No per-session record of these calls exists on disk (`~/.claude/sessions/<pid>.json` is
process metadata; `~/.claude/stats-cache.json` is a global rollup with no session
dimension), so closing the gap needs a second telemetry source rather than a fix to the
transcript reader.
