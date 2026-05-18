#!/usr/bin/env bash

set -euo pipefail

# Locate (and if needed download) the on-event binary, then run its
# `login` subcommand. Mirrors the download / checksum-verify logic in
# scripts/on-event.sh so the slash command works out-of-the-box after
# `claude plugin install dash0-agent-plugin`.

PLUGIN_DATA="${CLAUDE_PLUGIN_DATA:-$HOME/.claude/plugins/data/dash0-agent-plugin}"
BIN_DIR="$PLUGIN_DATA/bin"
REPO="dash0hq/dash0-agent-plugin"
VERSION="0.2.0"

OS=$(uname -s | tr '[:upper:]' '[:lower:]')
ARCH=$(uname -m)
case "$ARCH" in
  x86_64)  ARCH="amd64" ;;
  aarch64) ARCH="arm64" ;;
  arm64)   ARCH="arm64" ;;
esac

BINARY="$BIN_DIR/on-event-${VERSION}-${OS}-${ARCH}"

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
    echo "dash0-login: neither curl nor wget found" >&2
    exit 1
  fi

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
      echo "dash0-login: checksum mismatch (expected $EXPECTED, got $ACTUAL)" >&2
      rm -f "$BINARY"
      exit 1
    fi
  fi

  chmod +x "$BINARY"
fi

exec "$BINARY" login "$@"
