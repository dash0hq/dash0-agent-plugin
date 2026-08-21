#!/usr/bin/env bash
# SPDX-FileCopyrightText: Copyright 2026 Dash0 Inc.
# SPDX-License-Identifier: Apache-2.0

# Bootstrap wrapper for the cursor-on-event binary. Installed at a stable
# user-owned path by the setup CLI; referenced by absolute path from Cursor's
# hooks.json so each hook invocation runs:
#
#   stdin (JSON) → cursor-on-event.sh → cursor-on-event binary → OTLP
#
# Responsibilities:
#   - Detect OS/arch and download the matching cursor-on-event binary from
#     GitHub Releases on first run, verifying the checksum.
#   - exec the binary, forwarding stdin.
#
# Fail-open: any error before exec'ing the binary logs to stderr and exits 0
# so a broken installer never breaks the user's Cursor session.

# Note: we deliberately do NOT use `set -e`; the trap below converts any
# failure into a stderr log and a clean exit so Cursor's agent loop is never
# blocked by telemetry plumbing.
set -u

fail_open() {
  echo "cursor-on-event: $*" >&2
  exit 0
}

# Where the downloaded binary lives. Mirrors the per-source scratch root
# layout from cmd/cursor-on-event/main.go so users can clean up the whole
# tree at once.
BASE="${DASH0_PLUGIN_DATA:-${XDG_STATE_HOME:-$HOME/.local/state}/dash0-agent-plugin/cursor}"
BIN_DIR="$BASE/bin"
REPO="dash0hq/dash0-agent-plugin"
VERSION="0.1.24"

OS=$(uname -s | tr '[:upper:]' '[:lower:]')
ARCH=$(uname -m)
case "$ARCH" in
  x86_64)  ARCH="amd64" ;;
  aarch64) ARCH="arm64" ;;
  arm64)   ARCH="arm64" ;;
esac

BINARY="$BIN_DIR/cursor-on-event-${VERSION}-${OS}-${ARCH}"

if [ ! -x "$BINARY" ]; then
  mkdir -p "$BIN_DIR" 2>/dev/null || fail_open "could not create $BIN_DIR"
  BASE_URL="https://github.com/${REPO}/releases/download/v${VERSION}"
  ASSET="cursor-on-event-${OS}-${ARCH}"
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

  EXPECTED=$(echo "$CHECKSUMS" | grep "  ${ASSET}$" | cut -d' ' -f1)
  if [ -n "$EXPECTED" ]; then
    if command -v sha256sum &>/dev/null; then
      ACTUAL=$(sha256sum "$BINARY" | cut -d' ' -f1)
    elif command -v shasum &>/dev/null; then
      ACTUAL=$(shasum -a 256 "$BINARY" | cut -d' ' -f1)
    else
      ACTUAL=""
    fi
    if [ -n "$ACTUAL" ] && [ "$ACTUAL" != "$EXPECTED" ]; then
      rm -f "$BINARY"
      fail_open "checksum mismatch (expected $EXPECTED, got $ACTUAL)"
    fi
  fi

  chmod +x "$BINARY" || fail_open "could not mark $BINARY executable"
fi

# Forward stdin to the binary. The binary itself exits 0 on telemetry errors
# (see cmd/cursor-on-event/main.go) so we don't need to wrap this in a trap.
exec "$BINARY"
