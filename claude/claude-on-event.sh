#!/usr/bin/env bash
# SPDX-FileCopyrightText: Copyright 2026 Dash0 Inc.
# SPDX-License-Identifier: Apache-2.0

set -euo pipefail

# Exit 0 on every "we can't safely run the binary" path, as the other three
# bootstraps do: Claude renders any other non-zero exit as an error on *every*
# event. But say so once an hour, or silence would hide a proxy blocking github
# or a release that never published. Keyed by version — hooks race, and a marker
# written by a straggler after a faster sibling cleared it is never cleared
# again. Roughly hourly, not exactly: no lock, so simultaneous hooks can each
# print, and a duplicated one-liner is not worth one in a hot path.
REMIND=3600

fail_open() {
  echo "on-event: $*" >&2

  # Every step below is optional: this function's contract is `exit 0`, and the
  # reminder is a nicety layered on top. It must not be able to fail on its own
  # account — an earlier version raised an arithmetic error on a corrupt marker,
  # which RETURNED from here instead of exiting and let the script fall through
  # to exec a binary it had never downloaded (exit 127).
  #
  # VERSION as well as BIN_DIR: the earliest caller is the CLAUDE_PLUGIN_DATA
  # check, which runs before either is assigned. BIN_DIR alone is not a
  # "have we got that far" test, because an inherited one from the environment
  # satisfies it — and then `set -u` kills us on VERSION, after mkdir has
  # created a directory we do not own.
  if [ -n "${BIN_DIR:-}" ] && [ -n "${VERSION:-}" ] && mkdir -p "$BIN_DIR" 2>/dev/null; then
    local marker="$BIN_DIR/.download-failing" now ver notified
    now=$(date +%s)
    read -r ver notified <<<"$(cat "$marker" 2>/dev/null || true)"
    # Anything but digits is treated as "never notified". A marker can be a
    # partial write from a hook the host killed mid-timeout, or hand-edited.
    case "${notified:-}" in ''|*[!0-9]*) notified=0 ;; esac

    if [ "${ver:-}" != "$VERSION" ] || [ $((now - notified)) -gt "$REMIND" ]; then
      # Only speak if the reminder was recorded. Otherwise the hourly cap does
      # not exist — a BIN_DIR that survives from an earlier install but is no
      # longer writable would repeat this on every hook of every turn.
      if echo "$VERSION $now" >"$marker" 2>/dev/null; then
        # \\n, not \n: printf would emit a real newline, invalid inside JSON.
        printf '{"systemMessage":"\\ndash0: cannot download the v%s binary, so no telemetry is being sent. Run claude with --debug for the reason."}\n' "$VERSION"
      fi
    fi
  fi
  exit 0
}

# Load settings from a config file. Returns 1 if file doesn't exist.
load_settings() {
  local file="$1"
  [[ -f "$file" ]] || return 1

  local frontmatter
  frontmatter=$(sed -n '/^---$/,/^---$/{ /^---$/d; p; }' "$file")

  # Check enabled flag (default: true if file exists but field is absent).
  local enabled
  enabled=$(echo "$frontmatter" | grep '^enabled:' | sed 's/enabled: *//' || true)
  if [[ "$enabled" == "false" ]]; then
    exit 0
  fi

  local val
  val=$(echo "$frontmatter" | grep '^otlp_url:' | sed 's/otlp_url: *//' | sed 's/^"\(.*\)"$/\1/' || true)
  [[ -n "$val" ]] && export DASH0_OTLP_URL="$val"
  val=$(echo "$frontmatter" | grep '^auth_token:' | sed 's/auth_token: *//' | sed 's/^"\(.*\)"$/\1/' || true)
  [[ -n "$val" ]] && export CLAUDE_PLUGIN_OPTION_AUTH_TOKEN="$val"
  val=$(echo "$frontmatter" | grep '^auth_token_keychain_service:' | sed 's/auth_token_keychain_service: *//' | sed 's/^"\(.*\)"$/\1/' || true)
  [[ -n "$val" ]] && export DASH0_AUTH_TOKEN_KEYCHAIN_SERVICE="$val"
  val=$(echo "$frontmatter" | grep '^auth_token_keychain_account:' | sed 's/auth_token_keychain_account: *//' | sed 's/^"\(.*\)"$/\1/' || true)
  [[ -n "$val" ]] && export DASH0_AUTH_TOKEN_KEYCHAIN_ACCOUNT="$val"
  val=$(echo "$frontmatter" | grep '^dataset:' | sed 's/dataset: *//' | sed 's/^"\(.*\)"$/\1/' || true)
  [[ -n "$val" ]] && export DASH0_DATASET="$val"
  val=$(echo "$frontmatter" | grep '^agent_name:' | sed 's/agent_name: *//' | sed 's/^"\(.*\)"$/\1/' || true)
  [[ -n "$val" ]] && export DASH0_AGENT_NAME="$val"
  val=$(echo "$frontmatter" | grep '^team_name:' | sed 's/team_name: *//' | sed 's/^"\(.*\)"$/\1/' || true)
  [[ -n "$val" ]] && export DASH0_TEAM_NAME="$val"
  val=$(echo "$frontmatter" | grep '^omit_io:' | sed 's/omit_io: *//' | sed 's/^"\(.*\)"$/\1/' || true)
  [[ -n "$val" ]] && export DASH0_OMIT_IO="$val"
  val=$(echo "$frontmatter" | grep '^omit_user_info:' | sed 's/omit_user_info: *//' | sed 's/^"\(.*\)"$/\1/' || true)
  [[ -n "$val" ]] && export DASH0_OMIT_USER_INFO="$val"
  val=$(echo "$frontmatter" | grep '^omit_identity_fallback:' | sed 's/omit_identity_fallback: *//' | sed 's/^"\(.*\)"$/\1/' || true)
  [[ -n "$val" ]] && export DASH0_OMIT_IDENTITY_FALLBACK="$val"
  val=$(echo "$frontmatter" | grep '^debug:' | sed 's/debug: *//' | sed 's/^"\(.*\)"$/\1/' || true)
  [[ -n "$val" ]] && export DASH0_DEBUG="$val"
  val=$(echo "$frontmatter" | grep '^debug_file:' | sed 's/debug_file: *//' | sed 's/^"\(.*\)"$/\1/' || true)
  [[ -n "$val" ]] && export DASH0_DEBUG_FILE="$val"

  return 0
}

