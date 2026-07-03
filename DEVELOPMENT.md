# Development

## Local development

```bash
# Test locally without marketplace
claude --plugin-dir /path/to/dash0-agent-plugin

# Build the binary locally (instead of downloading from GitHub Releases)
VERSION=$(grep '^VERSION=' scripts/on-event.sh | cut -d'"' -f2)
go build -o ~/.claude/plugins/data/dash0-agent-plugin-inline/bin/on-event-${VERSION}-$(uname -s | tr '[:upper:]' '[:lower:]')-$(uname -m | sed 's/x86_64/amd64/') ./cmd/on-event/
```

## Releasing

Releases are automated with [GoReleaser](https://goreleaser.com/) via GitHub Actions. To create a new release, update the version in:

- `.claude-plugin/plugin.json` — `version` field
- `.cursor-plugin/plugin.json` — `version` field
- `scripts/on-event.sh` — `VERSION=` line (Claude Code binary downloader)
- `scripts/cursor-on-event.sh` — `VERSION=` line (Cursor binary downloader)

Then tag the commit in main:

```bash
git tag v<version>
git push --tags
```

This triggers the release workflow which cross-compiles binaries for `darwin/linux × amd64/arm64` and publishes them to [GitHub Releases](https://github.com/dash0hq/dash0-agent-plugin/releases). The `on-event.sh` script downloads the matching binary on first run.

## Cursor native-plugin layout

The `install-cursor.sh` script lays the plugin down at `~/.cursor/plugins/local/dash0-agent-plugin/`, which Cursor scans on startup. The on-disk shape mirrors the repo:

```
~/.cursor/plugins/local/dash0-agent-plugin/
├── .cursor-plugin/plugin.json          (copy of the repo's manifest)
├── cursor/plugin-hooks.json            (relative-path hook registrations)
├── cursor/skills/dash0-configure/…     (shipped skills)
└── scripts/cursor-on-event.sh          (bootstrap wrapper)
```

Cursor invokes hooks with the plugin directory as CWD, so `scripts/cursor-on-event.sh`'s per-project config lookup (`.cursor/dash0-agent-plugin.local.md` relative to CWD) does not resolve under this layout — only the global `~/.cursor/dash0-agent-plugin.local.md` file is honored. Verified against Cursor 3.7.27: no trust/enable dialog on first load, hooks fire immediately after restart.

Historical note: the `~/.cursor/plugins/local/` sub-directory is required. A plugin dropped one level higher at `~/.cursor/plugins/<name>/` is silently ignored by Cursor 3.7.27 (that path is reserved for Cursor's own Marketplace-managed installs).
