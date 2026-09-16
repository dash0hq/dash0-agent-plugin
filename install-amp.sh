#!/usr/bin/env bash
# SPDX-FileCopyrightText: Copyright 2026 Dash0 Inc.
# SPDX-License-Identifier: Apache-2.0

# Dash0 — Amp CLI and Orb telemetry installer.
#
# Usage:
#   curl -fsSL https://raw.githubusercontent.com/dash0hq/dash0-agent-plugin/main/install-amp.sh | bash
#
# With CLI flags (pass after `bash -s --` when piping from curl):
#   curl -fsSL .../install-amp.sh | bash -s -- \
#     --endpoint https://ingress.<region>.aws.dash0.com \
#     --token <auth-token> \
#     --dataset <dataset>
#
# All flags are optional. Any flag not provided is prompted for interactively,
# or (non-interactively) left blank — the plugin then installs but stays inactive
# until ~/.amp/dash0-agent-plugin.local.md is filled in.
#
# Flags:
#   --endpoint URL   Dash0 OTLP endpoint URL
#   --token TOKEN    Dash0 auth token
#   --dataset NAME   Dash0 dataset (defaults to "default")
#   --team NAME      Team name
#   --project        Install into ./.amp/plugins/dash0 instead of the user directory
#
# Env vars: DASH0_OTLP_URL, DASH0_AUTH_TOKEN, DASH0_DATASET, DASH0_TEAM_NAME,
#           DASH0_VERSION (pins a specific release),
#           DASH0_SOURCE_DIR (install from a local checkout instead of a release;
#           see below).
#
# What this installs:
#   ~/.config/amp/plugins/dash0/index.ts        The plugin Amp loads (Bun runs it).
#   ~/.config/amp/plugins/dash0/amp-on-event    The helper index.ts spawns. Amp's
#       bridge resolves it by that exact name next to index.ts, so the release
#       asset amp-on-event-<os>-<arch> is installed under the unsuffixed name.
#   ~/.amp/dash0-agent-plugin.local.md
#       YAML-frontmatter config carrying your OTLP URL + auth token (chmod 600).
#
# Amp loads a directory plugin: there is no shell hook and no settings file to
# register, so unlike install-codex.sh / install-cursor.sh this writes nothing
# outside the plugin directory and the config file. There is no connectivity
# check either — amp-on-event only accepts a completed turn, not a probe event.
#
# DASH0_SOURCE_DIR=<dir> installs index.ts and the helper from <dir> instead of
# downloading them, for developing this plugin or installing without network:
#   go build -o amp/amp-on-event ./cmd/amp-on-event
#   DASH0_SOURCE_DIR=amp ./install-amp.sh

set -u

REPO="dash0hq/dash0-agent-plugin"

DASH0_OTLP_URL="${DASH0_OTLP_URL:-}"
DASH0_AUTH_TOKEN="${DASH0_AUTH_TOKEN:-}"
DASH0_DATASET="${DASH0_DATASET:-}"
DASH0_TEAM_NAME="${DASH0_TEAM_NAME:-}"
PROJECT_SCOPE=0

while [ $# -gt 0 ]; do
  case "$1" in
    --endpoint) [ $# -ge 2 ] || { printf "✗ --endpoint requires a value\n" >&2; exit 1; }; DASH0_OTLP_URL="$2"; shift 2 ;;
    --token)    [ $# -ge 2 ] || { printf "✗ --token requires a value\n" >&2; exit 1; }; DASH0_AUTH_TOKEN="$2"; shift 2 ;;
    --dataset)  [ $# -ge 2 ] || { printf "✗ --dataset requires a value\n" >&2; exit 1; }; DASH0_DATASET="$2"; shift 2 ;;
    --team)     [ $# -ge 2 ] || { printf "✗ --team requires a value\n" >&2; exit 1; }; DASH0_TEAM_NAME="$2"; shift 2 ;;
    --project)  PROJECT_SCOPE=1; shift ;;
    -h|--help)
      cat <<'EOF'