# Load settings: project-level takes precedence, then global.
PROJECT_SETTINGS=".claude/dash0-agent-plugin.local.md"
GLOBAL_SETTINGS="$HOME/.claude/dash0-agent-plugin.local.md"

load_settings "$PROJECT_SETTINGS" || load_settings "$GLOBAL_SETTINGS" || true

# Resolve the auth token from the macOS keychain when a keychain reference is
# configured. This lets managed/MDM rollouts ship only a pointer (safe to place
# in managed-settings.json) while the secret is provisioned separately via
# `security add-generic-password`. A successful lookup takes precedence over an
# inline AUTH_TOKEN. Silently no-ops on non-macOS or when `security` is absent.
KEYCHAIN_SERVICE="${CLAUDE_PLUGIN_OPTION_AUTH_TOKEN_KEYCHAIN_SERVICE:-${DASH0_AUTH_TOKEN_KEYCHAIN_SERVICE:-}}"
KEYCHAIN_ACCOUNT="${CLAUDE_PLUGIN_OPTION_AUTH_TOKEN_KEYCHAIN_ACCOUNT:-${DASH0_AUTH_TOKEN_KEYCHAIN_ACCOUNT:-}}"
if [[ -n "$KEYCHAIN_SERVICE" ]] && command -v security &>/dev/null; then
  if [[ -n "$KEYCHAIN_ACCOUNT" ]]; then
    keychain_token=$(security find-generic-password -s "$KEYCHAIN_SERVICE" -a "$KEYCHAIN_ACCOUNT" -w 2>/dev/null || true)
  else
    keychain_token=$(security find-generic-password -s "$KEYCHAIN_SERVICE" -w 2>/dev/null || true)
  fi
  [[ -n "$keychain_token" ]] && export CLAUDE_PLUGIN_OPTION_AUTH_TOKEN="$keychain_token"
fi

# `:?` would exit 1 before fail_open is even reachable.
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
  # Fail CLOSED on integrity, as copilot-on-event.sh already does. Installing
  # unverified was the main route to a cached file that passes -x and cannot
  # run: on a host with neither tool, whatever a proxy served in place of the
  # asset was written straight into the cache, and never re-downloaded after.
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
  # Downloads work again; forget the earlier failures.
  rm -f "$BIN_DIR/.download-failing"
fi

# Forward stdin and arguments to the binary.
#
# The last path that could still end a hook non-zero. A cached file can pass the
# -x test and not be runnable — a wrong-architecture asset, or a body a proxy
# served in place of the real one on a host with neither sha256sum nor shasum,
# where ACTUAL="" installs it unverified. Unlike a release window this never
# heals: the file is never re-downloaded, so it stays broken until the next
# version bump.
#
# execfail keeps the shell alive when exec cannot start the file, so fail_open
# can remove the bad cache entry and report it. Without it bash exits 126/127
# here, or worse falls back to interpreting the file as a script.
# `set +e` as well as execfail: with `set -e` still on, a failed exec kills the
# shell before any guard can run — including `if ! exec …`. On success exec
# replaces this process, so everything below it runs only on failure.
#
# This covers an exec-format failure, which is what a wrong-architecture asset
# produces. It does not cover a plain text file in the cache, where bash falls
# back to interpreting it as a script — but failing closed on integrity above
# means an unverified body can no longer be installed, so that now requires
# corruption after the fact.
shopt -s execfail
set +e
exec "$BINARY" "$@"
rm -f "$BINARY"
fail_open "cached binary could not be executed — removed it, so the next hook re-downloads"
