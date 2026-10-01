#!/usr/bin/env bash
# SPDX-FileCopyrightText: Copyright 2026 Dash0 Inc.
# SPDX-License-Identifier: Apache-2.0

# Bootstrap wrapper for the opencode-on-event binary. Spawned once per canonical
# event by the OpenCode TypeScript plugin, which writes the event JSON to stdin:
#
#   plugin → opencode-on-event.sh → opencode-on-event binary → OTLP
#
# Responsibilities:
#   - Load configuration from a YAML-frontmatter config file (per-project or
#     user-scoped), exposing values as DASH0_* env vars for the binary.
#   - Resolve the auth token from the macOS keychain when configured.
#   - Detect OS/arch and download the matching opencode-on-event binary from
#     GitHub Releases on first run, verifying the checksum.
#   - exec the binary, forwarding stdin.
#
# Fail-open: any error before exec'ing the binary logs to stderr and exits 0
# so a broken installer never breaks the user's OpenCode session.

# Note: we deliberately do NOT use `set -e`; fail_open converts any failure into
# a stderr log and a clean exit so OpenCode's agent loop is never blocked by
# telemetry plumbing.
set -u

fail_open() {
  echo "opencode-on-event: $*" >&2
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
  [[ -n "$val" ]] && export OPENCODE_PLUGIN_OPTION_AUTH_TOKEN="$val"
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

# Project-scoped settings take precedence over user-scoped settings, and the two
# never merge. The plugin chdirs the spawned wrapper to the OpenCode project
# directory, so the relative path below resolves against the user's project.
PROJECT_SETTINGS=".opencode/dash0-agent-plugin.local.md"
GLOBAL_SETTINGS="$HOME/.config/opencode/dash0-agent-plugin.local.md"

load_settings "$PROJECT_SETTINGS" || load_settings "$GLOBAL_SETTINGS" || true

# Resolve the auth token from the macOS keychain when a keychain reference is
# configured. This lets managed/MDM rollouts ship only a pointer while the secret
# is provisioned separately via `security add-generic-password`. A successful
# lookup takes precedence over an inline auth_token. Silently no-ops on non-macOS
# or when `security` is absent.
KEYCHAIN_SERVICE="${OPENCODE_PLUGIN_OPTION_AUTH_TOKEN_KEYCHAIN_SERVICE:-${DASH0_AUTH_TOKEN_KEYCHAIN_SERVICE:-}}"
KEYCHAIN_ACCOUNT="${OPENCODE_PLUGIN_OPTION_AUTH_TOKEN_KEYCHAIN_ACCOUNT:-${DASH0_AUTH_TOKEN_KEYCHAIN_ACCOUNT:-}}"
if [[ -n "$KEYCHAIN_SERVICE" ]] && command -v security &>/dev/null; then
  if [[ -n "$KEYCHAIN_ACCOUNT" ]]; then
    keychain_token=$(security find-generic-password -s "$KEYCHAIN_SERVICE" -a "$KEYCHAIN_ACCOUNT" -w 2>/dev/null || true)
  else
    keychain_token=$(security find-generic-password -s "$KEYCHAIN_SERVICE" -w 2>/dev/null || true)
  fi
  [[ -n "$keychain_token" ]] && export OPENCODE_PLUGIN_OPTION_AUTH_TOKEN="$keychain_token"
fi

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
  BASE_URL="https://github.com/${REPO}/releases/download/v${VERSION}"
  ASSET="opencode-on-event-${OS}-${ARCH}"
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

  # A missing entry is fatal rather than skipped: OpenCode has no pre-checksum
  # releases, so "not in checksums.txt" can only mean the downloaded bytes are
  # not the published asset.
  # awk, not grep: awk exits 0 whether or not it matched, so the diagnostic below
  # is reached instead of a pipeline failure.
  EXPECTED=$(printf '%s\n' "$CHECKSUMS" | awk -v want="$ASSET" '$2 == want { print $1 }')
  if [ -z "$EXPECTED" ]; then
    rm -f "$BINARY"
    fail_open "${ASSET} is not listed in checksums.txt for v${VERSION}"
  fi
  ACTUAL=$(sha256_of "$BINARY")
  if [ -n "$ACTUAL" ] && [ "$ACTUAL" != "$EXPECTED" ]; then
    rm -f "$BINARY"
    fail_open "checksum mismatch (expected $EXPECTED, got $ACTUAL)"
  fi
  # No digest is recorded when neither hashing tool exists, which is also the one
  # case where the re-verification above has to let the cached binary through.
  if [ -n "$ACTUAL" ]; then
    printf '%s\n' "$ACTUAL" >"$DIGEST_FILE" || fail_open "could not record digest at $DIGEST_FILE"
  fi

  chmod +x "$BINARY" || fail_open "could not mark $BINARY executable"
fi

# Forward stdin to the binary. The binary itself exits 0 on telemetry errors
# (see cmd/opencode-on-event/main.go) so we don't need to wrap this in a trap.
exec "$BINARY"
