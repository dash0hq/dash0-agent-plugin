#!/usr/bin/env bash
# Dash0 — Cursor telemetry uninstaller.
#
# Usage:
#   ./uninstall-cursor.sh                       # prompts before deleting
#   ./uninstall-cursor.sh --yes                 # skips confirmation
#   curl -fsSL .../uninstall-cursor.sh | bash -s -- --yes
#
# What this removes:
#   Current (native-plugin) layout:
#     ~/.cursor/plugins/local/dash0-agent-plugin/  entire plugin dir
#   Legacy (pre-0.1.17 shell-installer) layout:
#     ~/.local/share/dash0-agent-plugin/           legacy bootstrap script dir
#     ~/.cursor/skills-cursor/dash0-configure/     legacy skill location
#     ~/.cursor/hooks.json                         only when every entry points
#                                                  at the Dash0 bootstrap;
#                                                  otherwise the file is left
#                                                  in place with a warning.
#   Shared (both layouts):
#     ~/.local/state/dash0-agent-plugin/cursor/    binary cache
#     ~/.cursor/dash0-agent-plugin.local.md        credential config

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

Removes Dash0 Cursor plugin files installed by any version of install-cursor.sh
(both the current native-plugin layout and the pre-0.1.17 shell-installer layout).

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
# Resolve paths (must mirror install-cursor.sh, current + legacy).
# ---------------------------------------------------------------------------

# Current native-plugin layout.
PLUGIN_DIR="$HOME/.cursor/plugins/local/dash0-agent-plugin"

# Legacy shell-installer layout.
LEGACY_SHARE_DIR="$HOME/.local/share/dash0-agent-plugin"
LEGACY_SHARE_SCRIPT="$LEGACY_SHARE_DIR/cursor-on-event.sh"
LEGACY_SKILL_DIR="$HOME/.cursor/skills-cursor/dash0-configure"
LEGACY_SKILLS_PARENT="$HOME/.cursor/skills-cursor"
LEGACY_HOOKS_PATH="$HOME/.cursor/hooks.json"

# Shared by both layouts.
STATE_DIR="${XDG_STATE_HOME:-$HOME/.local/state}/dash0-agent-plugin/cursor"
CONFIG_PATH="$HOME/.cursor/dash0-agent-plugin.local.md"

printf "${C_B}Dash0 → Cursor telemetry uninstaller${C_N}\n\n"
printf "Will remove (if present):\n"
printf "  %s\n" \
  "$PLUGIN_DIR" \
  "$LEGACY_SHARE_SCRIPT" \
  "$LEGACY_SKILL_DIR" \
  "$STATE_DIR" \
  "$CONFIG_PATH"
printf "  %s (only when it contains exclusively Dash0 hooks)\n" "$LEGACY_HOOKS_PATH"
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

remove_path "$PLUGIN_DIR"           "plugin dir"
remove_path "$LEGACY_SHARE_SCRIPT"  "legacy bootstrap script"
remove_path "$LEGACY_SKILL_DIR"     "legacy skill dir"
remove_path "$STATE_DIR"            "binary cache"
remove_path "$CONFIG_PATH"          "config file"

# Tidy empty parent directories (silent if they aren't empty or don't exist).
rmdir "$LEGACY_SHARE_DIR"      2>/dev/null && ok "removed empty $LEGACY_SHARE_DIR"      || true
rmdir "$LEGACY_SKILLS_PARENT"  2>/dev/null && ok "removed empty $LEGACY_SKILLS_PARENT"  || true

# ---------------------------------------------------------------------------
# Handle the legacy ~/.cursor/hooks.json carefully — it may contain
# user-authored hooks. Strategy:
#   - jq available: delete the file only if every hook command references
#     the Dash0 bootstrap script. Otherwise warn and leave it alone.
#   - jq missing: fall back to a grep heuristic; if it suggests mixed
#     content, warn and leave the file alone.
# The new native-plugin layout does not touch this file at all — this cleanup
# only matters for machines still carrying a pre-0.1.17 install.
# ---------------------------------------------------------------------------

BOOTSTRAP_BASENAME="cursor-on-event.sh"

if [ -e "$LEGACY_HOOKS_PATH" ]; then
  if command -v jq >/dev/null 2>&1; then
    foreign=$(jq -r '
      .hooks // {} | to_entries[] | .value[]? | .command // empty
    ' "$LEGACY_HOOKS_PATH" 2>/dev/null | grep -v "$BOOTSTRAP_BASENAME" || true)
    if [ -z "$foreign" ]; then
      rm -f "$LEGACY_HOOKS_PATH" && ok "removed legacy hooks → $LEGACY_HOOKS_PATH"
    else
      warn "$LEGACY_HOOKS_PATH contains non-Dash0 hooks; leaving the file in place."
      warn "Remove the entries whose 'command' contains '$BOOTSTRAP_BASENAME' by hand."
    fi
  else
    total=$(grep -c '"command"' "$LEGACY_HOOKS_PATH" 2>/dev/null || echo 0)
    ours=$(grep -c "$BOOTSTRAP_BASENAME" "$LEGACY_HOOKS_PATH" 2>/dev/null || echo 0)
    if [ "$total" -gt 0 ] && [ "$total" -eq "$ours" ]; then
      rm -f "$LEGACY_HOOKS_PATH" && ok "removed legacy hooks → $LEGACY_HOOKS_PATH"
    else
      warn "Cannot safely inspect $LEGACY_HOOKS_PATH (jq not installed); leaving it in place."
      warn "Inspect and remove entries that reference '$BOOTSTRAP_BASENAME' by hand."
    fi
  fi
else
  info "skip legacy hooks (not present): $LEGACY_HOOKS_PATH"
fi

# ---------------------------------------------------------------------------
# Done.
# ---------------------------------------------------------------------------

printf "\n${C_B}Done.${C_N} Restart Cursor (Cmd+Q on macOS) so it stops registering the hooks.\n"
