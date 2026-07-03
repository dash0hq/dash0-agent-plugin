#!/usr/bin/env bash
# Dash0 — Cursor telemetry installer.
#
# Usage:
#   curl -fsSL https://raw.githubusercontent.com/dash0hq/dash0-agent-plugin/main/install-cursor.sh | bash
#
# With CLI flags (pass after `bash -s --` when piping from curl):
#   curl -fsSL .../install-cursor.sh | bash -s -- \
#     --endpoint https://ingress.<region>.aws.dash0.com \
#     --token <auth-token> \
#     --dataset <dataset>
#
# Or, non-interactively (e.g. provisioning):
#   DASH0_OTLP_URL=... DASH0_AUTH_TOKEN=... \
#     curl -fsSL .../install-cursor.sh | bash
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
#
# What this installs (Cursor native-plugin layout):
#   ~/.cursor/plugins/local/dash0-agent-plugin/
#       .cursor-plugin/plugin.json          Cursor plugin manifest.
#       cursor/plugin-hooks.json            Hook registrations (relative paths).
#       cursor/skills/<skill>/SKILL.md      Skills the plugin ships.
#       scripts/cursor-on-event.sh          Bootstrap script Cursor invokes.
#   ~/.local/state/dash0-agent-plugin/cursor/bin/cursor-on-event-<v>-<os>-<arch>
#       The binary the bootstrap execs. Downloaded here on first hook fire
#       or by this installer (whichever runs first).
#   ~/.cursor/dash0-agent-plugin.local.md
#       YAML-frontmatter config carrying your OTLP URL + auth token.
#
# Cursor scans ~/.cursor/plugins/local/ on startup, so relaunching Cursor is
# required after install to pick up new or updated hook registrations. Under
# this layout Cursor invokes hooks with the plugin dir as CWD — per-project
# config files (.cursor/dash0-agent-plugin.local.md in a repo) are no longer
# picked up; only the global config path above is honored.

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
Usage: install-cursor.sh [--endpoint URL] [--token TOKEN] [--dataset NAME] [--team NAME]

All flags are optional. Any flag not provided is prompted for interactively,
or (in a non-interactive run) left blank. Without --endpoint and --token the
plugin installs but stays inactive.

Flags (each provided flag skips the corresponding prompt):
  --endpoint URL   Dash0 OTLP endpoint URL
  --token TOKEN    Dash0 auth token
  --dataset NAME   Dash0 dataset (defaults to "default")
  --team NAME      Team name

Env vars: DASH0_OTLP_URL, DASH0_AUTH_TOKEN, DASH0_DATASET, DASH0_TEAM_NAME,
          DASH0_VERSION (pins a specific release).
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

printf "${C_B}Dash0 → Cursor telemetry installer${C_N}\n\n"

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
  sha256() { echo ""; }
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

# ---------------------------------------------------------------------------
# 4. Resolve install paths.
# ---------------------------------------------------------------------------

STATE_BASE="${XDG_STATE_HOME:-$HOME/.local/state}/dash0-agent-plugin/cursor"
BIN_DIR="$STATE_BASE/bin"
BIN_PATH="$BIN_DIR/cursor-on-event-${VERSION}-${OS}-${ARCH}"

PLUGIN_DIR="$HOME/.cursor/plugins/local/dash0-agent-plugin"
SCRIPT_PATH="$PLUGIN_DIR/scripts/cursor-on-event.sh"
MANIFEST_PATH="$PLUGIN_DIR/.cursor-plugin/plugin.json"
HOOKS_MANIFEST_PATH="$PLUGIN_DIR/cursor/plugin-hooks.json"
SKILLS_DIR="$PLUGIN_DIR/cursor/skills"

CONFIG_PATH="$HOME/.cursor/dash0-agent-plugin.local.md"
LEGACY_HOOKS_PATH="$HOME/.cursor/hooks.json"

# ---------------------------------------------------------------------------
# 4b. Preflight — refuse to install if an older shell-style install is present.
#     An old ~/.cursor/hooks.json pointing at cursor-on-event.sh would fire
#     alongside the new native-plugin hooks and emit every span twice. Runs
#     before mkdir so a refused install leaves no trace on disk.
# ---------------------------------------------------------------------------

