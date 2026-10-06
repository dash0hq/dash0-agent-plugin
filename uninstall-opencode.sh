#!/usr/bin/env bash
# SPDX-FileCopyrightText: Copyright 2026 Dash0 Inc.
# SPDX-License-Identifier: Apache-2.0

# Dash0 — OpenCode telemetry uninstaller.
#
# Usage:
#   ./uninstall-opencode.sh                       # prompts before deleting
#   ./uninstall-opencode.sh --yes                 # skips confirmation
#   curl -fsSL .../uninstall-opencode.sh | bash -s -- --yes
#
# What this removes:
#   ~/.config/opencode/plugin/dash0-opencode-plugin.js   the plugin
#   ~/.config/opencode/plugin/opencode-on-event.sh       the bootstrap
#   ~/.local/state/dash0-agent-plugin/opencode/          binary cache
#
# What this keeps:
#   ~/.config/opencode/opencode.json                     your OpenCode config
#   ~/.config/opencode/dash0-agent-plugin.local.md       your Dash0 settings, so
#       a reinstall does not ask for the endpoint and token again. Delete it by
#       hand to drop the stored token.
#
# Installed from npm instead? Remove "@dash0/opencode-plugin" from the `plugin`
# array in your opencode.json; this script does not edit that file.

set -u

if [ -t 1 ]; then
  C_R=$'\033[31m'; C_G=$'\033[32m'; C_B=$'\033[1m'; C_N=$'\033[0m'
else
  C_R=""; C_G=""; C_B=""; C_N=""
fi
info()  { printf "%s\n" "$1"; }
ok()    { printf "${C_G}✓${C_N} %s\n" "$1"; }
die()   { printf "${C_R}✗${C_N} %s\n" "$1" >&2; exit 1; }

ASSUME_YES=0
while [ $# -gt 0 ]; do
  case "$1" in
    -y|--yes) ASSUME_YES=1; shift ;;
    -h|--help)
      cat <<'EOF'
Usage: uninstall-opencode.sh [--yes]

Removes the Dash0 OpenCode plugin, its bootstrap, and the cached binaries.
Your opencode.json and your dash0-agent-plugin.local.md are left in place.

Flags:
  -y, --yes   Skip the confirmation prompt.
  -h, --help  Show this help.
EOF
      exit 0 ;;
    *) printf "✗ unknown argument: %s (try --help)\n" "$1" >&2; exit 1 ;;
  esac
done

OPENCODE_DIR="$HOME/.config/opencode"
PLUGIN_DIR="$OPENCODE_DIR/plugin"
PLUGIN_PATH="$PLUGIN_DIR/dash0-opencode-plugin.js"
SCRIPT_PATH="$PLUGIN_DIR/opencode-on-event.sh"
STATE_DIR="${XDG_STATE_HOME:-$HOME/.local/state}/dash0-agent-plugin/opencode"
CONFIG_PATH="$OPENCODE_DIR/dash0-agent-plugin.local.md"

printf '%sDash0 → OpenCode telemetry uninstaller%s\n\n' "$C_B" "$C_N"
printf "Will remove (if present):\n"
printf "  %s\n" "$PLUGIN_PATH" "$SCRIPT_PATH" "$STATE_DIR"
printf "Will keep:\n"
printf "  %s\n" "$CONFIG_PATH (your endpoint and token)"
printf "\n"

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

remove_path() {
  local p="$1" label="$2"
  if [ -e "$p" ] || [ -L "$p" ]; then
    rm -rf "$p" && ok "removed ${label} → ${p}"
  else
    info "skip ${label} (not present): ${p}"
  fi
}

remove_path "$PLUGIN_PATH" "plugin"
remove_path "$SCRIPT_PATH" "bootstrap"
remove_path "$STATE_DIR"   "binary cache"

# The plugin directory is OpenCode's, not ours — drop it only if we emptied it.
if rmdir "$PLUGIN_DIR" 2>/dev/null; then ok "removed empty $PLUGIN_DIR"; fi

printf '\n%sDone.%s Start a new OpenCode session so it stops loading the plugin.\n' "$C_B" "$C_N"
if [ -f "$CONFIG_PATH" ]; then
  printf "Your Dash0 settings are still at %s — delete it to drop the stored token.\n" "$CONFIG_PATH"
fi
