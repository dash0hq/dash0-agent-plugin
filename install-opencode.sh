#!/usr/bin/env bash
# SPDX-FileCopyrightText: Copyright 2026 Dash0 Inc.
# SPDX-License-Identifier: Apache-2.0

# Dash0 — OpenCode telemetry installer.
#
# The npm path (adding "@dash0/opencode-plugin" to opencode.json's plugin array)
# is the other supported way in. This script is the one that needs no npm
# registry access: it downloads the same two files from GitHub Releases and drops
# them where OpenCode auto-loads plugins.
#
# Usage:
#   curl -fsSL https://raw.githubusercontent.com/dash0hq/dash0-agent-plugin/main/install-opencode.sh | bash
#
# With CLI flags (pass after `bash -s --` when piping from curl):
#   curl -fsSL .../install-opencode.sh | bash -s -- \
#     --endpoint https://ingress.<region>.aws.dash0.com \
#     --token <auth-token> \
#     --dataset <dataset>
#
# All flags are optional. Any flag not provided is prompted for interactively,
# or (non-interactively) left blank — the plugin then installs but stays inactive
# until ~/.config/opencode/dash0-agent-plugin.local.md is filled in.
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
#   ~/.config/opencode/plugin/dash0-opencode-plugin.js
#       The plugin OpenCode loads. Everything in ~/.config/opencode/plugin/ is
#       picked up automatically, so there is no registration step.
#   ~/.config/opencode/plugin/opencode-on-event.sh
#       The bootstrap the plugin spawns per event. It lives next to the plugin
#       because that is the first place the plugin looks for it.
#   ~/.local/state/dash0-agent-plugin/opencode/bin/opencode-on-event-<v>-<os>-<arch>
#       The binary the bootstrap execs (pre-downloaded so the connectivity check
#       can run before you start OpenCode).
#   ~/.config/opencode/dash0-agent-plugin.local.md
#       YAML-frontmatter config carrying your OTLP URL + auth token (chmod 600).
#   ~/.config/opencode/opencode.json
#       Written only when absent, and only as an empty config. An existing file
#       is never touched — the plugin needs no entry in it.

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
Usage: install-opencode.sh [--endpoint URL] [--token TOKEN] [--dataset NAME] [--team NAME]

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

printf '%sDash0 → OpenCode telemetry installer%s\n\n' "$C_B" "$C_N"

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
STATE_BASE="${XDG_STATE_HOME:-$HOME/.local/state}/dash0-agent-plugin/opencode"
BIN_DIR="$STATE_BASE/bin"
BIN_PATH="$BIN_DIR/opencode-on-event-${VERSION}-${OS}-${ARCH}"

OPENCODE_DIR="$HOME/.config/opencode"
PLUGIN_DIR="$OPENCODE_DIR/plugin"
PLUGIN_PATH="$PLUGIN_DIR/dash0-opencode-plugin.js"
SCRIPT_PATH="$PLUGIN_DIR/opencode-on-event.sh"

CONFIG_PATH="$OPENCODE_DIR/dash0-agent-plugin.local.md"
OPENCODE_JSON="$OPENCODE_DIR/opencode.json"

mkdir -p "$BIN_DIR" "$PLUGIN_DIR" || die "could not create install directories"

BASE_URL="https://github.com/${REPO}/releases/download/v${VERSION}"
RAW_BASE="https://raw.githubusercontent.com/${REPO}/v${VERSION}"

# Fetch checksums once: both release assets below are verified against it. A
# missing entry is fatal rather than skipped — OpenCode has no pre-checksum
# releases, so "not listed" can only mean the bytes are not the published asset.
CHECKSUMS=$(fetch_stdout "$BASE_URL/checksums.txt") \
  || die "could not fetch $BASE_URL/checksums.txt"

verify_asset() {
  # verify_asset <downloaded-path> <asset-name>
  local path="$1" asset="$2" expected actual
  expected=$(printf '%s\n' "$CHECKSUMS" | awk -v want="$asset" '$2 == want { print $1 }')
  if [ -z "$expected" ]; then
    rm -f "$path"; die "$asset is not listed in checksums.txt for v${VERSION}"
  fi
  actual=$(sha256 "$path")
  if [ -n "$actual" ] && [ "$actual" != "$expected" ]; then
    rm -f "$path"; die "checksum mismatch for $asset (expected $expected, got $actual)"
  fi
}

# 5. Download the binary.
#    The path is version-pinned, so an already-present binary is exactly this
#    version — skip the download (idempotent re-install; also lets a pre-staged
#    binary work). A version bump changes BIN_PATH, forcing a fetch.
if [ -x "$BIN_PATH" ]; then
  ok "binary already present → $BIN_PATH"
