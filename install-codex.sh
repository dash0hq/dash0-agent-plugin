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
# Or, non-interactively (e.g. provisioning):
#   DASH0_OTLP_URL=... DASH0_AUTH_TOKEN=... \
#     curl -fsSL .../install-codex.sh | bash
#
# All flags are optional. Any flag not provided is prompted for interactively,
# or (in a non-interactive run) left blank. Without --endpoint and --token the
# plugin installs but stays inactive until the config file is filled in.
#
# Flags (each provided flag skips the corresponding prompt; the value is
# written to the config file):
#   --endpoint URL   Dash0 OTLP endpoint URL
#   --token TOKEN    Dash0 auth token
#   --dataset NAME   Dash0 dataset (defaults to "default")
#   --team NAME      Team name
#
# Optional env vars: DASH0_DATASET, DASH0_TEAM_NAME.
#   DASH0_VERSION pins a specific release (e.g. "0.1.9"); without it, the
#   installer resolves the latest GitHub release at runtime.
#   DASH0_SOURCE_DIR installs the plugin files from a local checkout instead of
#   the tagged release ref (development and test use).
#   DASH0_SKIP_PLUGIN_FILES=1 leaves the plugin files on disk alone, for testing
#   a locally staged build.
#
# What this installs:
#   ~/.local/state/dash0-agent-plugin/codex/codex-on-event.sh
#       Bootstrap Codex invokes on each hook event.
#   ~/.local/state/dash0-agent-plugin/codex/bin/codex-on-event-<v>-<os>-<arch>
#       The binary the bootstrap execs. Pre-downloaded so the connectivity
#       check below can run before Codex restarts.
#   ~/.codex/dash0-agent-plugin.local.md
#       YAML-frontmatter config carrying your OTLP URL + auth token.
#   ~/.codex/config.toml
#       Codex reads hooks from here (there is no hooks.json). This installer
#       APPENDS a marker-delimited managed block registering the plugin's hooks
#       AND pre-trusting them (Codex requires a persisted trusted_hash or it
#       prompts via /hooks). Any hooks you authored yourself are preserved; the
#       managed block is replaced on re-install and removed by uninstall-codex.sh.

set -u

REPO="dash0hq/dash0-agent-plugin"

# ---------------------------------------------------------------------------
# Parse CLI flags. Values land in the same vars the prompt step reads, so a
# provided flag naturally skips its prompt.
# ---------------------------------------------------------------------------

DASH0_OTLP_URL="${DASH0_OTLP_URL:-}"
DASH0_AUTH_TOKEN="${DASH0_AUTH_TOKEN:-}"
DASH0_DATASET="${DASH0_DATASET:-}"
DASH0_TEAM_NAME="${DASH0_TEAM_NAME:-}"

while [ $# -gt 0 ]; do
  case "$1" in
    --endpoint)
      [ $# -ge 2 ] || { printf "✗ --endpoint requires a value\n" >&2; exit 1; }
      DASH0_OTLP_URL="$2"; shift 2 ;;
    --token)
      [ $# -ge 2 ] || { printf "✗ --token requires a value\n" >&2; exit 1; }
      DASH0_AUTH_TOKEN="$2"; shift 2 ;;
    --dataset)
      [ $# -ge 2 ] || { printf "✗ --dataset requires a value\n" >&2; exit 1; }
      DASH0_DATASET="$2"; shift 2 ;;
    --team)
      [ $# -ge 2 ] || { printf "✗ --team requires a value\n" >&2; exit 1; }
      DASH0_TEAM_NAME="$2"; shift 2 ;;
    -h|--help)
      cat <<'EOF'
Usage: install-codex.sh [--endpoint URL] [--token TOKEN] [--dataset NAME] [--team NAME]

All flags are optional. Any flag not provided is prompted for interactively,
or (in a non-interactive run) left blank. Without --endpoint and --token the
plugin installs but stays inactive.

