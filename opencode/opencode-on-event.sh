#!/usr/bin/env bash
# SPDX-FileCopyrightText: Copyright 2026 Dash0 Inc.
# SPDX-License-Identifier: Apache-2.0

# Bootstrap wrapper for the opencode-on-event binary. Spawned once per canonical
# event by the OpenCode TypeScript plugin, which writes the event JSON to stdin:
#
#   plugin → opencode-on-event.sh → opencode-on-event binary → OTLP
#
# Responsibilities:
#   - Detect OS/arch and download the matching opencode-on-event binary from
#     GitHub Releases on first run, verifying the checksum.
#   - exec the binary, forwarding stdin. The binary reads the configuration file,
#     the keychain and the environment itself.
#
# Fail-open: any error before exec'ing the binary logs to stderr and exits 0
# so a broken installer never breaks the user's OpenCode session.

set -u

fail_open() {
  echo "opencode-on-event: $*" >&2
  exit 0
}

# Where the downloaded binary lives. Mirrors harness.OpenCode.DataDir()'s
# precedence so users can clean up the whole tree at once.
BASE="${OPENCODE_PLUGIN_DATA:-${DASH0_PLUGIN_DATA:-${XDG_STATE_HOME:-$HOME/.local/state}/dash0-agent-plugin/opencode}}"
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

if [ -n "${DASH0_VERSION:-}" ]; then
  # Validated, because VERSION reaches both a download URL and a filesystem path:
  # curl squashes `..`, so an unvalidated value retargets BASE_URL, and with it
  # checksums.txt, at another repository.
  if [[ "$DASH0_VERSION" =~ ^[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.]+)?$ ]]; then
    VERSION="$DASH0_VERSION"
  else
    echo "opencode-on-event: ignoring DASH0_VERSION='$DASH0_VERSION' — not a version (expected 0.2.0, or 0.2.0-dev.1)" >&2
  fi
fi

case "$OS" in
  darwin|linux) ;;
  *) fail_open "unsupported OS: $OS (OpenCode telemetry runs on macOS and Linux)" ;;
esac

BINARY="$BIN_DIR/opencode-on-event-${VERSION}-${OS}-${ARCH}"
# The digest that was verified at download time, kept next to the binary so every
# later run can re-verify locally without another network round trip.
DIGEST_FILE="$BINARY.sha256"

# Prints the SHA-256 of "$1", or nothing when neither hashing tool is installed.
sha256_of() {
  if command -v sha256sum &>/dev/null; then
    sha256sum "$1" | cut -d' ' -f1
  elif command -v shasum &>/dev/null; then
    shasum -a 256 "$1" | cut -d' ' -f1
  fi
}

if [ -x "$BINARY" ]; then
  # A cached binary is re-verified on every run: the download-time check says
  # nothing about bytes that were tampered with afterwards.
  RECORDED=$(cat "$DIGEST_FILE" 2>/dev/null || true)
  CACHED=$(sha256_of "$BINARY")
  if [ -n "$RECORDED" ] && [ -n "$CACHED" ] && [ "$CACHED" != "$RECORDED" ]; then
    rm -f "$BINARY" "$DIGEST_FILE"
    fail_open "cached binary failed checksum verification (expected $RECORDED, got $CACHED); it has been removed and will be re-downloaded"
  fi
fi

if [ ! -x "$BINARY" ]; then
  mkdir -p "$BIN_DIR" 2>/dev/null || fail_open "could not create $BIN_DIR"

  # Sessions share this directory and fire wrappers concurrently, so on the first
  # run after a version bump several processes see no binary at once. Each stages
  # its own copy and renames it into place: rename(2) is atomic, so a late arrival
  # sees either no file or a complete verified one, and nobody deletes another's.
  TMP="$BINARY.tmp.$$"
  trap 'rm -f "$TMP"' EXIT

  BASE_URL="https://github.com/${REPO}/releases/download/v${VERSION}"
  ASSET="opencode-on-event-${OS}-${ARCH}"
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

  # A missing entry is fatal rather than skipped: OpenCode has no pre-checksum
  # releases, so "not in checksums.txt" can only mean the downloaded bytes are
  # not the published asset.
  # awk, not grep: awk exits 0 whether or not it matched, so the diagnostic below
  # is reached instead of a pipeline failure.
  EXPECTED=$(printf '%s\n' "$CHECKSUMS" | awk -v want="$ASSET" '$2 == want { print $1 }')
  if [ -z "$EXPECTED" ]; then
    fail_open "${ASSET} is not listed in checksums.txt for v${VERSION}"
  fi
  ACTUAL=$(sha256_of "$TMP")
  if [ -z "$ACTUAL" ]; then
    fail_open "no sha256 tool (sha256sum/shasum) to verify ${ASSET} — refusing to run an unverified binary"
  fi
  if [ "$ACTUAL" != "$EXPECTED" ]; then
    fail_open "checksum mismatch (expected $EXPECTED, got $ACTUAL)"
  fi
  printf '%s\n' "$ACTUAL" >"$DIGEST_FILE" || fail_open "could not record digest at $DIGEST_FILE"

  chmod +x "$TMP" || fail_open "could not mark $TMP executable"
  mv -f "$TMP" "$BINARY" || fail_open "could not move $TMP into place"
fi

# A cached file can pass the -x test and still not run — a wrong-architecture
# asset is the realistic case. execfail and `set +e` make a failed exec fall
# through to fail_open instead of ending the shell with 126 or 127. On success
# exec replaces this process, so the line after it runs only on failure.
shopt -s execfail
set +e
# shellcheck disable=SC2093 # execfail is the point: the next line runs only when exec could not start the binary
exec "$BINARY"
fail_open "the cached binary could not be executed — telemetry is off until the next release"
