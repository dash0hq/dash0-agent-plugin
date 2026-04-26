#!/usr/bin/env bash

set -euo pipefail

PLUGIN_DATA="${CLAUDE_PLUGIN_DATA:?CLAUDE_PLUGIN_DATA not set}"
BIN_DIR="$PLUGIN_DATA/bin"
REPO="dash0hq/dash0-agent-plugin"
VERSION="0.1.0"

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
