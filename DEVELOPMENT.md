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