Flags (each provided flag skips the corresponding prompt):
  --endpoint URL   Dash0 OTLP endpoint URL
  --token TOKEN    Dash0 auth token
  --dataset NAME   Dash0 dataset (defaults to "default")
  --team NAME      Team name

Env vars: DASH0_OTLP_URL, DASH0_AUTH_TOKEN, DASH0_DATASET, DASH0_TEAM_NAME,
          DASH0_VERSION (pins a specific release),
          DASH0_SOURCE_DIR (install plugin files from a local checkout),
          DASH0_SKIP_PLUGIN_FILES (leave the plugin files on disk alone).
EOF
      exit 0 ;;
    *)
      printf "✗ unknown argument: %s (try --help)\n" "$1" >&2
      exit 1 ;;
  esac
done

# Color helpers (skip if stdout isn't a TTY).
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

# ---------------------------------------------------------------------------
# 1. Platform detection.
# ---------------------------------------------------------------------------

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

# ---------------------------------------------------------------------------
# 2. Set up fetch/checksum helpers.
# ---------------------------------------------------------------------------

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
  # verified, and an unverified binary is not installed. Every supported platform
  # ships one of these, so this is a stop rather than a fallback.
  die "sha256sum or shasum is required to verify the download"
fi

# ---------------------------------------------------------------------------
# 3. Resolve VERSION.
#    DASH0_VERSION env var pins a specific release; otherwise query the
#    GitHub API for the latest published tag.
# ---------------------------------------------------------------------------

VERSION="${DASH0_VERSION:-}"
if [ -z "$VERSION" ]; then
  info "resolving latest release..."
  LATEST_JSON=$(fetch_stdout "https://api.github.com/repos/${REPO}/releases/latest" || true)
  VERSION=$(echo "$LATEST_JSON" | grep -m1 '"tag_name"' | cut -d'"' -f4 | sed 's/^v//')
  if [ -z "$VERSION" ]; then
    die "could not resolve latest release; set DASH0_VERSION to pin a specific version"
  fi
fi
ok "using v${VERSION}"

# DASH0_SOURCE_DIR installs the plugin files from a local checkout instead of the
# tagged release ref. With a pre-staged binary (see step 5) that makes the whole
# install offline, and it is what lets the install contract test THIS checkout's
# bootstrap rather than the last release's.
SOURCE_DIR="${DASH0_SOURCE_DIR:-}"
if [ -n "$SOURCE_DIR" ]; then
  [ -d "$SOURCE_DIR" ] || die "DASH0_SOURCE_DIR is not a directory: $SOURCE_DIR"
  info "installing plugin files from $SOURCE_DIR"
fi

# DASH0_SKIP_PLUGIN_FILES=1 leaves every plugin file exactly as it is on disk, so
# a test can stage one from its working tree. Nothing else should set it: a
# failed download stays fatal, because an install that quietly kept the old files
# would report success while the previous release kept running.
KEEP_PLUGIN_FILES="${DASH0_SKIP_PLUGIN_FILES:-}"

# ---------------------------------------------------------------------------
# 4. Resolve install paths.
# ---------------------------------------------------------------------------

STATE_BASE="${XDG_STATE_HOME:-$HOME/.local/state}/dash0-agent-plugin/codex"
BIN_DIR="$STATE_BASE/bin"
BIN_PATH="$BIN_DIR/codex-on-event-${VERSION}-${OS}-${ARCH}"
SCRIPT_PATH="$STATE_BASE/codex-on-event.sh"

CONFIG_PATH="$HOME/.codex/dash0-agent-plugin.local.md"
CONFIG_TOML="$HOME/.codex/config.toml"

mkdir -p "$BIN_DIR" "$HOME/.codex" \
  || die "could not create install directories"

# ---------------------------------------------------------------------------
# 5. Download the binary with checksum verification.
#    Pre-downloading lets the connectivity check below succeed before Codex
#    is restarted. The bootstrap script would otherwise download it on first
#    hook fire.
# ---------------------------------------------------------------------------

