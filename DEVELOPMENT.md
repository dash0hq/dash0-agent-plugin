# Development

## Local development

```bash
# Test locally without marketplace
claude --plugin-dir /path/to/dash0-agent-plugin

# Build the binary locally (instead of downloading from GitHub Releases)
VERSION=$(grep '^VERSION=' scripts/on-event.sh | cut -d'"' -f2)
go build -o ~/.claude/plugins/data/dash0-agent-plugin-inline/bin/on-event-${VERSION}-$(uname -s | tr '[:upper:]' '[:lower:]')-$(uname -m | sed 's/x86_64/amd64/') ./cmd/on-event/
```

### Running hooks from source (`.claude/settings.json`)

This repo ships a `.claude/settings.json` that wires every hook to run the Go source directly (`CLAUDE_PLUGIN_DATA=/tmp/dash0-dev go run ./cmd/on-event/`), so a Claude Code session started **inside this repo** exercises your local code instead of the released binary.

These are plain project-level command hooks, **not** plugin-managed hooks — the plugin itself is not installed as a plugin in this session.

In this case `CLAUDE_PLUGIN_DATA` is the filesystem root for per-session state, written to `<CLAUDE_PLUGIN_DATA>/<session_id>/` (`started`, `trace_context.json`, `events.jsonl`).
It is deliberately pointed at `/tmp/dash0-dev` to not pollute the repository.

## Notification capabilities across coding agents

The pipeline produces status messages (e.g. the `dash0: connected → <session
link>` welcome banner) uniformly, but only **Claude Code** can show them to the
user at session start. The others expose only a model-context field there, or a
diagnostic log the user doesn't normally see — so the banner renders on Claude
Code alone.

| Agent | User-visible message | Model-context injection | Notes |
|-------|----------------------|-------------------------|-------|
| Claude Code | `systemMessage` (any hook) | `additionalContext` | Full support. |
| Cursor | `user_message` — only when a hook **denies** an action | `additional_context` (sessionStart) | No unblocked startup banner. [docs](https://cursor.com/docs/hooks.md) |
| Codex | none (hook stderr not surfaced; `notify` is OS-only) | none | Nothing user-visible. [docs](https://learn.chatgpt.com/docs/config-file/config-reference) |
| Copilot CLI | none at sessionStart (stderr only on exit 2) | `additionalContext` (sessionStart) | Open bug [copilot-cli#1352](https://github.com/github/copilot-cli/issues/1352). [docs](https://docs.github.com/en/copilot/reference/hooks-reference) |

For the three non-Claude agents, injecting the session link as model context is
the only portable fallback — it lets the agent surface the link if asked, but
does not display it directly.

## Releasing

Releases are automated with [GoReleaser](https://goreleaser.com/) via GitHub Actions. To create a new release, update the version in:

- `.claude-plugin/plugin.json` — `version` field
- `.cursor-plugin/plugin.json` — `version` field
- `scripts/on-event.sh` — `VERSION=` line (Claude Code binary downloader)
- `scripts/cursor-on-event.sh` — `VERSION=` line (Cursor binary downloader)
- `scripts/codex-on-event.sh` — `VERSION=` line (Codex binary downloader)

`scripts/release.sh <version>` updates all of these in one commit, tags, and pushes.

Then tag the commit in main:

```bash
git tag v<version>
git push --tags
```

This triggers the release workflow which cross-compiles binaries for `darwin/linux × amd64/arm64` and publishes them to [GitHub Releases](https://github.com/dash0hq/dash0-agent-plugin/releases). The `on-event.sh` script downloads the matching binary on first run.

## Cursor install layout (hybrid)

The `install-cursor.sh` script lays the plugin down at `~/.cursor/plugins/local/dash0-agent-plugin/`, which Cursor scans on startup:

```
~/.cursor/plugins/local/dash0-agent-plugin/
├── .cursor-plugin/plugin.json          (manifest — declares skills, no hooks)
├── cursor/plugin-hooks.json            (installer template — see below)
├── cursor/skills/dash0-configure/…     (shipped skills)
└── scripts/cursor-on-event.sh          (bootstrap wrapper Cursor invokes)
```

**Hooks are registered in `~/.cursor/hooks.json`, not in the plugin manifest.** Cursor 3.9.x loads the local plugin (making the name + skills surface in the UI with a "local plugin" label) but silently ignores any `hooks` field in the manifest — verified with a probe plugin whose only hook was a `printf … >> /tmp/probe.log` script; no invocation was ever recorded despite `[pluginsSubsystem] loadUserLocalPlugin` log lines confirming the manifest loaded. Hooks fire only from `~/.cursor/hooks.json` (user scope) and `<project>/.cursor/hooks.json` (project scope).

`install-cursor.sh` therefore reads `cursor/plugin-hooks.json` (source of truth for which events the plugin listens to), translates each `./scripts/cursor-on-event.sh` command to `$HOME/.cursor/plugins/local/dash0-agent-plugin/scripts/cursor-on-event.sh` (Cursor expands `$HOME` at invocation time), and merges the entries into `~/.cursor/hooks.json` — preserving any non-Dash0 hooks already there. `uninstall-cursor.sh` uses the reverse strip: remove entries whose `command` contains `cursor-on-event.sh`, delete the file if it ends up with no hooks, else write the reduced JSON back.

Both scripts require `jq` for reliable JSON manipulation.

Two other Cursor-3.9 quirks worth remembering:
- The `~/.cursor/plugins/local/` sub-directory is required. A plugin dropped one level higher at `~/.cursor/plugins/<name>/` is silently ignored (that path is reserved for Cursor's own Marketplace-managed installs).
- No trust/enable dialog is required on first load — headless / `curl | bash` install stays fully non-interactive.

## Codex — build & run locally

Wire once against your local build, then rebuild-and-run.

```bash
# BIN = the exact path the bootstrap execs (build here and it runs your code, no download).
# BOOT = the working-copy bootstrap the config.toml hooks invoke.
BIN="$HOME/.local/state/dash0-agent-plugin/codex/bin/codex-on-event-$(grep '^VERSION=' scripts/codex-on-event.sh | cut -d'"' -f2)-$(uname -s | tr A-Z a-z)-$(uname -m | sed 's/x86_64/amd64/;s/aarch64/arm64/')"
BOOT="$PWD/scripts/codex-on-event.sh"

# 1. build the binary to that path
make build-binary PKG=./cmd/codex-on-event OUT="$BIN"

# 2. credentials, with debug so you can see spans without a backend
cat > ~/.codex/dash0-agent-plugin.local.md <<'EOF'
---
otlp_url: "https://ingress.<region>.aws.dash0.com"
auth_token: "<token>"
debug: true
debug_file: "/tmp/dash0-codex-debug.log"
---
EOF

# 3. register hooks + trust in config.toml, pointing at your working-copy bootstrap (run once)
"$BIN" emit-codex-hooks --command "$BOOT" --config ~/.codex/config.toml >> ~/.codex/config.toml
```

Then run and watch spans:

```bash
codex exec 'run: echo hi' </dev/null      # or interactive `codex` — start a NEW session
tail -F /tmp/dash0-codex-debug.log        # each span logged as [dash0:trace] {...}
```

Rebuild loop — just step 1, then a new session. No re-trust: the trust hash is over the hook *command* (the bootstrap path), so editing the bootstrap or the Go binary is picked up without touching `config.toml`. (`</dev/null` keeps `codex exec` from blocking on stdin.)
