#!/usr/bin/env bash
# SPDX-FileCopyrightText: Copyright 2026 Dash0 Inc.
# SPDX-License-Identifier: Apache-2.0

# Dash0 — OpenAI Codex telemetry installer.
#
# Usage:
#   curl -fsSL https://raw.githubusercontent.com/dash0hq/dash0-agent-plugin/main/install-codex.sh | bash
#
# With CLI flags (pass after `bash -s --` when piping from curl):
#   curl -fsSL .../install-codex.sh | bash -s -- \
#     --endpoint https://ingress.<region>.aws.dash0.com \
#     --token <auth-token> \
#     --dataset <dataset>
#
# All flags are optional. Any flag not provided is prompted for interactively,
# or (non-interactively) left blank — the plugin then installs but stays inactive
# until ~/.codex/dash0-agent-plugin.local.md is filled in.
#
# Flags:
#   --endpoint URL   Dash0 OTLP endpoint URL
#   --token TOKEN    Dash0 auth token
#   --dataset NAME   Dash0 dataset (defaults to "default")
#   --team NAME      Team name
#
# Env vars: DASH0_OTLP_URL, DASH0_AUTH_TOKEN, DASH0_DATASET, DASH0_TEAM_NAME,
#           DASH0_VERSION (pins a specific release).
#
# What this installs:
#   ~/.local/state/dash0-agent-plugin/codex/codex-on-event.sh
#       Bootstrap Codex invokes on each hook event.
#   ~/.local/state/dash0-agent-plugin/codex/bin/codex-on-event-<v>-<os>-<arch>
#       The binary the bootstrap execs (pre-downloaded so the connectivity check
#       can run before you restart Codex).
#   ~/.codex/dash0-agent-plugin.local.md
#       YAML-frontmatter config carrying your OTLP URL + auth token (chmod 600).
#   ~/.codex/config.toml
#       Codex reads hooks from here (there is no hooks.json). This installer
#       APPENDS a marker-delimited managed block registering the plugin's hooks
#       AND pre-trusting them (Codex requires a persisted trusted_hash or it
#       prompts via /hooks). Any hooks you authored yourself are preserved; the
#       managed block is replaced on re-install and removed by uninstall-codex.sh.

set -u

REPO="dash0hq/dash0-agent-plugin"

DASH0_OTLP_URL="${DASH0_OTLP_URL:-}"
DASH0_AUTH_TOKEN="${DASH0_AUTH_TOKEN:-}"
DASH0_DATASET="${DASH0_DATASET:-}"
DASH0_TEAM_NAME="${DASH0_TEAM_NAME:-}"

while [ $# -gt 0 ]; do
  case "$1" in
    --endpoint) [ $# -ge 2 ] || { printf "✗ --endpoint requires a value\n" >&2; exit 1; }; DASH0_OTLP_URL="$2"; shift 2 ;;
    --token)    [ $# -ge 2 ] || { printf "✗ --token requires a value\n" >&2; exit 1; }; DASH0_AUTH_TOKEN="$2"; shift 2 ;;
    --dataset)  [ $# -ge 2 ] || { printf "✗ --dataset requires a value\n" >&2; exit 1; }; DASH0_DATASET="$2"; shift 2 ;;
    --team)     [ $# -ge 2 ] || { printf "✗ --team requires a value\n" >&2; exit 1; }; DASH0_TEAM_NAME="$2"; shift 2 ;;
    -h|--help)
      cat <<'EOF'
Usage: install-codex.sh [--endpoint URL] [--token TOKEN] [--dataset NAME] [--team NAME]

All flags optional; missing ones are prompted for (or left blank non-interactively).
Env vars: DASH0_OTLP_URL, DASH0_AUTH_TOKEN, DASH0_DATASET, DASH0_TEAM_NAME, DASH0_VERSION.
EOF
      exit 0 ;;
    *) printf "✗ unknown argument: %s (try --help)\n" "$1" >&2; exit 1 ;;
  esac
done

if [ -t 1 ]; then
  C_R=$'\033[31m'; C_G=$'\033[32m'; C_Y=$'\033[33m'; C_B=$'\033[1m'; C_N=$'\033[0m'
else
  C_R=""; C_G=""; C_Y=""; C_B=""; C_N=""
fi
info()  { printf "%s\n" "$1"; }
ok()    { printf "${C_G}✓${C_N} %s\n" "$1"; }
warn()  { printf "${C_Y}!${C_N} %s\n" "$1"; }
die()   { printf "${C_R}✗${C_N} %s\n" "$1" >&2; exit 1; }

printf '%sDash0 → OpenAI Codex telemetry installer%s\n\n' "$C_B" "$C_N"

# 1. Platform detection.
OS=$(uname -s | tr '[:upper:]' '[:lower:]')
ARCH=$(uname -m)
case "$ARCH" in
  x86_64)  ARCH="amd64" ;;
  aarch64) ARCH="arm64" ;;
  arm64)   ARCH="arm64" ;;
  *)       die "unsupported architecture: $ARCH (need amd64 or arm64)" ;;