Usage: install-amp.sh [--endpoint URL] [--token TOKEN] [--dataset NAME] [--team NAME] [--project]

All flags optional; missing ones are prompted for (or left blank non-interactively).

Flags:
  --endpoint URL   Dash0 OTLP endpoint URL
  --token TOKEN    Dash0 auth token
  --dataset NAME   Dash0 dataset (defaults to "default")
  --team NAME      Team name
  --project        Install into ./.amp/plugins/dash0 (this workspace only)

Env vars: DASH0_OTLP_URL, DASH0_AUTH_TOKEN, DASH0_DATASET, DASH0_TEAM_NAME,
          DASH0_VERSION (pins a specific release),
          DASH0_SOURCE_DIR (install index.ts + amp-on-event from a local
          checkout instead of a release).
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

printf '%sDash0 → Amp telemetry installer%s\n\n' "$C_B" "$C_N"

SOURCE_DIR="${DASH0_SOURCE_DIR:-}"

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
  *) die "unsupported OS: $OS (need darwin or linux; on Windows use install-amp.ps1)" ;;
esac
ok "detected $OS/$ARCH"

# 2. Fetch/checksum helpers. Not needed for a local install, so a source install
#    works on a host with neither curl nor wget.
if [ -z "$SOURCE_DIR" ]; then
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
    # Fail closed on integrity: without a hash tool the download cannot be
    # verified, and an unverified binary is not installed. Every supported
    # platform ships one of these, so this is a stop rather than a fallback.
    die "sha256sum or shasum is required to verify the download"
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
fi

# 4. Paths. Amp reads plugins from ~/.config/amp/plugins/ on every platform, and
#    from <workspace>/.amp/plugins/ for a single project.
if [ "$PROJECT_SCOPE" -eq 1 ]; then
  PLUGIN_DIR="$PWD/.amp/plugins/dash0"
else
  PLUGIN_DIR="$HOME/.config/amp/plugins/dash0"
fi
BIN_PATH="$PLUGIN_DIR/amp-on-event"
INDEX_PATH="$PLUGIN_DIR/index.ts"
CONFIG_PATH="$HOME/.amp/dash0-agent-plugin.local.md"

mkdir -p "$PLUGIN_DIR" "$HOME/.amp" || die "could not create install directories"

# 5. Install the helper and index.ts. Both are replaced on every run, so
#    re-running really upgrades.
if [ -n "$SOURCE_DIR" ]; then
  [ -f "$SOURCE_DIR/index.ts" ] || die "no index.ts in $SOURCE_DIR"
  [ -f "$SOURCE_DIR/amp-on-event" ] \
    || die "no amp-on-event in $SOURCE_DIR (build it: go build -o $SOURCE_DIR/amp-on-event ./cmd/amp-on-event)"
  cp "$SOURCE_DIR/index.ts" "$INDEX_PATH" || die "could not copy index.ts"
  cp "$SOURCE_DIR/amp-on-event" "$BIN_PATH" || die "could not copy amp-on-event"
  chmod +x "$BIN_PATH"
  ok "installed from $SOURCE_DIR → $PLUGIN_DIR"
