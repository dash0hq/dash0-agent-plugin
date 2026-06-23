# Cursor source — developer reference

This directory holds the Cursor-side configuration and capture scaffolding
for the Cursor → Dash0 integration. It is the developer reference: how to
build, sideload local changes, cut releases, and collect fixture payloads.

End-user install / configure / uninstall docs live in
[`.cursor-plugin/README.md`](../.cursor-plugin/README.md).

## Contents

| Path | Purpose |
|---|---|
| `hooks.json` | Hook registration for the `curl \| bash` flow. Uses absolute `$HOME/...` paths because it gets copied to `~/.cursor/hooks.json`. |
| `plugin-hooks.json` | Hook registration for the Marketplace plugin. Uses relative `./scripts/...` paths because Cursor resolves them from the plugin root. |
| `skills/` | Cursor-only agent skills (e.g. `dash0-configure`). Referenced from `.cursor-plugin/plugin.json`. |
| `capture/` | Records real Cursor hook payloads as test fixtures. See `capture/README.md`. |

The code that consumes Cursor hooks lives elsewhere:

- `cmd/cursor-on-event/` — the binary the bootstrap script execs
- `internal/source/cursor/` — Cursor-specific event normalization
- `internal/pipeline/` — shared OTLP span emission (also used by Claude Code)
- `scripts/cursor-on-event.sh` — bootstrap wrapper that downloads + execs the binary
- `.cursor-plugin/plugin.json` — Marketplace plugin manifest (references `cursor/plugin-hooks.json` and `cursor/skills/`)
- `cursor/skills/dash0-configure/SKILL.md` — agent skill that walks the user through writing the config file

## Build

For your current platform:

```bash
go build ./cmd/cursor-on-event
```

Cross-compile the full release matrix (matches `.goreleaser.yaml`):

```bash
for OS in darwin linux; do
  for ARCH in amd64 arm64; do
    GOOS=$OS GOARCH=$ARCH CGO_ENABLED=0 go build \
      -ldflags="-s -w -X github.com/dash0hq/dash0-agent-plugin/internal/version.Version=dev" \
      -o dist/cursor-on-event-${OS}-${ARCH} \
      ./cmd/cursor-on-event
  done
done
```

Run unit tests (cursor adapter + everything else):

```bash
go test ./...
```

## Package

Releases are cut via `scripts/release.sh <version>`, which:

1. Bumps the hardcoded `VERSION` in `scripts/on-event.sh`, `scripts/cursor-on-event.sh`,
   `.claude-plugin/plugin.json`, and `.cursor-plugin/plugin.json`.
   (`install-cursor.sh` resolves the latest GitHub release at runtime, so it's
   not bumped here — set `DASH0_VERSION=` to pin a specific version.)
2. Commits the bumps as `release: v<version>`.
3. Creates the `v<version>` tag and pushes it.

The push triggers `.github/workflows/release.yml`, which runs GoReleaser
(`.goreleaser.yaml`) to build and publish:

| Artifact | Source |
|---|---|
| `on-event-{darwin,linux}-{amd64,arm64}` | `cmd/on-event` (Claude Code) |
| `cursor-on-event-{darwin,linux}-{amd64,arm64}` | `cmd/cursor-on-event` (this) |
| `checksums.txt` | sha256 of every artifact |

The bootstrap script (`scripts/cursor-on-event.sh`) and `install-cursor.sh`
both fetch the binary from GitHub Releases by version on first run and
verify against `checksums.txt`. They also pull `cursor-on-event.sh` itself
from the matching git tag on `raw.githubusercontent.com`, so the install
flow has zero dependencies beyond `curl`/`wget` + `sha256sum`/`shasum`.

## Install in a local Cursor instance

Replicates what `install-cursor.sh` does, but sideloads a locally-built
binary instead of downloading from a release. Use this to test changes
without tagging.

**1. Build the binary at the path the bootstrap script expects:**

```bash
OS=$(uname -s | tr '[:upper:]' '[:lower:]')
ARCH=$(uname -m | sed 's/x86_64/amd64/;s/aarch64/arm64/')
VERSION=0.1.9   # must match scripts/cursor-on-event.sh
BIN_DIR="$HOME/.local/state/dash0-agent-plugin/cursor/bin"
mkdir -p "$BIN_DIR"
go build -o "$BIN_DIR/cursor-on-event-${VERSION}-${OS}-${ARCH}" \
  ./cmd/cursor-on-event
```

**2. Install the bootstrap script** at the path `cursor/hooks.json` references:

```bash
SHARE_DIR="$HOME/.local/share/dash0-agent-plugin"
mkdir -p "$SHARE_DIR"
cp scripts/cursor-on-event.sh "$SHARE_DIR/cursor-on-event.sh"
chmod +x "$SHARE_DIR/cursor-on-event.sh"
```

**3. Install the production `hooks.json`** (back up any existing one first):

```bash
[ -e ~/.cursor/hooks.json ] && cp ~/.cursor/hooks.json ~/.cursor/hooks.json.bak
cp cursor/hooks.json ~/.cursor/hooks.json
```

**4. Write a config file** at `~/.cursor/dash0-agent-plugin.local.md`:

```yaml
---
otlp_url: "https://ingress.<region>.aws.dash0.com"
auth_token: "your-dash0-auth-token"
dataset: "default"
agent_name: "cursor"
omit_io: false
# For local debugging — every emitted span is also appended to this file:
# debug: true
# debug_file: /tmp/dash0-cursor-debug.log
---
```

```bash
chmod 600 ~/.cursor/dash0-agent-plugin.local.md
```

**5. Quit and relaunch Cursor** (Cmd+Q on macOS) — Cursor reads `hooks.json`
at startup. Subsequent rebuilds (step 1) take effect on the next hook fire
without needing another restart, since the bootstrap script `exec`'s a fresh
binary each time.

## Verify

With `debug: true` set in the config, every emitted span lands in the debug
file as one `[dash0:trace] {...}` line. In another terminal:

```bash
tail -F /tmp/dash0-cursor-debug.log
```

Run a prompt that uses at least one tool. You should see:

- one `execute_tool <Name>` span per tool call
- one `chat default` span at turn end carrying `gen_ai.usage.input_tokens`,
  `output_tokens`, and `cache_read.input_tokens`
- the same `traceId` on every span in the turn
- the tool span's `parentSpanId` matching the chat span's `spanId`

## Switch to capture mode

To collect new fixture payloads instead of emitting spans, swap in the
capture `hooks.json` — see `capture/README.md`.

## Uninstall

```bash
rm -rf ~/.local/state/dash0-agent-plugin \
       ~/.local/share/dash0-agent-plugin \
       ~/.cursor/dash0-agent-plugin.local.md \
       ~/.cursor/hooks.json
```
