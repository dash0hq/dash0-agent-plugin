#!/usr/bin/env bash
# SPDX-FileCopyrightText: Copyright 2026 Dash0 Inc.
# SPDX-License-Identifier: Apache-2.0

# Bootstrap wrapper for the copilot-on-event binary. Referenced from the
# plugin-contributed copilot/hooks.json, which passes the event name as an
# argument (camelCase Copilot payloads carry no event-name field):
#
#   stdin (JSON) → copilot-on-event.sh <eventName> → copilot-on-event binary → OTLP
#
# Responsibilities:
#   - Load config from a YAML-frontmatter file, exposing DASH0_* env vars (and
#     the sensitive token as COPILOT_PLUGIN_OPTION_AUTH_TOKEN) for the binary.
#   - Download the matching binary from GitHub Releases on first run (checksum-verified).
#   - exec the binary, FORWARDING the event-name argument and stdin.
#
# Per-turn token/cost/model telemetry additionally requires Copilot's native
# OTel to be enabled to a per-session file — set up by the `dash0-configure`
# skill as a shell function that shadows `copilot`. Without it, spans are still
# emitted, just without usage.
#
# Fail-open: any error before exec logs to stderr and exits 0 so a broken
# installer never breaks the user's Copilot session. (The binary itself is also
# strictly fail-open — mandatory, since Copilot's tool hooks are fail-closed.)

set -u

fail_open() {
  echo "copilot-on-event: $*" >&2
  exit 0
}

load_settings() {
  local file="$1"
  [[ -f "$file" ]] || return 1

  local frontmatter
  frontmatter=$(sed -n '/^---$/,/^---$/{ /^---$/d; p; }' "$file")

  local enabled
  enabled=$(echo "$frontmatter" | grep '^enabled:' | sed 's/enabled: *//' | sed 's/^"\(.*\)"$/\1/' || true)
  if [[ "$enabled" == "false" ]]; then
    exit 0
  fi

  local val
  val=$(echo "$frontmatter" | grep '^otlp_url:' | sed 's/otlp_url: *//' | sed 's/^"\(.*\)"$/\1/' || true)
  [[ -n "$val" ]] && export DASH0_OTLP_URL="$val"
  val=$(echo "$frontmatter" | grep '^auth_token:' | sed 's/auth_token: *//' | sed 's/^"\(.*\)"$/\1/' || true)
  [[ -n "$val" ]] && export COPILOT_PLUGIN_OPTION_AUTH_TOKEN="$val"
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
  val=$(echo "$frontmatter" | grep '^debug:' | sed 's/debug: *//' | sed 's/^"\(.*\)"$/\1/' || true)
  [[ -n "$val" ]] && export DASH0_DEBUG="$val"
  val=$(echo "$frontmatter" | grep '^debug_file:' | sed 's/debug_file: *//' | sed 's/^"\(.*\)"$/\1/' || true)
  [[ -n "$val" ]] && export DASH0_DEBUG_FILE="$val"

  return 0
}

# v1 ships global-only config (project-scope path is a deferred decision).
load_settings "$HOME/.copilot/dash0-agent-plugin.local.md" || true

if [ -n "${COPILOT_PLUGIN_DATA:-}" ]; then
  BASE="$COPILOT_PLUGIN_DATA"
else
  BASE="${XDG_STATE_HOME:-$HOME/.local/state}/dash0-agent-plugin/copilot"
fi
BIN_DIR="$BASE/bin"
REPO="dash0hq/dash0-agent-plugin"
VERSION="0.1.20"

OS=$(uname -s | tr '[:upper:]' '[:lower:]')
ARCH=$(uname -m)
case "$ARCH" in
  x86_64)  ARCH="amd64" ;;
  aarch64) ARCH="arm64" ;;
  arm64)   ARCH="arm64" ;;
esac

BINARY="$BIN_DIR/copilot-on-event-${VERSION}-${OS}-${ARCH}"

if [ ! -x "$BINARY" ]; then
  mkdir -p "$BIN_DIR" 2>/dev/null || fail_open "could not create $BIN_DIR"
  BASE_URL="https://github.com/${REPO}/releases/download/v${VERSION}"
  ASSET="copilot-on-event-${OS}-${ARCH}"
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

  # Fail CLOSED on integrity: if we can't verify the download (no checksum line,
  # or no sha256 tool), refuse to run it. fail_open still exits 0 so the user's
  # session isn't broken — we just skip telemetry this run and re-download next.
  EXPECTED=$(echo "$CHECKSUMS" | grep "  ${ASSET}$" | cut -d' ' -f1)
  if [ -z "$EXPECTED" ]; then
    rm -f "$BINARY"
    fail_open "no checksum for ${ASSET} — refusing to run an unverified binary"
  fi
  if command -v sha256sum &>/dev/null; then
    ACTUAL=$(sha256sum "$BINARY" | cut -d' ' -f1)
  elif command -v shasum &>/dev/null; then
    ACTUAL=$(shasum -a 256 "$BINARY" | cut -d' ' -f1)
  else
    ACTUAL=""
  fi
  if [ -z "$ACTUAL" ]; then
    rm -f "$BINARY"
    fail_open "no sha256 tool (sha256sum/shasum) to verify ${ASSET} — refusing to run an unverified binary"
  fi
  if [ "$ACTUAL" != "$EXPECTED" ]; then
    rm -f "$BINARY"
    fail_open "checksum mismatch (expected $EXPECTED, got $ACTUAL)"
  fi

  chmod +x "$BINARY" || fail_open "could not mark $BINARY executable"
fi

# Forward the event-name argument(s) and stdin to the binary.
exec "$BINARY" "$@"