esac
case "$OS" in
  darwin|linux) : ;;
  *) die "unsupported OS: $OS (need darwin or linux)" ;;
esac
ok "detected $OS/$ARCH"

# 2. Fetch/checksum helpers.
if command -v curl >/dev/null 2>&1; then
  fetch() { curl -fsSL -o "$2" "$1"; }
  fetch_stdout() { curl -fsSL "$1"; }
elif command -v wget >/dev/null 2>&1; then
  fetch() { wget -qO "$2" "$1"; }
  fetch_stdout() { wget -qO- "$1"; }
else
  die "neither curl nor wget found"
fi
if command -v sha256sum >/dev/null 2>&1; then
  sha256() { sha256sum "$1" | cut -d' ' -f1; }
elif command -v shasum >/dev/null 2>&1; then
  sha256() { shasum -a 256 "$1" | cut -d' ' -f1; }
else
  sha256() { echo ""; }
fi

# 3. Resolve VERSION.
VERSION="${DASH0_VERSION:-}"
if [ -z "$VERSION" ]; then
  info "resolving latest release..."
  LATEST_JSON=$(fetch_stdout "https://api.github.com/repos/${REPO}/releases/latest" || true)
  VERSION=$(echo "$LATEST_JSON" | grep -m1 '"tag_name"' | cut -d'"' -f4 | sed 's/^v//')
  [ -n "$VERSION" ] || die "could not resolve latest release; set DASH0_VERSION to pin a specific version"
fi
ok "using v${VERSION}"

# 4. Paths.
STATE_BASE="${XDG_STATE_HOME:-$HOME/.local/state}/dash0-agent-plugin/codex"
BIN_DIR="$STATE_BASE/bin"
BIN_PATH="$BIN_DIR/codex-on-event-${VERSION}-${OS}-${ARCH}"
SCRIPT_PATH="$STATE_BASE/codex-on-event.sh"

CONFIG_PATH="$HOME/.codex/dash0-agent-plugin.local.md"
CONFIG_TOML="$HOME/.codex/config.toml"

mkdir -p "$BIN_DIR" "$HOME/.codex" || die "could not create install directories"

# 5. Download the binary with checksum verification.
BASE_URL="https://github.com/${REPO}/releases/download/v${VERSION}"
BIN_ASSET="codex-on-event-${OS}-${ARCH}"
RAW_BASE="https://raw.githubusercontent.com/${REPO}/v${VERSION}"

info "downloading codex-on-event v${VERSION}..."
fetch "$BASE_URL/$BIN_ASSET" "$BIN_PATH" || die "failed to download binary: $BASE_URL/$BIN_ASSET"
CHECKSUMS=$(fetch_stdout "$BASE_URL/checksums.txt" || true)
if [ -n "$CHECKSUMS" ]; then
  EXPECTED=$(echo "$CHECKSUMS" | grep "  ${BIN_ASSET}\$" | cut -d' ' -f1)
  if [ -n "$EXPECTED" ]; then
    ACTUAL=$(sha256 "$BIN_PATH")
    if [ -n "$ACTUAL" ] && [ "$ACTUAL" != "$EXPECTED" ]; then
      rm -f "$BIN_PATH"; die "checksum mismatch for $BIN_ASSET (expected $EXPECTED, got $ACTUAL)"
    fi
  fi
fi
chmod +x "$BIN_PATH"
ok "installed binary → $BIN_PATH"

# 5b. Install the bootstrap script from the tagged ref.
info "downloading codex-on-event.sh..."
fetch "$RAW_BASE/scripts/codex-on-event.sh" "$SCRIPT_PATH" || die "failed to download: $RAW_BASE/scripts/codex-on-event.sh"
chmod +x "$SCRIPT_PATH"
ok "installed bootstrap → $SCRIPT_PATH"

# 6. Collect configuration (env var > interactive prompt > skip).
prompt_value() {
  local var="$1" label="$2" default="${3:-}"; local val="${!var:-}"
  if [ -z "$val" ]; then
    if [ -r /dev/tty ]; then
      if [ -n "$default" ]; then printf "%s [%s]: " "$label" "$default" > /dev/tty; else printf "%s: " "$label" > /dev/tty; fi
      IFS= read -r val < /dev/tty || val=""; val="${val:-$default}"
    else val="$default"; fi
  fi
  printf -v "$var" "%s" "$val"
}
prompt_secret() {
  local var="$1" label="$2"; local val="${!var:-}"
  if [ -z "$val" ] && [ -r /dev/tty ]; then
    printf "%s (input hidden): " "$label" > /dev/tty
    stty -echo < /dev/tty 2>/dev/null; IFS= read -r val < /dev/tty || val=""; stty echo < /dev/tty 2>/dev/null
    printf "\n" > /dev/tty
  fi
  printf -v "$var" "%s" "$val"
}

