#!/usr/bin/env bash
# SPDX-FileCopyrightText: Copyright 2026 Dash0 Inc.
# SPDX-License-Identifier: Apache-2.0

# Dash0 — Amp CLI and Orb telemetry uninstaller.
#
# Usage:
#   ./uninstall-amp.sh                        # prompts before deleting
#   ./uninstall-amp.sh --yes                  # skips confirmation
#   ./uninstall-amp.sh --project              # removes ./.amp/plugins/dash0
#   curl -fsSL .../uninstall-amp.sh | bash -s -- --yes
#
# What this removes:
#   ~/.config/amp/plugins/dash0/     the plugin directory (index.ts + helper).
#                                    Only this directory; other Amp plugins stay.
#   ~/.amp/dash0-agent-plugin.local.md        credential config
#
# A workspace .amp/dash0-agent-plugin.local.md is left alone: this installer
# never wrote one.

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
PROJECT_SCOPE=0
while [ $# -gt 0 ]; do
  case "$1" in
    -y|--yes)  ASSUME_YES=1; shift ;;
    --project) PROJECT_SCOPE=1; shift ;;
    -h|--help)
      cat <<'EOF'
Usage: uninstall-amp.sh [--yes] [--project]

Removes the Dash0 Amp plugin directory and the user-level config file. Other
Amp plugins are untouched.

Flags:
  -y, --yes   Skip the confirmation prompt.
  --project   Remove ./.amp/plugins/dash0 instead of the user-level plugin.
  -h, --help  Show this help.
EOF
      exit 0 ;;
    *) printf "✗ unknown argument: %s (try --help)\n" "$1" >&2; exit 1 ;;
  esac
done

if [ "$PROJECT_SCOPE" -eq 1 ]; then
  PLUGIN_DIR="$PWD/.amp/plugins/dash0"
else
  PLUGIN_DIR="$HOME/.config/amp/plugins/dash0"
fi
CONFIG_PATH="$HOME/.amp/dash0-agent-plugin.local.md"

printf '%sDash0 → Amp telemetry uninstaller%s\n\n' "$C_B" "$C_N"
printf "Will remove (if present):\n"
printf "  %s\n" "$PLUGIN_DIR" "$CONFIG_PATH"
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
remove_path "$PLUGIN_DIR"  "plugin directory"
remove_path "$CONFIG_PATH" "config file"

printf '\n%sDone.%s Reload plugins in Amp (or start a new session) so it stops loading the plugin.\n' "$C_B" "$C_N"