if [ -e "$LEGACY_HOOKS_PATH" ]; then
  legacy_ours=""
  if command -v jq >/dev/null 2>&1; then
    legacy_ours=$(jq -r '
      .hooks // {} | to_entries[] | .value[]? | .command // empty
    ' "$LEGACY_HOOKS_PATH" 2>/dev/null | grep "cursor-on-event.sh" || true)
  else
    grep "cursor-on-event.sh" "$LEGACY_HOOKS_PATH" >/dev/null 2>&1 && legacy_ours="yes"
  fi
  if [ -n "$legacy_ours" ]; then
    die "detected an older shell-style install at $LEGACY_HOOKS_PATH; run uninstall-cursor.sh first, then re-run this installer"
  fi
fi

mkdir -p "$BIN_DIR" "$PLUGIN_DIR/.cursor-plugin" "$PLUGIN_DIR/cursor" "$PLUGIN_DIR/scripts" "$SKILLS_DIR" "$HOME/.cursor" \
  || die "could not create install directories"

# ---------------------------------------------------------------------------
# 5. Download the binary with checksum verification.
#    Pre-downloading lets the connectivity check below succeed before Cursor
#    is relaunched. The bootstrap script would otherwise download it on first
#    hook fire.
# ---------------------------------------------------------------------------

BASE_URL="https://github.com/${REPO}/releases/download/v${VERSION}"
BIN_ASSET="cursor-on-event-${OS}-${ARCH}"
RAW_BASE="https://raw.githubusercontent.com/${REPO}/v${VERSION}"

info "downloading cursor-on-event v${VERSION}..."
fetch "$BASE_URL/$BIN_ASSET" "$BIN_PATH" \
  || die "failed to download binary: $BASE_URL/$BIN_ASSET"

CHECKSUMS=$(fetch_stdout "$BASE_URL/checksums.txt" || true)
if [ -n "$CHECKSUMS" ]; then
  EXPECTED=$(echo "$CHECKSUMS" | grep "  ${BIN_ASSET}\$" | cut -d' ' -f1)
  if [ -n "$EXPECTED" ]; then
    ACTUAL=$(sha256 "$BIN_PATH")
    if [ -n "$ACTUAL" ] && [ "$ACTUAL" != "$EXPECTED" ]; then
      rm -f "$BIN_PATH"
      die "checksum mismatch for $BIN_ASSET (expected $EXPECTED, got $ACTUAL)"
    fi
  fi
fi
chmod +x "$BIN_PATH"
ok "installed binary → $BIN_PATH"

# ---------------------------------------------------------------------------
# 5b. Install plugin files.
#     Fetch each plugin file from the tagged git ref so the on-disk layout
#     is byte-identical to what a native Cursor Marketplace install would
#     produce. Skills live inside the plugin dir at cursor/skills/<name>/.
# ---------------------------------------------------------------------------

install_plugin_file() {
  # install_plugin_file <repo-relative-source> <local-dest> [--executable]
  local src="$1" dest="$2" flag="${3:-}"
  info "downloading ${src}..."
  fetch "$RAW_BASE/$src" "$dest" \
    || die "failed to download: $RAW_BASE/$src"
  if [ "$flag" = "--executable" ]; then
    chmod +x "$dest"
  fi
  ok "installed → $dest"
}

install_plugin_file ".cursor-plugin/plugin.json"     "$MANIFEST_PATH"
install_plugin_file "cursor/plugin-hooks.json"       "$HOOKS_MANIFEST_PATH"
install_plugin_file "scripts/cursor-on-event.sh"     "$SCRIPT_PATH" --executable

SKILLS="dash0-configure"
for skill in $SKILLS; do
  skill_dest_dir="$SKILLS_DIR/$skill"
  mkdir -p "$skill_dest_dir" || die "could not create $skill_dest_dir"
  install_plugin_file "cursor/skills/${skill}/SKILL.md" "$skill_dest_dir/SKILL.md"
done

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

DASH0_AGENT_NAME="cursor"

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
# 8. Hook registrations live inside the plugin dir (cursor/plugin-hooks.json)
#    and are picked up by Cursor's local-plugins scan on startup — no
#    ~/.cursor/hooks.json write needed.
# ---------------------------------------------------------------------------

# ---------------------------------------------------------------------------
# 9. Connectivity check.
#    Pipe a fake sessionStart through the binary. It logs the connectivity
#    result to stderr; we capture and surface it here.
# ---------------------------------------------------------------------------

if [ -n "$DASH0_OTLP_URL" ] && [ -n "$DASH0_AUTH_TOKEN" ]; then
  info "running connectivity check..."
  CHECK_OUT=$(
    echo '{"hook_event_name":"sessionStart","session_id":"install-check","conversation_id":"install-check","model":"default"}' \
      | DASH0_OTLP_URL="$DASH0_OTLP_URL" \
        CURSOR_PLUGIN_OPTION_AUTH_TOKEN="$DASH0_AUTH_TOKEN" \
        DASH0_DATASET="$DASH0_DATASET" \
        DASH0_PLUGIN_DATA="$(mktemp -d)" \
        "$BIN_PATH" 2>&1 || true
  )
  case "$CHECK_OUT" in
    *"connectivity check failed"*)
      warn "connectivity check failed:"
      printf "    %s\n" "$CHECK_OUT"
      ;;
    *"connected"*)
      ok "connectivity check passed"
      ;;
    *)
      warn "connectivity check returned unexpected output:"
      printf "    %s\n" "$CHECK_OUT"
      ;;
  esac
fi

# ---------------------------------------------------------------------------
# 10. Done.
# ---------------------------------------------------------------------------

printf "\n${C_B}Next steps${C_N}\n"
printf "  1. Quit Cursor (Cmd+Q on macOS) and relaunch — Cursor scans %s on startup.\n" "$HOME/.cursor/plugins/local/"
printf "  2. Open any repo in Cursor; run a prompt. Spans should land in your Dash0 dataset.\n"
printf "\nTo reconfigure later, edit %s (no restart needed).\n" "$CONFIG_PATH"
printf "To uninstall: curl -fsSL https://raw.githubusercontent.com/%s/main/uninstall-cursor.sh | bash\n" "$REPO"
