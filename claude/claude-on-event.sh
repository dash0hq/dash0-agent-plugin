#!/usr/bin/env bash
# SPDX-FileCopyrightText: Copyright 2026 Dash0 Inc.
# SPDX-License-Identifier: Apache-2.0

set -euo pipefail

# Nothing here is worth interrupting a session for. Claude renders a non-zero,
# non-2 hook exit as an error on every event, so every path that ends in "we
# can't safely run the binary" exits 0 instead — as cursor, codex and copilot
# already do. `set -e` still governs the unanticipated cases.
#
# But exiting 0 alone would hide a real breakage. Two very different things look
# identical on the first attempt:
#
#   transient  the minute between a release bump landing on main and its
#              binaries publishing. Resolves itself; not worth a word.
#   persistent a proxy blocking github.com, no curl, an unsupported platform, a
#              release that never published. Never resolves — and silence here
#              means someone believes telemetry works while no data arrives.
#
# Time tells them apart. Note the first failure, stay quiet for GRACE, and after
# that speak through the plugin's own channel — a systemMessage, the same way
# the binary reports a session link — roughly once every REMIND. "Roughly"
# because the marker is a read-modify-write with no lock: hooks firing in the
# same instant can each read the old timestamp and each print. The cost is a
# duplicated one-line notice, which is not worth a lockfile in a hot path.

GRACE=600      # 10 min: an order of magnitude past any release window
REMIND=3600    # roughly hourly; hooks fire many times a turn

fail_open() {
  echo "on-event: $*" >&2

  local marker="${BIN_DIR:-}/.download-failing" now ver first notified
  now=$(date +%s)
  if [ -n "${BIN_DIR:-}" ]; then
    mkdir -p "$BIN_DIR" 2>/dev/null || true
    read -r ver first notified <<<"$(cat "$marker" 2>/dev/null || true)"
    # Keyed by version. Hooks race, so a straggler can write a marker moments
    # after a faster sibling finished the download and cleared it, and nothing
    # afterwards takes the `-x` branch that would clear it again. Left unkeyed,
    # that stale timestamp would be days old at the next bump and fire the alarm
    # on the first release-window failure — the exact case GRACE exists to
    # suppress.
    if [ "${ver:-}" != "$VERSION" ] || [ -z "${first:-}" ]; then
      # First failure for this version. Assume it is the release window.
      echo "$VERSION $now 0" >"$marker" 2>/dev/null || true
    elif [ $((now - first)) -gt "$GRACE" ] && [ $((now - ${notified:-0})) -gt "$REMIND" ]; then
      echo "$VERSION $first $now" >"$marker" 2>/dev/null || true
      # Fixed text plus the version, which is semver-safe. The error itself goes
      # to stderr only — interpolating it here could emit invalid JSON.
      # \\n, not \n: the leading newline must reach Claude Code as the two-character
      # JSON escape. printf would turn a single backslash into a real newline,
      # which is a control character and illegal inside a JSON string.
      printf '{"systemMessage":"\\ndash0: cannot download the v%s binary, so no telemetry is being sent. Run claude with --debug for the reason."}\n' "$VERSION"
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

# Claude Code always sets this. If it somehow does not, fail open like every
# other path rather than aborting: `:?` exits 1, which is the hook error this
# script exists to avoid, and it happens before fail_open is reachable.
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
    # Neither tool present. The READMEs list one of them as a requirement; the
    # binary is still used, as before, so a minimal host is not broken by this.
    ACTUAL=""
  fi
  if [ -n "$ACTUAL" ] && [ "$ACTUAL" != "$EXPECTED" ]; then
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
exec "$BINARY" "$@"
