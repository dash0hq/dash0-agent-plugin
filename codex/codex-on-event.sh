#!/usr/bin/env bash
# SPDX-FileCopyrightText: Copyright 2026 Dash0 Inc.
# SPDX-License-Identifier: Apache-2.0

# Bootstrap wrapper for the codex-on-event binary. Installed at a stable
# user-owned path by install-codex.sh and referenced by absolute path from the
# [[hooks.*]] entries in ~/.codex/config.toml, so each hook invocation runs:
#
#   stdin (JSON) → codex-on-event.sh → codex-on-event binary → OTLP
#
# Fail-open: any error before exec'ing the binary logs to stderr and exits 0 so
# a broken installer never breaks the user's Codex session. `set -e` is
# deliberately absent; fail_open does that job.
set -u

AGENT="codex"
VERSION="0.1.24"

# Where the downloaded binary lives. Resolution order:
#   1. DASH0_PLUGIN_DATA  — explicit override (dev / tests).
#   2. PLUGIN_DATA        — Codex sets this to the plugin's data dir when this
#                           script runs as an installed marketplace plugin, so
#                           the cache stays inside the plugin's own state.
#   3. ~/.local/state/…   — the installer (config.toml) path.
BASE="${DASH0_PLUGIN_DATA:-${PLUGIN_DATA:-${XDG_STATE_HOME:-$HOME/.local/state}/dash0-agent-plugin/codex}}"

# >>> shared bootstrap — byte-identical across cursor, codex and copilot >>>
# test/consistency asserts these three regions match, so a fix lands in all of
# them or in none. Everything agent-specific is declared above.

fail_open() {
  echo "${AGENT}-on-event: $*" >&2
  exit 0
}

BIN_DIR="$BASE/bin"
REPO="dash0hq/dash0-agent-plugin"

# Git Bash, MSYS2 and Cygwin report kernel strings like MINGW64_NT-10.0-26200,
# never "windows", so the release asset would be requested under a name that does
# not exist. EXE carries the suffix GoReleaser appends for Windows builds through
# to both the asset name and the cache filename; it stays empty elsewhere, so a
# POSIX cache path is unchanged and nothing re-downloads.
OS=$(uname -s | tr '[:upper:]' '[:lower:]')
EXE=""
case "$OS" in
  mingw*|msys*|cygwin*) OS="windows"; EXE=".exe" ;;
esac
ARCH=$(uname -m)
case "$ARCH" in
  x86_64|amd64)  ARCH="amd64" ;;
  aarch64|arm64) ARCH="arm64" ;;
esac

BINARY="$BIN_DIR/${AGENT}-on-event-${VERSION}-${OS}-${ARCH}${EXE}"

if [ ! -x "$BINARY" ]; then
  mkdir -p "$BIN_DIR" 2>/dev/null || fail_open "could not create $BIN_DIR"
  BASE_URL="https://github.com/${REPO}/releases/download/v${VERSION}"
  ASSET="${AGENT}-on-event-${OS}-${ARCH}${EXE}"
  URL="${BASE_URL}/${ASSET}"
  CHECKSUMS_URL="${BASE_URL}/checksums.txt"

  if command -v curl &>/dev/null; then
    curl -fsSL -o "$BINARY" "$URL" || fail_open "download failed: $URL"
    CHECKSUMS=$(curl -fsSL "$CHECKSUMS_URL") || fail_open "checksums fetch failed"
  elif command -v wget &>/dev/null; then
    wget -qO "$BINARY" "$URL" || fail_open "download failed: $URL"
    CHECKSUMS=$(wget -qO- "$CHECKSUMS_URL") || fail_open "checksums fetch failed"
  else
    fail_open "neither curl nor wget found"
  fi

  # Fail closed on integrity: a binary that cannot be verified is not run. Every
  # supported platform ships a hash tool — shasum on macOS, sha256sum on glibc
  # Linux and on busybox — so reaching either refusal below means the release is
  # malformed or the host is not one we support. fail_open still exits 0, so the
  # cost is this run's telemetry, never the user's session.
  EXPECTED=$(echo "$CHECKSUMS" | grep "  ${ASSET}$" | cut -d' ' -f1)
  if [ -z "$EXPECTED" ]; then
    rm -f "$BINARY"
    fail_open "no checksum for ${ASSET} — refusing to run an unverified binary"
  fi
  if command -v sha256sum &>/dev/null; then
    ACTUAL=$(sha256sum "$BINARY" | cut -d' ' -f1)
  elif command -v shasum &>/dev/null; then
    ACTUAL=$(shasum -a 256 "$BINARY" | cut -d' ' -f1)
  else
    ACTUAL=""
  fi
  if [ -z "$ACTUAL" ]; then
    rm -f "$BINARY"
    fail_open "no sha256 tool (sha256sum/shasum) to verify ${ASSET} — refusing to run an unverified binary"
  fi
  if [ "$ACTUAL" != "$EXPECTED" ]; then
    rm -f "$BINARY"
    fail_open "checksum mismatch (expected $EXPECTED, got $ACTUAL)"
  fi

  if [ "$OS" != "windows" ]; then
    chmod +x "$BINARY" || fail_open "could not mark $BINARY executable"
  fi
fi

# Forward stdin, plus the event-name argument for the agents that pass one. The
# binary exits 0 on telemetry errors, so no trap is needed around this.
exec "$BINARY" "$@"
# <<< shared bootstrap <<<
