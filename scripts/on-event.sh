#!/usr/bin/env bash

set -euo pipefail

# Read plugin settings from .claude/dash0-agent-plugin.local.md if present.
SETTINGS_FILE=".claude/dash0-agent-plugin.local.md"
if [[ -f "$SETTINGS_FILE" ]]; then
  FRONTMATTER=$(sed -n '/^---$/,/^---$/{ /^---$/d; p; }' "$SETTINGS_FILE")

  # Check enabled flag (default: true if file exists but field is absent).
  ENABLED=$(echo "$FRONTMATTER" | grep '^enabled:' | sed 's/enabled: *//')
  if [[ "$ENABLED" == "false" ]]; then
    exit 0
  fi

  val=$(echo "$FRONTMATTER" | grep '^otlp_url:' | sed 's/otlp_url: *//' | sed 's/^"\(.*\)"$/\1/')
  [[ -n "$val" ]] && export DASH0_OTLP_URL="$val"
  val=$(echo "$FRONTMATTER" | grep '^auth_token:' | sed 's/auth_token: *//' | sed 's/^"\(.*\)"$/\1/')
  [[ -n "$val" ]] && export DASH0_AUTH_TOKEN="$val"
  val=$(echo "$FRONTMATTER" | grep '^dataset:' | sed 's/dataset: *//' | sed 's/^"\(.*\)"$/\1/')
  [[ -n "$val" ]] && export DASH0_DATASET="$val"
  val=$(echo "$FRONTMATTER" | grep '^agent_name:' | sed 's/agent_name: *//' | sed 's/^"\(.*\)"$/\1/')
  [[ -n "$val" ]] && export DASH0_AGENT_NAME="$val"
fi

PLUGIN_DATA="${CLAUDE_PLUGIN_DATA:?CLAUDE_PLUGIN_DATA not set}"
BIN_DIR="$PLUGIN_DATA/bin"
REPO="dash0hq/dash0-agent-plugin"
VERSION="0.1.2"

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
  URL="https://github.com/${REPO}/releases/download/v${VERSION}/on-event-${OS}-${ARCH}"
  if command -v curl &>/dev/null; then
    curl -fsSL -o "$BINARY" "$URL"
  elif command -v wget &>/dev/null; then
    wget -qO "$BINARY" "$URL"
  else
    echo "on-event: neither curl nor wget found" >&2
    exit 1
  fi
  chmod +x "$BINARY"
fi

# Forward stdin to the binary.
exec "$BINARY"
