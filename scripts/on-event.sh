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

  return 0
}

# Load settings: project-level takes precedence, then global.
PROJECT_SETTINGS=".claude/dash0-agent-plugin.local.md"
GLOBAL_SETTINGS="$HOME/.claude/dash0-agent-plugin.local.md"

load_settings "$PROJECT_SETTINGS" || load_settings "$GLOBAL_SETTINGS" || true

PLUGIN_DATA="${CLAUDE_PLUGIN_DATA:?CLAUDE_PLUGIN_DATA not set}"
BIN_DIR="$PLUGIN_DATA/bin"
REPO="dash0hq/dash0-agent-plugin"
VERSION="0.1.19"

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
  ASSET="on-event-${OS}-${ARCH}"
  URL="${BASE_URL}/${ASSET}"
  CHECKSUMS_URL="${BASE_URL}/checksums.txt"

  if command -v curl &>/dev/null; then
    curl -fsSL -o "$BINARY" "$URL"
    CHECKSUMS=$(curl -fsSL "$CHECKSUMS_URL")
  elif command -v wget &>/dev/null; then
    wget -qO "$BINARY" "$URL"
    CHECKSUMS=$(wget -qO- "$CHECKSUMS_URL")
  else
    echo "on-event: neither curl nor wget found" >&2
    exit 1
  fi

  # Verify checksum.
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
      echo "on-event: checksum mismatch (expected $EXPECTED, got $ACTUAL)" >&2
      rm -f "$BINARY"
      exit 1
    fi
  fi

  chmod +x "$BINARY"
fi

# Forward stdin and arguments to the binary.
exec "$BINARY" "$@"
