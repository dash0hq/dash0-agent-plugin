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
# What this installs:
#   ~/.local/state/dash0-agent-plugin/cursor/bin/cursor-on-event-<v>-<os>-<arch>
#       The binary that turns Cursor hook events into OTLP spans.
#   ~/.local/share/dash0-agent-plugin/cursor-on-event.sh
#       The bootstrap script Cursor's hooks.json invokes.
#   ~/.cursor/dash0-agent-plugin.local.md
#       YAML-frontmatter config carrying your OTLP URL + auth token.
#   ~/.cursor/hooks.json
#       Cursor hook registrations (only added when no file exists yet).
#   ~/.cursor/skills-cursor/<skill>/SKILL.md
#       Skills the plugin ships (e.g. dash0-configure).

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

SHARE_DIR="$HOME/.local/share/dash0-agent-plugin"
SCRIPT_PATH="$SHARE_DIR/cursor-on-event.sh"

CONFIG_PATH="$HOME/.cursor/dash0-agent-plugin.local.md"
HOOKS_PATH="$HOME/.cursor/hooks.json"
SKILLS_DIR="$HOME/.cursor/skills-cursor"

mkdir -p "$BIN_DIR" "$SHARE_DIR" "$HOME/.cursor" "$SKILLS_DIR" \
  || die "could not create install directories"

# ---------------------------------------------------------------------------
# 5. Download the binary and bootstrap script with checksum verification.
# ---------------------------------------------------------------------------

BASE_URL="https://github.com/${REPO}/releases/download/v${VERSION}"
BIN_ASSET="cursor-on-event-${OS}-${ARCH}"
SCRIPT_URL="https://raw.githubusercontent.com/${REPO}/v${VERSION}/scripts/cursor-on-event.sh"

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

info "downloading bootstrap script..."
fetch "$SCRIPT_URL" "$SCRIPT_PATH" \
  || die "failed to download bootstrap script: $SCRIPT_URL"
chmod +x "$SCRIPT_PATH"
ok "installed bootstrap script → $SCRIPT_PATH"

# ---------------------------------------------------------------------------
# 5b. Install skills.
#     Skills live in the plugin repo under cursor/skills/<name>/SKILL.md.
#     Cursor picks them up from ~/.cursor/skills-cursor/<name>/SKILL.md.
# ---------------------------------------------------------------------------

SKILLS="dash0-configure"
for skill in $SKILLS; do
  skill_url="https://raw.githubusercontent.com/${REPO}/v${VERSION}/cursor/skills/${skill}/SKILL.md"
  skill_dest_dir="$SKILLS_DIR/$skill"
  skill_dest="$skill_dest_dir/SKILL.md"
  mkdir -p "$skill_dest_dir" || die "could not create $skill_dest_dir"
  info "downloading skill: $skill..."
  fetch "$skill_url" "$skill_dest" \
    || die "failed to download skill: $skill_url"
  ok "installed skill → $skill_dest"
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
# 8. Write or warn-about ~/.cursor/hooks.json.
#    v1: only write when no file exists, to avoid clobbering user hooks.
# ---------------------------------------------------------------------------

if [ -e "$HOOKS_PATH" ]; then
  warn "$HOOKS_PATH already exists. Skipping to avoid clobbering."
  warn "Merge the following entries by hand (each event triggers cursor-on-event.sh):"
  warn "  sessionStart, sessionEnd, beforeSubmitPrompt, afterAgentResponse,"
  warn "  preToolUse, postToolUse, postToolUseFailure, subagentStart, subagentStop"
  warn "  command: \"$SCRIPT_PATH\""
else
  cat > "$HOOKS_PATH" <<EOF
{
  "version": 1,
  "hooks": {
    "sessionStart":        [{"command": "$SCRIPT_PATH"}],
    "sessionEnd":          [{"command": "$SCRIPT_PATH"}],
    "beforeSubmitPrompt":  [{"command": "$SCRIPT_PATH"}],
    "afterAgentResponse":  [{"command": "$SCRIPT_PATH"}],
    "preToolUse":          [{"command": "$SCRIPT_PATH"}],
    "postToolUse":         [{"command": "$SCRIPT_PATH"}],
    "postToolUseFailure":  [{"command": "$SCRIPT_PATH"}],
    "subagentStart":       [{"command": "$SCRIPT_PATH"}],
    "subagentStop":        [{"command": "$SCRIPT_PATH"}]
  }
}
EOF
  ok "wrote hooks → $HOOKS_PATH"
fi

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
printf "  1. Quit Cursor (Cmd+Q on macOS) and relaunch — Cursor reads hooks.json on startup.\n"
printf "  2. Open this repo in Cursor; run a prompt. Spans should land in your Dash0 dataset.\n"
printf "\nTo reconfigure later, edit %s and restart Cursor.\n" "$CONFIG_PATH"
printf "To uninstall: rm -rf %s %s %s %s %s\n" \
  "$STATE_BASE" "$SHARE_DIR" "$CONFIG_PATH" "$HOOKS_PATH" "$SKILLS_DIR/dash0-configure"
