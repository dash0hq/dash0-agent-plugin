#!/usr/bin/env bash
# SPDX-FileCopyrightText: Copyright 2026 Dash0 Inc.
# SPDX-License-Identifier: Apache-2.0

# Dash0 — Cursor telemetry uninstaller.
#
# Usage:
#   ./uninstall-cursor.sh                       # prompts before deleting
#   ./uninstall-cursor.sh --yes                 # skips confirmation
#   curl -fsSL .../uninstall-cursor.sh | bash -s -- --yes
#
# What this removes:
#   Plugin dir:
#     ~/.cursor/plugins/local/dash0-agent-plugin/  entire plugin dir
#   Global hooks (Cursor's user-scope registrations):
#     ~/.cursor/hooks.json                         Dash0 entries only — any
#                                                  user-authored hooks in the
#                                                  same file are preserved.
#                                                  If the file ends up empty
#                                                  after removal, it's deleted.
#   Pre-0.1.17 shell-installer leftovers:
#     ~/.local/share/dash0-agent-plugin/           legacy bootstrap script dir
#     ~/.cursor/skills-cursor/dash0-configure/     legacy skill location
#   Binary + config (shared across all layouts):
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

Removes Dash0 Cursor plugin files installed by any version of install-cursor.sh.
Non-Dash0 entries in ~/.cursor/hooks.json are preserved; only entries whose
command references cursor-on-event.sh are stripped.

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

# Plugin directory (native local plugin — provides skills + UI surface).
PLUGIN_DIR="$HOME/.cursor/plugins/local/dash0-agent-plugin"

# Global hooks registration — Dash0 entries stripped selectively.
HOOKS_PATH="$HOME/.cursor/hooks.json"

# Pre-0.1.17 shell-installer leftovers.
LEGACY_SHARE_DIR="$HOME/.local/share/dash0-agent-plugin"
LEGACY_SHARE_SCRIPT="$LEGACY_SHARE_DIR/cursor-on-event.sh"
LEGACY_SKILL_DIR="$HOME/.cursor/skills-cursor/dash0-configure"
LEGACY_SKILLS_PARENT="$HOME/.cursor/skills-cursor"

# Binary cache + credential config.
STATE_DIR="${XDG_STATE_HOME:-$HOME/.local/state}/dash0-agent-plugin/cursor"
CONFIG_PATH="$HOME/.cursor/dash0-agent-plugin.local.md"

printf '%sDash0 → Cursor telemetry uninstaller%s\n\n' "$C_B" "$C_N"
printf "Will remove (if present):\n"
printf "  %s\n" \
  "$PLUGIN_DIR" \
  "$LEGACY_SHARE_SCRIPT" \
  "$LEGACY_SKILL_DIR" \
  "$STATE_DIR" \
  "$CONFIG_PATH"
printf "  %s (Dash0 entries only; user hooks preserved)\n" "$HOOKS_PATH"
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
if rmdir "$LEGACY_SHARE_DIR" 2>/dev/null; then ok "removed empty $LEGACY_SHARE_DIR"; fi
if rmdir "$LEGACY_SKILLS_PARENT" 2>/dev/null; then ok "removed empty $LEGACY_SKILLS_PARENT"; fi

# ---------------------------------------------------------------------------
# Strip Dash0 entries from ~/.cursor/hooks.json while preserving any
# user-authored entries. Match by command basename cursor-on-event.sh —
# same filename in both the current $HOME/.cursor/plugins/local/… layout
# and the pre-0.1.17 ~/.local/share/… legacy path.
#
# If the file ends up with no hook entries, delete it entirely. Otherwise
# write back the reduced JSON.
# ---------------------------------------------------------------------------

BOOTSTRAP_BASENAME="cursor-on-event.sh"

if [ -e "$HOOKS_PATH" ]; then
  if ! command -v jq >/dev/null 2>&1; then
    warn "$HOOKS_PATH exists but jq is not installed — cannot safely strip Dash0 entries."
    warn "Install jq and re-run, or remove entries whose 'command' contains '$BOOTSTRAP_BASENAME' by hand."
  else
    REDUCED=$(mktemp)
    jq --arg marker "$BOOTSTRAP_BASENAME" '
      .hooks //= {} |
      .hooks |= (
        map_values(map(select(.command | contains($marker) | not)))
        | with_entries(select(.value | length > 0))
      )
    ' "$HOOKS_PATH" > "$REDUCED" 2>/dev/null

    if [ ! -s "$REDUCED" ]; then
      warn "failed to inspect $HOOKS_PATH (invalid JSON?) — leaving the file in place."
      rm -f "$REDUCED"
    else
      remaining=$(jq '.hooks | length' "$REDUCED" 2>/dev/null || echo 0)
      if [ "$remaining" -eq 0 ]; then
        rm -f "$HOOKS_PATH" "$REDUCED" && ok "removed hooks (no user entries left) → $HOOKS_PATH"
      else
        mv "$REDUCED" "$HOOKS_PATH" && ok "stripped Dash0 entries from $HOOKS_PATH ($remaining event(s) preserved)"
      fi
    fi
  fi
else
  info "skip hooks (not present): $HOOKS_PATH"
fi

# ---------------------------------------------------------------------------
# Done.
# ---------------------------------------------------------------------------

printf '\n%sDone.%s Restart Cursor (Cmd+Q on macOS) so it stops registering the hooks.\n' "$C_B" "$C_N"
