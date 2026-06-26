#!/usr/bin/env bash
# Dash0 — Cursor telemetry uninstaller.
#
# Usage:
#   ./uninstall-cursor.sh                       # prompts before deleting
#   ./uninstall-cursor.sh --yes                 # skips confirmation
#   curl -fsSL .../uninstall-cursor.sh | bash -s -- --yes
#
# What this removes (mirror of install-cursor.sh):
#   ~/.local/state/dash0-agent-plugin/cursor/    cursor-on-event binaries
#   ~/.local/share/dash0-agent-plugin/cursor-on-event.sh
#                                                bootstrap script
#   ~/.cursor/dash0-agent-plugin.local.md        credential config
#   ~/.cursor/skills-cursor/dash0-configure/     shipped skill
#   ~/.cursor/hooks.json                         only when every entry points at
#                                                the Dash0 bootstrap script;
#                                                otherwise the file is left in
#                                                place with a warning.

set -u

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

# ---------------------------------------------------------------------------
# Parse CLI flags.
# ---------------------------------------------------------------------------

ASSUME_YES=0
while [ $# -gt 0 ]; do
  case "$1" in
    -y|--yes) ASSUME_YES=1; shift ;;
    -h|--help)
      cat <<'EOF'
Usage: uninstall-cursor.sh [--yes]

Removes Dash0 Cursor plugin files previously installed by install-cursor.sh.

Flags:
  -y, --yes   Skip the confirmation prompt.
  -h, --help  Show this help.
EOF
      exit 0 ;;
    *)
      printf "✗ unknown argument: %s (try --help)\n" "$1" >&2
      exit 1 ;;
  esac
done

# ---------------------------------------------------------------------------
# Resolve paths (must mirror install-cursor.sh).
# ---------------------------------------------------------------------------

STATE_DIR="${XDG_STATE_HOME:-$HOME/.local/state}/dash0-agent-plugin/cursor"
SHARE_DIR="$HOME/.local/share/dash0-agent-plugin"
SHARE_FILE="$SHARE_DIR/cursor-on-event.sh"
CONFIG_PATH="$HOME/.cursor/dash0-agent-plugin.local.md"
HOOKS_PATH="$HOME/.cursor/hooks.json"
SKILLS_PARENT="$HOME/.cursor/skills-cursor"
SKILL_DIR="$SKILLS_PARENT/dash0-configure"

printf "${C_B}Dash0 → Cursor telemetry uninstaller${C_N}\n\n"
printf "Will remove:\n"
printf "  %s\n" \
  "$STATE_DIR" \
  "$SHARE_FILE" \
  "$CONFIG_PATH" \
  "$SKILL_DIR"
printf "  %s (only when it contains exclusively Dash0 hooks)\n" "$HOOKS_PATH"
printf "\n"

# ---------------------------------------------------------------------------
# Confirm.
# ---------------------------------------------------------------------------

if [ "$ASSUME_YES" -ne 1 ]; then
  if [ -r /dev/tty ]; then
    printf "Proceed? [y/N] " > /dev/tty
    IFS= read -r reply < /dev/tty || reply=""
    case "$reply" in
      y|Y|yes|YES) : ;;
      *) info "aborted"; exit 0 ;;
    esac
  else
    die "no TTY available for confirmation; pass --yes to proceed non-interactively"
  fi
fi

# ---------------------------------------------------------------------------
# Remove files & directories.
# ---------------------------------------------------------------------------

remove_path() {
  local p="$1" label="$2"
  if [ -e "$p" ] || [ -L "$p" ]; then
    rm -rf "$p" && ok "removed ${label} → ${p}"
  else
    info "skip ${label} (not present): ${p}"
  fi
}

remove_path "$STATE_DIR"   "binary dir"
remove_path "$SHARE_FILE"  "bootstrap script"
remove_path "$CONFIG_PATH" "config file"
remove_path "$SKILL_DIR"   "configure skill"

# Tidy empty parent directories (silent if they aren't empty or don't exist).
rmdir "$SHARE_DIR"     2>/dev/null && ok "removed empty $SHARE_DIR"     || true
rmdir "$SKILLS_PARENT" 2>/dev/null && ok "removed empty $SKILLS_PARENT" || true

# ---------------------------------------------------------------------------
# Handle hooks.json carefully — it may contain user-authored hooks.
# Strategy:
#   - jq available: delete the file only if every hook command references
#     the Dash0 bootstrap script. Otherwise warn and leave it alone.
#   - jq missing: fall back to a grep heuristic; if it suggests mixed
#     content, warn and leave the file alone.
# ---------------------------------------------------------------------------

BOOTSTRAP_BASENAME="cursor-on-event.sh"

if [ -e "$HOOKS_PATH" ]; then
  if command -v jq >/dev/null 2>&1; then
    foreign=$(jq -r '
      .hooks // {} | to_entries[] | .value[]? | .command // empty
    ' "$HOOKS_PATH" 2>/dev/null | grep -v "$BOOTSTRAP_BASENAME" || true)
    if [ -z "$foreign" ]; then
      rm -f "$HOOKS_PATH" && ok "removed hooks → $HOOKS_PATH"
    else
      warn "$HOOKS_PATH contains non-Dash0 hooks; leaving the file in place."
      warn "Remove the entries whose 'command' contains '$BOOTSTRAP_BASENAME' by hand."
    fi
  else
    total=$(grep -c '"command"' "$HOOKS_PATH" 2>/dev/null || echo 0)
    ours=$(grep -c "$BOOTSTRAP_BASENAME" "$HOOKS_PATH" 2>/dev/null || echo 0)
    if [ "$total" -gt 0 ] && [ "$total" -eq "$ours" ]; then
      rm -f "$HOOKS_PATH" && ok "removed hooks → $HOOKS_PATH"
    else
      warn "Cannot safely inspect $HOOKS_PATH (jq not installed); leaving it in place."
      warn "Inspect and remove entries that reference '$BOOTSTRAP_BASENAME' by hand."
    fi
  fi
else
  info "skip hooks (not present): $HOOKS_PATH"
fi

# ---------------------------------------------------------------------------
# Done.
# ---------------------------------------------------------------------------

printf "\n${C_B}Done.${C_N} Restart Cursor (Cmd+Q on macOS) so it stops registering the hooks.\n"