else
  BASE_URL="https://github.com/${REPO}/releases/download/v${VERSION}"
  RAW_BASE="https://raw.githubusercontent.com/${REPO}/v${VERSION}"
  # Published per platform, installed under the plain name: amp/index.ts spawns
  # exactly ./amp-on-event next to itself.
  BIN_ASSET="amp-on-event-${OS}-${ARCH}"

  info "downloading amp-on-event v${VERSION}..."
  # Staged under a temp name and renamed: curl and wget both create the
  # destination before they learn the request failed, so writing $BIN_PATH
  # directly would truncate a helper that works.
  BIN_TMP="$BIN_PATH.tmp.$$"
  fetch "$BASE_URL/$BIN_ASSET" "$BIN_TMP" \
    || { rm -f "$BIN_TMP"; die "failed to download binary: $BASE_URL/$BIN_ASSET"; }
  CHECKSUMS=$(fetch_stdout "$BASE_URL/checksums.txt") \
    || { rm -f "$BIN_TMP"; die "failed to download $BASE_URL/checksums.txt"; }

  # Fail closed on integrity, matching the other installers: a binary that
  # cannot be verified is deleted rather than installed. A missing entry means
  # the release is malformed, which is not a reason to trust the download.
  EXPECTED=$(echo "$CHECKSUMS" | grep "  ${BIN_ASSET}\$" | cut -d' ' -f1)
  if [ -z "$EXPECTED" ]; then
    rm -f "$BIN_TMP"
    die "no checksum for $BIN_ASSET in v${VERSION} — refusing to install an unverified binary"
  fi
  ACTUAL=$(sha256 "$BIN_TMP")
  if [ "$ACTUAL" != "$EXPECTED" ]; then
    rm -f "$BIN_TMP"
    die "checksum mismatch for $BIN_ASSET (expected $EXPECTED, got $ACTUAL)"
  fi
  chmod +x "$BIN_TMP"
  mv -f "$BIN_TMP" "$BIN_PATH" || { rm -f "$BIN_TMP"; die "could not move $BIN_TMP into place"; }
  ok "installed helper → $BIN_PATH"

  # index.ts comes from the tagged ref rather than the release: it is source, not
  # a build artifact, and the tag carries the exact copy the helper was built
  # with. Same mechanism install-cursor.sh uses for its plugin files.
  info "downloading index.ts..."
  INDEX_TMP="$INDEX_PATH.tmp.$$"
  fetch "$RAW_BASE/amp/index.ts" "$INDEX_TMP" \
    || { rm -f "$INDEX_TMP"; die "failed to download: $RAW_BASE/amp/index.ts"; }
  mv -f "$INDEX_TMP" "$INDEX_PATH" || { rm -f "$INDEX_TMP"; die "could not move $INDEX_TMP into place"; }
  ok "installed plugin → $INDEX_PATH"
fi

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

DASH0_AGENT_NAME="amp"
prompt_value  DASH0_OTLP_URL    "Dash0 OTLP endpoint URL (e.g. https://ingress.<region>.aws.dash0.com)"
prompt_secret DASH0_AUTH_TOKEN  "Dash0 auth token"
prompt_value  DASH0_DATASET     "Dash0 dataset (optional)" "default"
prompt_value  DASH0_TEAM_NAME   "Team name (optional)"

if [ -z "$DASH0_OTLP_URL" ] || [ -z "$DASH0_AUTH_TOKEN" ]; then
  warn "OTLP URL or auth token not provided. The plugin will install but stay inactive."
  warn "Re-run with DASH0_OTLP_URL and DASH0_AUTH_TOKEN set, or edit $CONFIG_PATH later."
fi

# 7. Write the config file (chmod 600 — holds the token in cleartext).
#    Always user scope, even with --project: a workspace-level
#    .amp/dash0-agent-plugin.local.md is a token inside a Git working tree, and
#    that is a decision for whoever owns the repository, not for this installer.
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

# 8. Done.
printf '\n%sNext steps%s\n' "$C_B" "$C_N"
printf "  1. Reload plugins from Amp's command palette (or start a new Amp session).\n"
printf "  2. Run a prompt. Spans should land in your Dash0 dataset with gen_ai.harness.name=amp.\n"
printf "\nExecute mode must wait for plugins to load: amp --plugin-ready-timeout -x 'Your task'\n"
printf "An Orb needs its own installation — run this from the project's setup script; a local install is not copied.\n"
printf "To reconfigure later, edit %s (a workspace .amp/dash0-agent-plugin.local.md outranks it; keep it out of Git).\n" "$CONFIG_PATH"
printf "To uninstall: curl -fsSL https://raw.githubusercontent.com/%s/main/uninstall-amp.sh | bash\n" "$REPO"
