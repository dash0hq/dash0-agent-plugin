# Cursor source — developer reference

This directory holds everything shipped to Cursor — the bootstrap wrapper
(`cursor-on-event.sh`), the hook registration template (`hooks.json`),
and skills. It is the developer reference: how to build, sideload local changes,
and cut releases.

End-user install / configure / uninstall docs live in
[.cursor-plugin/README.md](../.cursor-plugin/README.md).

## Contents

| Path | Purpose |
|---|---|
| `hooks.json` | Source of truth for which Cursor events the plugin listens to, and the only one of the four `<runtime>/hooks.json` files that is **not** referenced from a plugin manifest — Cursor ignores manifest hooks for local plugins, so this is an installer template rather than something Cursor reads. (Codex also merges hooks into a global config at install time, but its `codex/hooks.json` *is* manifest-referenced for the marketplace path; `install-codex.sh` bypasses it and renders the block from `codex.HookEvents` instead, because each hook needs a `trusted_hash` computed from install-time values.) `install-cursor.sh` reads this file, translates `./cursor/cursor-on-event.sh` to `$HOME/.cursor/plugins/local/dash0-agent-plugin/cursor/cursor-on-event.sh`, and merges the entries into the user's `~/.cursor/hooks.json` (Cursor doesn't fire hooks from local plugins directly). |
| `cursor-on-event.sh` | Bootstrap wrapper Cursor invokes on each event: loads the config file, downloads + checksum-verifies the `cursor-on-event` binary on first run, then execs it. |
| `skills/` | Cursor-only agent skills (e.g. `dash0-configure`). Referenced from `.cursor-plugin/plugin.json`. |

The code that consumes Cursor hooks lives elsewhere:

- `cmd/cursor-on-event/` — the binary the bootstrap script execs
- `internal/source/cursor/` — Cursor-specific event normalization
- `internal/harness/` — resolves the config from Cursor's environment (shared by all four runtimes)
- `internal/pipeline/` — shared OTLP span emission (used by all four runtimes)
- `.cursor-plugin/plugin.json` — native plugin manifest Cursor reads from `~/.cursor/plugins/local/dash0-agent-plugin/.cursor-plugin/plugin.json` (declares `skills`; hooks are wired via `~/.cursor/hooks.json` at install time, not via the manifest)
- `cursor/skills/dash0-configure/SKILL.md` — agent skill that walks the user through writing the config file

## Install layout (hybrid)

The `install-cursor.sh` script lays the plugin down at `~/.cursor/plugins/local/dash0-agent-plugin/`, which Cursor scans on startup:

```
~/.cursor/plugins/local/dash0-agent-plugin/
├── .cursor-plugin/plugin.json          (manifest — declares skills, no hooks)
├── cursor/hooks.json                   (installer template — see below)
├── cursor/skills/dash0-configure/…     (shipped skills)
└── cursor/cursor-on-event.sh           (bootstrap wrapper Cursor invokes)
```

**Hooks are registered in `~/.cursor/hooks.json`, not in the plugin manifest.** Cursor 3.9.x loads the local plugin (making the name + skills surface in the UI with a "local plugin" label) but silently ignores any `hooks` field in the manifest — verified with a probe plugin whose only hook was a `printf … >> /tmp/probe.log` script; no invocation was ever recorded despite `[pluginsSubsystem] loadUserLocalPlugin` log lines confirming the manifest loaded. Hooks fire only from `~/.cursor/hooks.json` (user scope) and `<project>/.cursor/hooks.json` (project scope).

`install-cursor.sh` therefore reads `cursor/hooks.json` (source of truth for which events the plugin listens to), translates each `./cursor/cursor-on-event.sh` command to `$HOME/.cursor/plugins/local/dash0-agent-plugin/cursor/cursor-on-event.sh` (Cursor expands `$HOME` at invocation time), and merges the entries into `~/.cursor/hooks.json` — preserving any non-Dash0 hooks already there. `uninstall-cursor.sh` uses the reverse strip: remove entries whose `command` contains `cursor-on-event.sh`, delete the file if it ends up with no hooks, else write the reduced JSON back.

Both scripts require `jq` for reliable JSON manipulation.

Two other Cursor-3.9 quirks worth remembering:
- The `~/.cursor/plugins/local/` sub-directory is required. A plugin dropped one level higher at `~/.cursor/plugins/<name>/` is silently ignored (that path is reserved for Cursor's own Marketplace-managed installs).
- No trust/enable dialog is required on first load — headless / `curl | bash` install stays fully non-interactive.

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

1. Bumps the hardcoded `VERSION` in `claude/claude-on-event.sh`, `cursor/cursor-on-event.sh`,
   `.claude-plugin/plugin.json`, and `.cursor-plugin/plugin.json`.
   (`install-cursor.sh` resolves the latest GitHub release at runtime, so it's
   not bumped here — set `DASH0_VERSION=` to pin a specific version.)
2. Commits the bumps as `release: v<version>`.
3. Creates the `v<version>` tag and pushes it.

The push triggers `.github/workflows/release.yml`, which runs GoReleaser
(`.goreleaser.yaml`) to build and publish:

| Artifact | Source |
|---|---|
| `on-event-{darwin,linux}-{amd64,arm64}` | `cmd/claude-on-event` (Claude Code) |
| `cursor-on-event-{darwin,linux}-{amd64,arm64}` | `cmd/cursor-on-event` (this) |
| `checksums.txt` | sha256 of every artifact |

The bootstrap script (`cursor/cursor-on-event.sh`) and `install-cursor.sh`
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
VERSION=$(grep '^VERSION=' cursor/cursor-on-event.sh | cut -d'"' -f2)
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
jq --arg cmd '$HOME/.cursor/plugins/local/dash0-agent-plugin/cursor/cursor-on-event.sh' \
   '{version: (.version // 1), hooks: (.hooks | map_values(map(.command = $cmd)))}' \
   cursor/hooks.json > ~/.cursor/hooks.json
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
(`cursor/hooks.json`) require re-running step 3 and restarting.

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
capture `hooks.json` — see [`test/capture/cursor/README.md`](../test/capture/cursor/README.md).

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
