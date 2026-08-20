#!/usr/bin/env bash
# SPDX-FileCopyrightText: Copyright 2026 Dash0 Inc.
# SPDX-License-Identifier: Apache-2.0

set -euo pipefail

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

PLUGIN_DATA="${CLAUDE_PLUGIN_DATA:?CLAUDE_PLUGIN_DATA not set}"
BIN_DIR="$PLUGIN_DATA/bin"
REPO="dash0hq/dash0-agent-plugin"
VERSION="0.1.24"

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
  mkdir -p "$BIN_DIR"
  BASE_URL="https://github.com/${REPO}/releases/download/v${VERSION}"
  CHECKSUMS_URL="${BASE_URL}/checksums.txt"

  if command -v curl &>/dev/null; then
    fetch() { curl -fsSL -o "$2" "$1"; }
    fetch_stdout() { curl -fsSL "$1"; }
  elif command -v wget &>/dev/null; then
    fetch() { wget -qO "$2" "$1"; }
    fetch_stdout() { wget -qO- "$1"; }
  else
    echo "on-event: neither curl nor wget found" >&2
    exit 1
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
    if fetch "${BASE_URL}/${CANDIDATE}" "$BINARY" 2>/dev/null; then
      ASSET="$CANDIDATE"
      break
    fi
  done
  if [ -z "$ASSET" ]; then
    echo "on-event: no release asset for ${OS}-${ARCH} in v${VERSION}" >&2
    rm -f "$BINARY"
    exit 1
  fi
  CHECKSUMS=$(fetch_stdout "$CHECKSUMS_URL")

  # Verify the checksum. A missing entry is fatal, not skipped: the first asset
  # name tried is one that current releases do not publish, so treating "not in
  # checksums.txt" as "nothing to check" would accept an unverified binary served
  # under that name.
  # awk, not grep: `set -o pipefail` is on, so a grep that matches nothing would
  # abort the script at this assignment and skip the diagnostic below. awk exits 0
  # whether or not it matched.
  EXPECTED=$(printf '%s\n' "$CHECKSUMS" | awk -v want="$ASSET" '$2 == want { print $1 }')
  if [ -z "$EXPECTED" ]; then
    echo "on-event: ${ASSET} is not listed in checksums.txt for v${VERSION}" >&2
    rm -f "$BINARY"
    exit 1
  fi
  if command -v sha256sum &>/dev/null; then
    ACTUAL=$(sha256sum "$BINARY" | cut -d' ' -f1)
  elif command -v shasum &>/dev/null; then
    ACTUAL=$(shasum -a 256 "$BINARY" | cut -d' ' -f1)
  else
    # Neither tool present. The READMEs list one of them as a requirement; the
    # binary is still used, as before, so a minimal host is not broken by this.
    ACTUAL=""
  fi
  if [ -n "$ACTUAL" ] && [ "$ACTUAL" != "$EXPECTED" ]; then
    echo "on-event: checksum mismatch (expected $EXPECTED, got $ACTUAL)" >&2
    rm -f "$BINARY"
    exit 1
  fi

  chmod +x "$BINARY"
fi

# Forward stdin and arguments to the binary.
exec "$BINARY" "$@"