BASE_URL="https://github.com/${REPO}/releases/download/v${VERSION}"
BIN_ASSET="codex-on-event-${OS}-${ARCH}"
RAW_BASE="https://raw.githubusercontent.com/${REPO}/v${VERSION}"

# An already-present binary is left alone, which makes a re-install idempotent and
# an offline or pre-staged install possible. A version bump changes BIN_PATH,
# forcing a fetch.
#
# Note what this costs: the path is version-pinned, so presence proves the NAME is
# this version and says nothing about the bytes. A truncated download or a
# tampered file is adopted without being hashed, and repairing exactly that is one
# reason a user re-runs an installer. Deleting the file first is the repair, and
# no README says so. Changing it here would need a signal for "the binary is pre-staged" that
# is narrower than DASH0_SKIP_PLUGIN_FILES, which also holds back the plugin files.
if [ -x "$BIN_PATH" ]; then
  ok "binary already present → $BIN_PATH"
else
  info "downloading codex-on-event v${VERSION}..."
  fetch "$BASE_URL/$BIN_ASSET" "$BIN_PATH" \
    || die "failed to download binary: $BASE_URL/$BIN_ASSET"

  CHECKSUMS=$(fetch_stdout "$BASE_URL/checksums.txt") \
    || die "failed to download $BASE_URL/checksums.txt"

  # Fail closed on integrity, matching the bootstraps: a binary that cannot be
  # verified is deleted rather than installed. A missing entry means the release
  # is malformed, which is not a reason to trust the download.
  EXPECTED=$(echo "$CHECKSUMS" | grep "  ${BIN_ASSET}\$" | cut -d' ' -f1)
  if [ -z "$EXPECTED" ]; then
    rm -f "$BIN_PATH"
    die "no checksum for $BIN_ASSET in v${VERSION} — refusing to install an unverified binary"
  fi
  ACTUAL=$(sha256 "$BIN_PATH")
  if [ "$ACTUAL" != "$EXPECTED" ]; then
    rm -f "$BIN_PATH"
    die "checksum mismatch for $BIN_ASSET (expected $EXPECTED, got $ACTUAL)"
  fi
  chmod +x "$BIN_PATH"
  ok "installed binary → $BIN_PATH"
fi

# ---------------------------------------------------------------------------
# 5b. Install plugin files.
#     Codex loads no plugin directory, so the bootstrap is the only file here.
#     It is always written, never reused: SCRIPT_PATH carries no version, unlike
#     BIN_PATH, so an older script cannot be told apart from a current one by its
#     path, and a bootstrap resolves the binary it execs from the VERSION it
#     declares. Reusing one would pin the plugin to that release while this run
#     reported success.
# ---------------------------------------------------------------------------

install_plugin_file() {
  # install_plugin_file <repo-relative-source> <local-dest> [--executable] [legacy-source]
  # A legacy-source is tried when the primary path 404s, so pinning an older
  # release (DASH0_VERSION) still resolves files that have since moved.
  local src="$1" dest="$2" flag="${3:-}" legacy="${4:-}"
  if [ "$KEEP_PLUGIN_FILES" = "1" ]; then
    [ -f "$dest" ] || die "DASH0_SKIP_PLUGIN_FILES is set but $dest is not there"
    if [ "$flag" = "--executable" ]; then
      chmod +x "$dest"
    fi
    ok "kept staged → $dest"
    return
  fi
  if [ -n "$SOURCE_DIR" ]; then
    info "copying ${src} from ${SOURCE_DIR}..."
    if [ -f "$SOURCE_DIR/$src" ]; then
      cp "$SOURCE_DIR/$src" "$dest" || die "failed to copy: $SOURCE_DIR/$src"
    elif [ -n "$legacy" ] && [ -f "$SOURCE_DIR/$legacy" ]; then
      cp "$SOURCE_DIR/$legacy" "$dest" || die "failed to copy: $SOURCE_DIR/$legacy"
      info "fell back to legacy path ${legacy}"
    else
      die "not found in $SOURCE_DIR: $src"
    fi
  else
    # Staged under a private temp name and renamed into place: curl and wget both
    # create the destination before they learn the request failed, so writing
    # $dest directly would truncate a file that works.
    local tmp="$dest.tmp.$$"
    info "downloading ${src}..."
    if ! fetch "$RAW_BASE/$src" "$tmp"; then
      if [ -n "$legacy" ] && fetch "$RAW_BASE/$legacy" "$tmp"; then
        info "fell back to legacy path ${legacy}"
      else
        rm -f "$tmp"
        die "failed to download: $RAW_BASE/$src"
      fi
    fi
    mv -f "$tmp" "$dest" || { rm -f "$tmp"; die "could not move $tmp into place"; }
  fi
  if [ "$flag" = "--executable" ]; then
    chmod +x "$dest"
  fi
  ok "installed → $dest"
}

