#!/usr/bin/env bash
# SPDX-FileCopyrightText: Copyright 2026 Dash0 Inc.
# SPDX-License-Identifier: Apache-2.0

set -euo pipefail

# Every path below that ends in "we can't safely run the binary" exits 0, as
# cursor, codex and copilot already do. Claude renders any other non-zero exit
# as a hook error on *every* event, and none of these is something the user can
# act on mid-session. It does not mean anything unverified runs: a failed
# checksum still installs nothing.
fail_open() {
  echo "on-event: $*" >&2
  exit 0
}

PLUGIN_DATA="${CLAUDE_PLUGIN_DATA:-}"
[ -n "$PLUGIN_DATA" ] || fail_open "CLAUDE_PLUGIN_DATA is not set"
BIN_DIR="$PLUGIN_DATA/bin"
REPO="dash0hq/dash0-agent-plugin"
VERSION="0.1.25"

# Detect OS and architecture.
OS=$(uname -s | tr '[:upper:]' '[:lower:]')
ARCH=$(uname -m)
case "$ARCH" in
  x86_64)  ARCH="amd64" ;;
  aarch64) ARCH="arm64" ;;
  arm64)   ARCH="arm64" ;;
esac

BINARY="$BIN_DIR/on-event-${VERSION}-${OS}-${ARCH}"

# Download the binary on first run.
if [ ! -x "$BINARY" ]; then
  mkdir -p "$BIN_DIR" 2>/dev/null || fail_open "could not create $BIN_DIR"

  # Download to a private temp and rename into place. Hooks run concurrently —
  # parallel tool calls each fire their own, and every session on the machine
  # shares this directory — so on the first run after a version bump, N processes
  # see no binary at once. Writing $BINARY directly let them interleave into one
  # file and exec whatever was there so far. rename(2) is atomic within a
  # directory: a late arrival sees either no file or a complete one, and a process
  # already executing the old inode keeps running it.
  #
  # $$ suffices to make the name private, because every process that can collide
  # here is on this machine. The trap covers the failure paths below; a temp is
  # only orphaned if the run is killed outright, which for a hook means the host's
  # timeout. exec does not fire it, and by then there is nothing left to remove.
  TMP="$BINARY.tmp.$$"
  trap 'rm -f "$TMP"' EXIT

  BASE_URL="https://github.com/${REPO}/releases/download/v${VERSION}"
  CHECKSUMS_URL="${BASE_URL}/checksums.txt"

  if command -v curl &>/dev/null; then
    fetch() { curl -fsSL -o "$2" "$1"; }
    fetch_stdout() { curl -fsSL "$1"; }
  elif command -v wget &>/dev/null; then
    fetch() { wget -qO "$2" "$1"; }
    fetch_stdout() { wget -qO- "$1"; }
  else
    fail_open "neither curl nor wget found"
  fi

  # Try each asset name this binary has been published under, newest first. The
  # Claude marketplace lists the repo with no ref, so install and update take the
  # default branch and pair this script with the LAST PUBLISHED release. Accepting
  # both names means the published name can be changed in .goreleaser.yaml alone,
  # with no release-timing coordination: before the rename ships the first
  # candidate 404s and the second succeeds, and after it ships the first one hits.
  ASSET=""
  for CANDIDATE in "claude-on-event-${OS}-${ARCH}" "on-event-${OS}-${ARCH}"; do
    # stderr is dropped: a miss on the first candidate is expected until the
    # rename ships, and the message below covers the case where none is found.
    if fetch "${BASE_URL}/${CANDIDATE}" "$TMP" 2>/dev/null; then
      ASSET="$CANDIDATE"
      break
    fi
  done
  if [ -z "$ASSET" ]; then
    fail_open "no release asset for ${OS}-${ARCH} in v${VERSION}"
  fi
  CHECKSUMS=$(fetch_stdout "$CHECKSUMS_URL") \
    || fail_open "could not fetch checksums.txt for v${VERSION}"

  # Verify the checksum. A missing entry is fatal, not skipped: the first asset
  # name tried is one that current releases do not publish, so treating "not in
  # checksums.txt" as "nothing to check" would accept an unverified binary served
  # under that name.
  # awk, not grep: `set -o pipefail` is on, so a grep that matches nothing would
  # abort the script at this assignment and skip the diagnostic below. awk exits 0
  # whether or not it matched.
  EXPECTED=$(printf '%s\n' "$CHECKSUMS" | awk -v want="$ASSET" '$2 == want { print $1 }')
  if [ -z "$EXPECTED" ]; then
    fail_open "${ASSET} is not listed in checksums.txt for v${VERSION} — refusing to run an unverified binary"
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

# Forward stdin and arguments to the binary.
#
# A cached file can pass the -x test and still not run — a wrong-architecture
# asset is the realistic case. `set +e` as well as execfail: with `set -e` still
# on, a failed exec kills the shell before any guard can act. On success exec
# replaces this process, so the line below runs only on failure.
#
# It is deliberately left in place rather than deleted. Deleting it means the
# next hook re-downloads the whole asset, fails to exec it again, and repeats —
# a multi-MB fetch on every tool call. This stays quiet until the next release
# swaps the pinned version, which is when it could start working again.
shopt -s execfail
set +e
# shellcheck disable=SC2093  # execfail is the point: the line below runs only
# when exec could not start the binary. Without it bash would exit here.
exec "$BINARY" "$@"
fail_open "the cached binary could not be executed — telemetry is off until the next release"
