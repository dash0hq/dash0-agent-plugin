#!/usr/bin/env bash
# SPDX-FileCopyrightText: Copyright 2026 Dash0 Inc.
# SPDX-License-Identifier: Apache-2.0

# Bootstrap wrapper for the copilot-on-event binary. Referenced from the
# plugin-contributed copilot/hooks.json, which passes the event name as an
# argument (camelCase Copilot payloads carry no event-name field):
#
#   stdin (JSON) → copilot-on-event.sh <eventName> → copilot-on-event binary → OTLP
#
# Per-turn token/cost/model telemetry additionally requires Copilot's native
# OTel written to a per-session file, set up by the `dash0-configure` skill as a
# shell function that shadows `copilot`. Without it, spans are still emitted,
# just without usage.
#
# Fail-open: any error before exec logs to stderr and exits 0 so a broken
# installer never breaks the user's Copilot session. Mandatory here, since
# Copilot's tool hooks are fail-closed. `set -e` is deliberately absent;
# fail_open does that job.
set -u

AGENT="copilot"
VERSION="0.1.25"

# Where the downloaded binary lives. Copilot sets COPILOT_PLUGIN_DATA for a
# marketplace install; the XDG path is the fallback for a manual one.
BASE="${COPILOT_PLUGIN_DATA:-${XDG_STATE_HOME:-$HOME/.local/state}/dash0-agent-plugin/copilot}"

# >>> shared bootstrap — byte-identical across cursor, codex and copilot >>>
# test/consistency asserts these three regions match, so a fix lands in all of
# them or in none. Everything agent-specific is declared above.

fail_open() {
  echo "${AGENT}-on-event: $*" >&2
  exit 0
}

BIN_DIR="$BASE/bin"
REPO="dash0hq/dash0-agent-plugin"

OS=$(uname -s | tr '[:upper:]' '[:lower:]')
ARCH=$(uname -m)
case "$ARCH" in
  x86_64)  ARCH="amd64" ;;
  aarch64) ARCH="arm64" ;;
  arm64)   ARCH="arm64" ;;
esac

BINARY="$BIN_DIR/${AGENT}-on-event-${VERSION}-${OS}-${ARCH}"

if [ ! -x "$BINARY" ]; then
  mkdir -p "$BIN_DIR" 2>/dev/null || fail_open "could not create $BIN_DIR"

  # Download to a private temp and rename into place. Hooks run concurrently and
  # every session on the machine shares this directory, so several processes can
  # find no binary at once; writing $BINARY directly let them interleave into one
  # file. rename(2) is atomic within a directory, so the others see either no file
  # or a complete one. The trap covers the fail_open paths below.
  TMP="$BINARY.tmp.$$"
  trap 'rm -f "$TMP"' EXIT

  BASE_URL="https://github.com/${REPO}/releases/download/v${VERSION}"
  ASSET="${AGENT}-on-event-${OS}-${ARCH}"
  URL="${BASE_URL}/${ASSET}"
  CHECKSUMS_URL="${BASE_URL}/checksums.txt"

  if command -v curl &>/dev/null; then
    curl -fsSL -o "$TMP" "$URL" || fail_open "download failed: $URL"
    CHECKSUMS=$(curl -fsSL "$CHECKSUMS_URL") || fail_open "checksums fetch failed"
  elif command -v wget &>/dev/null; then
    wget -qO "$TMP" "$URL" || fail_open "download failed: $URL"
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
    fail_open "no checksum for ${ASSET} — refusing to run an unverified binary"
  fi
  if command -v sha256sum &>/dev/null; then
    ACTUAL=$(sha256sum "$TMP" | cut -d' ' -f1)
  elif command -v shasum &>/dev/null; then
    ACTUAL=$(shasum -a 256 "$TMP" | cut -d' ' -f1)
  else
    ACTUAL=""
  fi
  if [ -z "$ACTUAL" ]; then
    fail_open "no sha256 tool (sha256sum/shasum) to verify ${ASSET} — refusing to run an unverified binary"
  fi
  if [ "$ACTUAL" != "$EXPECTED" ]; then
    fail_open "checksum mismatch (expected $EXPECTED, got $ACTUAL)"
  fi

  # Executable before it is visible, so nothing can find $BINARY and fail the
  # -x test that guards this block.
  chmod +x "$TMP" || fail_open "could not mark $TMP executable"
  mv -f "$TMP" "$BINARY" || fail_open "could not move $TMP into place"
fi

# Forward stdin, plus the event-name argument for the agents that pass one. The
# binary exits 0 on telemetry errors, so no trap is needed around this.
exec "$BINARY" "$@"
# <<< shared bootstrap <<<