# The bootstrap moved from scripts/ to codex/ after v0.1.24 — see the legacy
# fallback note above. Drop the fourth argument once v0.1.24 is unsupported.
install_plugin_file "codex/codex-on-event.sh"        "$SCRIPT_PATH" --executable "scripts/codex-on-event.sh"

# ---------------------------------------------------------------------------
# 6. Collect configuration.
#    Precedence: env var > interactive prompt > skip (with warning).
# ---------------------------------------------------------------------------

prompt_value() {
  # prompt_value VAR_NAME "Label" "default"
  local var="$1" label="$2" default="${3:-}"
  local val="${!var:-}"
  if [ -z "$val" ]; then
    if [ -r /dev/tty ]; then
      if [ -n "$default" ]; then
        printf "%s [%s]: " "$label" "$default" > /dev/tty
      else
        printf "%s: " "$label" > /dev/tty
      fi
      IFS= read -r val < /dev/tty || val=""
      val="${val:-$default}"
    else
      val="$default"
    fi
  fi
  printf -v "$var" "%s" "$val"
}

prompt_secret() {
  local var="$1" label="$2"
  local val="${!var:-}"
  if [ -z "$val" ]; then
    if [ -r /dev/tty ]; then
      printf "%s (input hidden): " "$label" > /dev/tty
      stty -echo < /dev/tty 2>/dev/null
      IFS= read -r val < /dev/tty || val=""
      stty echo  < /dev/tty 2>/dev/null
      printf "\n" > /dev/tty
    fi
  fi
  printf -v "$var" "%s" "$val"
}

DASH0_AGENT_NAME="codex"

prompt_value  DASH0_OTLP_URL    "Dash0 OTLP endpoint URL (e.g. https://ingress.<region>.aws.dash0.com)"
prompt_secret DASH0_AUTH_TOKEN  "Dash0 auth token"
prompt_value  DASH0_DATASET     "Dash0 dataset (optional)"               "default"
prompt_value  DASH0_TEAM_NAME   "Team name (optional)"

if [ -z "$DASH0_OTLP_URL" ] || [ -z "$DASH0_AUTH_TOKEN" ]; then
  warn "OTLP URL or auth token not provided. The plugin will install but stay inactive."
  warn "Re-run with DASH0_OTLP_URL and DASH0_AUTH_TOKEN set, or edit $CONFIG_PATH later."
fi

# ---------------------------------------------------------------------------
# 7. Write the config file (chmod 600 — it holds the token in cleartext).
# ---------------------------------------------------------------------------

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

# ---------------------------------------------------------------------------
# 8. Merge hooks + pre-trust into ~/.codex/config.toml.
#    Codex reads hooks from config.toml and requires a persisted trusted_hash to
#    run them without a /hooks prompt. The binary emits both the [[hooks.*]]
#    blocks and the matching [hooks.state] trust entries, wrapped in markers.
#
#    Merge strategy: strip any prior managed block (so a re-install is clean and
#    group indices are recomputed against the user's own hooks), then append the
#    fresh block. User-authored hooks outside the markers are never touched.
# ---------------------------------------------------------------------------