DASH0_AGENT_NAME="codex"
prompt_value  DASH0_OTLP_URL    "Dash0 OTLP endpoint URL (e.g. https://ingress.<region>.aws.dash0.com)"
prompt_secret DASH0_AUTH_TOKEN  "Dash0 auth token"
prompt_value  DASH0_DATASET     "Dash0 dataset (optional)" "default"
prompt_value  DASH0_TEAM_NAME   "Team name (optional)"

if [ -z "$DASH0_OTLP_URL" ] || [ -z "$DASH0_AUTH_TOKEN" ]; then
  warn "OTLP URL or auth token not provided. The plugin will install but stay inactive."
  warn "Re-run with DASH0_OTLP_URL and DASH0_AUTH_TOKEN set, or edit $CONFIG_PATH later."
fi

# 7. Write the config file (chmod 600 — holds the token in cleartext).
{
  echo "---"
  echo "otlp_url: \"$DASH0_OTLP_URL\""
  echo "auth_token: \"$DASH0_AUTH_TOKEN\""
  [ -n "$DASH0_DATASET" ]    && echo "dataset: \"$DASH0_DATASET\""
  [ -n "$DASH0_AGENT_NAME" ] && echo "agent_name: \"$DASH0_AGENT_NAME\""
  [ -n "$DASH0_TEAM_NAME" ]  && echo "team_name: \"$DASH0_TEAM_NAME\""
  echo "---"
} > "$CONFIG_PATH"
chmod 600 "$CONFIG_PATH"
ok "wrote config → $CONFIG_PATH (chmod 600)"

# 8. Merge hooks + pre-trust into ~/.codex/config.toml.
#    Codex reads hooks from config.toml and requires a persisted trusted_hash to
#    run them without a /hooks prompt. The binary emits both the [[hooks.*]]
#    blocks and the matching [hooks.state] trust entries, wrapped in markers.
#    We first strip any prior managed block (so re-install is clean and group
#    indices are recomputed against the user's own hooks), then append the fresh
#    block. User-authored hooks outside the markers are never touched.
info "registering + pre-trusting hooks in ${CONFIG_TOML}..."
HOOK_CMD="bash \"$SCRIPT_PATH\""

if [ -f "$CONFIG_TOML" ]; then
  STRIPPED_TMP=$(mktemp)
  awk '
    index($0, ">>> dash0-agent-plugin (managed)") { skip=1 }
    !skip { print }
    index($0, "<<< dash0-agent-plugin (managed)") { skip=0 }
  ' "$CONFIG_TOML" > "$STRIPPED_TMP" || { rm -f "$STRIPPED_TMP"; die "failed to read $CONFIG_TOML"; }
  # Drop a trailing blank line left by a removed block, then keep the user content.
  mv "$STRIPPED_TMP" "$CONFIG_TOML"
fi

BLOCK=$("$BIN_PATH" emit-codex-hooks --config "$CONFIG_TOML" --command "$HOOK_CMD") \
  || die "failed to render hook config"

# Separate from any preceding content with a blank line, then append.
if [ -s "$CONFIG_TOML" ]; then printf "\n" >> "$CONFIG_TOML"; fi
printf "%s" "$BLOCK" >> "$CONFIG_TOML" || die "failed to write $CONFIG_TOML"
ok "registered + pre-trusted hooks (managed block in $CONFIG_TOML)"

# 9. Connectivity check.
if [ -n "$DASH0_OTLP_URL" ] && [ -n "$DASH0_AUTH_TOKEN" ]; then
  info "running connectivity check..."
  CHECK_OUT=$(
    echo '{"hook_event_name":"SessionStart","session_id":"install-check","model":"gpt-5.5","source":"startup"}' \
      | DASH0_OTLP_URL="$DASH0_OTLP_URL" \
        CODEX_PLUGIN_OPTION_AUTH_TOKEN="$DASH0_AUTH_TOKEN" \
        DASH0_DATASET="$DASH0_DATASET" \
        DASH0_PLUGIN_DATA="$(mktemp -d)" \
        "$BIN_PATH" 2>&1 || true
  )
  case "$CHECK_OUT" in
    *"connectivity check failed"*) warn "connectivity check failed:"; printf "    %s\n" "$CHECK_OUT" ;;
    *"connected"*)                 ok "connectivity check passed" ;;
    *)                             warn "connectivity check returned unexpected output:"; printf "    %s\n" "$CHECK_OUT" ;;
  esac
fi

# 10. Done.
printf '\n%sNext steps%s\n' "$C_B" "$C_N"
printf "  1. Start a new Codex session (existing sessions won't pick up the new hooks).\n"
printf "  2. Run a prompt in any repo. Spans should land in your Dash0 dataset with gen_ai.harness.name=codex.\n"
printf "\nHooks are pre-trusted, so Codex should not prompt via /hooks. If it does, run /hooks and trust 'dash0'.\n"
printf "To reconfigure later, edit %s (no restart needed).\n" "$CONFIG_PATH"
printf "To uninstall: curl -fsSL https://raw.githubusercontent.com/%s/main/uninstall-codex.sh | bash\n" "$REPO"
