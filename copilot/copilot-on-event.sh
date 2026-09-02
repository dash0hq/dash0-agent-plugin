#!/usr/bin/env bash
# SPDX-FileCopyrightText: Copyright 2026 Dash0 Inc.
# SPDX-License-Identifier: Apache-2.0

# Bootstrap wrapper for the copilot-on-event binary. Referenced from the
# plugin-contributed copilot/hooks.json, which passes the event name as an
# argument (camelCase Copilot payloads carry no event-name field):
#
#   stdin (JSON) → copilot-on-event.sh <eventName> → copilot-on-event binary → OTLP
#
# Per-turn token/model telemetry additionally requires Copilot's native
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
VERSION="0.1.27"

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

# Says something is wrong and carries on. fail_open() ends the hook; this does
# not, for the cases where the sensible response is to use the default.
warn() {
  echo "on-event: $*" >&2
}

BIN_DIR="$BASE/bin"
REPO="dash0hq/dash0-agent-plugin"

# Point this install at a different published release — a -dev prerelease cut
# from a branch for QA, or a rollback — without editing this file. Same repo and
# the same checksum verification; only which release is asked for changes. Kept
# off the VERSION= line above so scripts/version.sh keeps reading the pinned
# default.
if [ -n "${DASH0_VERSION:-}" ]; then
  # Validated, because VERSION reaches both a download URL and a filesystem path.
  # curl squashes `..` in a path, so `v../../../owner/repo/releases/download/v9`
  # retargets BASE_URL at another repository — and checksums.txt comes from the
  # same base, so verification would pass against the attacker's own manifest
  # before the binary is exec'd. $BINARY traverses the same way and would write
  # outside BIN_DIR. The hook runs inside an agent session, so an injected
  # instruction writing this into a project .envrc is enough to reach it.
  if [[ "$DASH0_VERSION" =~ ^[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.]+)?$ ]]; then
    VERSION="$DASH0_VERSION"
  else
    # Warn and carry on with the pinned version, rather than end the hook.
    # DASH0_VERSION is a convenience for a rollback or a QA build, and the
    # realistic bad value is a typo — `v0.1.26` for `0.1.26`. Exiting here
    # turned that into a silent session with no telemetry at all, which is a
    # much worse outcome than ignoring the override, and the message already
    # said "ignoring".
    warn "ignoring DASH0_VERSION='$DASH0_VERSION' — not a version (expected 0.2.0, or 0.2.0-dev.1)"
  fi
fi

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

  # Download to a private temp and rename into place. Hooks run concurrently and
  # every session on the machine shares this directory, so several processes can
  # find no binary at once; writing $BINARY directly let them interleave into one
  # file. rename(2) is atomic within a directory, so the others see either no file
  # or a complete one. The trap covers the fail_open paths below.
  TMP="$BINARY.tmp.$$"
  trap 'rm -f "$TMP"' EXIT

  BASE_URL="https://github.com/${REPO}/releases/download/v${VERSION}"
  ASSET="${AGENT}-on-event-${OS}-${ARCH}${EXE}"
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
  # -x test that guards this block. Skipped on Windows, which has no executable
  # bit: a no-op that can still fail would fail_open for no reason.
  if [ "$OS" != "windows" ]; then
    chmod +x "$TMP" || fail_open "could not mark $TMP executable"
  fi
  # Windows refuses to rename over a .exe that another process is executing, and
  # a sibling hook that won this race has already started it. Its file is the same
  # verified build this one just downloaded, so an existing $BINARY is success
  # rather than an event dropped with the binary sitting right there. The
  # PowerShell twin of this bootstrap makes the same allowance.
  if ! mv -f "$TMP" "$BINARY" 2>/dev/null; then
    [ -x "$BINARY" ] || fail_open "could not move $TMP into place"
  fi
fi

# Forward stdin, plus the event-name argument for the agents that pass one. The
# binary exits 0 on telemetry errors, so no trap is needed around this.
exec "$BINARY" "$@"
# <<< shared bootstrap <<<