else
  BIN_ASSET="opencode-on-event-${OS}-${ARCH}"
  info "downloading ${BIN_ASSET} v${VERSION}..."
  fetch "$BASE_URL/$BIN_ASSET" "$BIN_PATH" || die "failed to download binary: $BASE_URL/$BIN_ASSET"
  verify_asset "$BIN_PATH" "$BIN_ASSET"
  chmod +x "$BIN_PATH"
  ok "installed binary → $BIN_PATH"
fi

# 6. Install the plugin bundle and the bootstrap it spawns.
#    Both are overwritten unconditionally: unlike the version-pinned binary,
#    their paths do not encode a version, so a re-install is the only way they
#    are ever refreshed.
PLUGIN_ASSET="dash0-opencode-plugin.js"
info "downloading ${PLUGIN_ASSET}..."
fetch "$BASE_URL/$PLUGIN_ASSET" "$PLUGIN_PATH" || die "failed to download: $BASE_URL/$PLUGIN_ASSET"
verify_asset "$PLUGIN_PATH" "$PLUGIN_ASSET"
ok "installed plugin → $PLUGIN_PATH"

info "downloading opencode-on-event.sh..."
fetch "$RAW_BASE/opencode/opencode-on-event.sh" "$SCRIPT_PATH" \
  || die "failed to download: $RAW_BASE/opencode/opencode-on-event.sh"
chmod +x "$SCRIPT_PATH"
ok "installed bootstrap → $SCRIPT_PATH"

# 7. Collect configuration (env var > interactive prompt > skip).
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

DASH0_AGENT_NAME="opencode"
prompt_value  DASH0_OTLP_URL    "Dash0 OTLP endpoint URL (e.g. https://ingress.<region>.aws.dash0.com)"
prompt_secret DASH0_AUTH_TOKEN  "Dash0 auth token"
prompt_value  DASH0_DATASET     "Dash0 dataset (optional)" "default"
prompt_value  DASH0_TEAM_NAME   "Team name (optional)"

if [ -z "$DASH0_OTLP_URL" ] || [ -z "$DASH0_AUTH_TOKEN" ]; then
  warn "OTLP URL or auth token not provided. The plugin will install but stay inactive."
  warn "Re-run with DASH0_OTLP_URL and DASH0_AUTH_TOKEN set, or edit $CONFIG_PATH later."
fi

# 8. Write the config file (chmod 600 — holds the token in cleartext).
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

# 9. Seed opencode.json when the user has none, so there is somewhere obvious to
#    put OpenCode settings. An existing file is left exactly as it is: the
#    plugin directory is auto-loaded, so nothing has to be registered in it.
if [ -e "$OPENCODE_JSON" ]; then
  info "left your existing $OPENCODE_JSON untouched"
else
  # shellcheck disable=SC2016 # literal $schema: it is an OpenCode config key, not a shell variable
  printf '{\n  "$schema": "https://opencode.ai/config.json"\n}\n' > "$OPENCODE_JSON" \
    || die "could not write $OPENCODE_JSON"
  ok "wrote $OPENCODE_JSON"
fi

# 10. Connectivity check. The envelope is the one the plugin sends for a new
#     session; the pipeline answers it with the check whose result is printed.
if [ -n "$DASH0_OTLP_URL" ] && [ -n "$DASH0_AUTH_TOKEN" ]; then
  info "running connectivity check..."
  CHECK_OUT=$(
    echo '{"kind":"event","name":"session.created","payload":{"properties":{"info":{"id":"install-check","directory":"/tmp"}}},"cwd":"/tmp","root_session_id":"install-check","assistants":{}}' \
      | DASH0_OTLP_URL="$DASH0_OTLP_URL" \
        OPENCODE_PLUGIN_OPTION_AUTH_TOKEN="$DASH0_AUTH_TOKEN" \
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

# 11. Done.
printf '\n%sNext steps%s\n' "$C_B" "$C_N"
printf "  1. Start a new OpenCode session (a running one won't pick up the new plugin).\n"
printf "  2. Run a prompt in any repo. Spans should land in your Dash0 dataset with gen_ai.harness.name=opencode.\n"
printf "\nTo reconfigure later, edit %s (no restart needed).\n" "$CONFIG_PATH"
printf "To uninstall: curl -fsSL https://raw.githubusercontent.com/%s/main/uninstall-opencode.sh | bash\n" "$REPO"
