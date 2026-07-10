# Cursor source — developer reference

This directory holds the Cursor-side configuration and capture scaffolding
for the Cursor → Dash0 integration. It is the developer reference: how to
build, sideload local changes, cut releases, and collect fixture payloads.

End-user install / configure / uninstall docs live in
[`.cursor-plugin/README.md`](../.cursor-plugin/README.md).

## Contents

| Path | Purpose |
|---|---|
| `plugin-hooks.json` | Source of truth for which Cursor events the plugin listens to. `install-cursor.sh` reads this file, translates `./scripts/cursor-on-event.sh` to `$HOME/.cursor/plugins/local/dash0-agent-plugin/scripts/cursor-on-event.sh`, and merges the entries into the user's `~/.cursor/hooks.json` (Cursor doesn't fire hooks from local plugins directly). |
| `skills/` | Cursor-only agent skills (e.g. `dash0-configure`). Referenced from `.cursor-plugin/plugin.json`. |
| `capture/` | Records real Cursor hook payloads as test fixtures. See `capture/README.md`. |

The code that consumes Cursor hooks lives elsewhere:

- `cmd/cursor-on-event/` — the binary the bootstrap script execs
- `internal/source/cursor/` — Cursor-specific event normalization
- `internal/pipeline/` — shared OTLP span emission (also used by Claude Code)
- `scripts/cursor-on-event.sh` — bootstrap wrapper that downloads + execs the binary
- `.cursor-plugin/plugin.json` — native plugin manifest Cursor reads from `~/.cursor/plugins/local/dash0-agent-plugin/.cursor-plugin/plugin.json` (declares `skills`; hooks are wired via `~/.cursor/hooks.json` at install time, not via the manifest)
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
VERSION=$(grep '^VERSION=' scripts/cursor-on-event.sh | cut -d'"' -f2)
BIN_DIR="$HOME/.local/state/dash0-agent-plugin/cursor/bin"
mkdir -p "$BIN_DIR"
go build -o "$BIN_DIR/cursor-on-event-${VERSION}-${OS}-${ARCH}" \
  ./cmd/cursor-on-event
```

**2. Symlink the repo into Cursor's local-plugins scan directory (surfaces the plugin manifest + skills in Cursor's UI):**

```bash
mkdir -p ~/.cursor/plugins/local
ln -sfn "$PWD" ~/.cursor/plugins/local/dash0-agent-plugin
```

**3. Merge the plugin's hooks into `~/.cursor/hooks.json`.** Cursor 3.9.x does
not fire hooks from local-plugin manifests, so hooks must live in the global
`~/.cursor/hooks.json` file. Same shape as the install-cursor.sh merge, done
by hand for sideload:

```bash
jq --arg cmd '$HOME/.cursor/plugins/local/dash0-agent-plugin/scripts/cursor-on-event.sh' \
   '{version: (.version // 1), hooks: (.hooks | map_values(map(.command = $cmd)))}' \
   cursor/plugin-hooks.json > ~/.cursor/hooks.json
```

Replace the `>` with the merge invocation from `install-cursor.sh` if you
already have hooks in `~/.cursor/hooks.json` you want to keep.

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

**5. Quit and relaunch Cursor** (Cmd+Q on macOS) — Cursor reads
`~/.cursor/hooks.json` at startup. Subsequent rebuilds (step 1) take effect
on the next hook fire without another restart, since the bootstrap script
`exec`'s a fresh binary each time. Changes to the hook event list
(`cursor/plugin-hooks.json`) require re-running step 3 and restarting.

To tear down the sideload:

```bash
rm ~/.cursor/plugins/local/dash0-agent-plugin
rm ~/.cursor/hooks.json                                # or edit to drop Dash0 entries
rm ~/.cursor/dash0-agent-plugin.local.md
rm -rf ~/.local/state/dash0-agent-plugin/cursor
```

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

Use the top-level uninstaller — it handles both the current native-plugin
layout and any pre-0.1.17 shell-installer leftovers:

```bash
./uninstall-cursor.sh --yes
```

Or from a source checkout:

```bash
bash uninstall-cursor.sh --yes
```
