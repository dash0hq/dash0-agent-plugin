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
#   - Load configuration from a YAML-frontmatter config file (per-project or
#     global), exposing values as DASH0_* env vars for the binary.
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

# Load settings from a YAML-frontmatter config file. Returns 1 if the file
# doesn't exist so callers can fall through to the next location.
load_settings() {
  local file="$1"
  [[ -f "$file" ]] || return 1

  local frontmatter
  frontmatter=$(sed -n '/^---$/,/^---$/{ /^---$/d; p; }' "$file")

  local enabled
  enabled=$(echo "$frontmatter" | grep '^enabled:' | sed 's/enabled: *//' || true)
  if [[ "$enabled" == "false" ]]; then
    exit 0
  fi

  local val
  val=$(echo "$frontmatter" | grep '^otlp_url:' | sed 's/otlp_url: *//' | sed 's/^"\(.*\)"$/\1/' || true)
  [[ -n "$val" ]] && export DASH0_OTLP_URL="$val"
  val=$(echo "$frontmatter" | grep '^auth_token:' | sed 's/auth_token: *//' | sed 's/^"\(.*\)"$/\1/' || true)
  [[ -n "$val" ]] && export CURSOR_PLUGIN_OPTION_AUTH_TOKEN="$val"
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

# Project-scoped settings take precedence over global settings.
# Cursor sets the workspace as CWD when running hooks.
PROJECT_SETTINGS=".cursor/dash0-agent-plugin.local.md"
GLOBAL_SETTINGS="$HOME/.cursor/dash0-agent-plugin.local.md"

load_settings "$PROJECT_SETTINGS" || load_settings "$GLOBAL_SETTINGS" || true

# Where the downloaded binary lives. Mirrors the per-source scratch root
# layout from cmd/cursor-on-event/main.go so users can clean up the whole
# tree at once.
BASE="${DASH0_PLUGIN_DATA:-${XDG_STATE_HOME:-$HOME/.local/state}/dash0-agent-plugin/cursor}"
BIN_DIR="$BASE/bin"
REPO="dash0hq/dash0-agent-plugin"
VERSION="0.1.25"

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

  # Download to a private temp and rename into place. Hooks run concurrently and
  # every session on the machine shares this directory, so several processes can
  # find no binary at once; writing $BINARY directly let them interleave into one
  # file. rename(2) is atomic within a directory, so the others see either no file
  # or a complete one. The trap covers the fail_open paths below.
  TMP="$BINARY.tmp.$$"
  trap 'rm -f "$TMP"' EXIT

  BASE_URL="https://github.com/${REPO}/releases/download/v${VERSION}"
  ASSET="cursor-on-event-${OS}-${ARCH}"
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

  EXPECTED=$(echo "$CHECKSUMS" | grep "  ${ASSET}$" | cut -d' ' -f1)
  if [ -n "$EXPECTED" ]; then
    if command -v sha256sum &>/dev/null; then
      ACTUAL=$(sha256sum "$TMP" | cut -d' ' -f1)
    elif command -v shasum &>/dev/null; then
      ACTUAL=$(shasum -a 256 "$TMP" | cut -d' ' -f1)
    else
      ACTUAL=""
    fi
    if [ -n "$ACTUAL" ] && [ "$ACTUAL" != "$EXPECTED" ]; then
      fail_open "checksum mismatch (expected $EXPECTED, got $ACTUAL)"
    fi
  fi

  # Executable before it is visible, so nothing can find $BINARY and fail the
  # -x test that guards this block.
  chmod +x "$TMP" || fail_open "could not mark $TMP executable"
  mv -f "$TMP" "$BINARY" || fail_open "could not move $TMP into place"
fi

# Forward stdin to the binary. The binary itself exits 0 on telemetry errors
# (see cmd/cursor-on-event/main.go) so we don't need to wrap this in a trap.
shopt -s execfail
exec "$BINARY" || true
exit 0