info "registering + pre-trusting hooks in ${CONFIG_TOML}..."

HOOK_CMD="bash \"$SCRIPT_PATH\""

if [ -f "$CONFIG_TOML" ]; then
  STRIPPED_TMP=$(mktemp)
  awk '
    index($0, ">>> dash0-agent-plugin (managed)") { skip=1 }
    !skip { print }
    index($0, "<<< dash0-agent-plugin (managed)") { skip=0 }
  ' "$CONFIG_TOML" > "$STRIPPED_TMP" || { rm -f "$STRIPPED_TMP"; die "failed to read $CONFIG_TOML"; }
  mv "$STRIPPED_TMP" "$CONFIG_TOML"
fi

BLOCK=$("$BIN_PATH" emit-codex-hooks --config "$CONFIG_TOML" --command "$HOOK_CMD") \
  || die "failed to render hook config"

# Separate from any preceding content with a blank line, then append.
if [ -s "$CONFIG_TOML" ]; then printf "\n" >> "$CONFIG_TOML"; fi
printf "%s" "$BLOCK" >> "$CONFIG_TOML" || die "failed to write $CONFIG_TOML"
ok "registered + pre-trusted hooks (managed block in $CONFIG_TOML)"

# ---------------------------------------------------------------------------
# 9. Connectivity check.
#
#    No credentials are passed in. The binary resolves otlp_url, auth_token and
#    dataset from the config file written above, exactly as it will on a real hook
#    fire, so this validates that file rather than the values held in this shell.
#    Passing the token as CODEX_PLUGIN_OPTION_AUTH_TOKEN would outrank the file
#    and hide a token the installer wrote but the binary cannot use.
#
#    The check runs in an empty scratch directory, which is also its state root.
#    A project-level config file in the installer's working directory outranks the
#    user-level one, and this check has no business validating some unrelated
#    repository's configuration.
# ---------------------------------------------------------------------------

if [ -n "$DASH0_OTLP_URL" ] && [ -n "$DASH0_AUTH_TOKEN" ]; then
  info "running connectivity check..."
  CHECK_DIR=$(mktemp -d)
  CHECK_OUT=$(
    cd "$CHECK_DIR" \
      && echo '{"hook_event_name":"SessionStart","session_id":"install-check","model":"gpt-5.5","source":"startup"}' \
      | DASH0_PLUGIN_DATA="$CHECK_DIR" "$BIN_PATH" 2>&1 || true
  )
  rm -rf "$CHECK_DIR"
  case "$CHECK_OUT" in
    *"connectivity check failed"*)
      warn "connectivity check failed:"
      printf "    %s\n" "$CHECK_OUT" ;;
    *"connected"*)
      ok "connectivity check passed" ;;
    *)
      warn "connectivity check returned unexpected output:"
      printf "    %s\n" "$CHECK_OUT" ;;
  esac
fi

# ---------------------------------------------------------------------------
# 10. Done.
# ---------------------------------------------------------------------------

printf '\n%sNext steps%s\n' "$C_B" "$C_N"
printf "  1. Start a new Codex session (existing sessions won't pick up the new hooks).\n"
printf "  2. Run a prompt in any repo. Spans should land in your Dash0 dataset with gen_ai.harness.name=codex.\n"
printf "\nHooks are pre-trusted, so Codex should not prompt via /hooks. If it does, run /hooks and trust 'dash0'.\n"
printf "To reconfigure later, edit %s (no restart needed).\n" "$CONFIG_PATH"
printf "To uninstall: curl -fsSL https://raw.githubusercontent.com/%s/main/uninstall-codex.sh | bash\n" "$REPO"
