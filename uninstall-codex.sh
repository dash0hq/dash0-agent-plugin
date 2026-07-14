#!/usr/bin/env bash
# SPDX-FileCopyrightText: Copyright 2026 Dash0 Inc.
# SPDX-License-Identifier: Apache-2.0

# Dash0 — OpenAI Codex telemetry uninstaller.
#
# Usage:
#   ./uninstall-codex.sh                       # prompts before deleting
#   ./uninstall-codex.sh --yes                 # skips confirmation
#   curl -fsSL .../uninstall-codex.sh | bash -s -- --yes
#
# What this removes:
#   ~/.codex/config.toml       the managed block ONLY (hooks + trust the plugin
#                              added, between its markers). User-authored hooks
#                              and all other config are preserved. If the file
#                              ends up empty, it is deleted.
#   ~/.codex/dash0-agent-plugin.local.md      credential config
#   ~/.local/state/dash0-agent-plugin/codex/  binary cache + bootstrap script

set -u

if [ -t 1 ]; then
  C_R=$'\033[31m'; C_G=$'\033[32m'; C_Y=$'\033[33m'; C_B=$'\033[1m'; C_N=$'\033[0m'
else
  C_R=""; C_G=""; C_Y=""; C_B=""; C_N=""
fi
info()  { printf "%s\n" "$1"; }
ok()    { printf "${C_G}✓${C_N} %s\n" "$1"; }
warn()  { printf "${C_Y}!${C_N} %s\n" "$1"; }
die()   { printf "${C_R}✗${C_N} %s\n" "$1" >&2; exit 1; }

ASSUME_YES=0
while [ $# -gt 0 ]; do
  case "$1" in
    -y|--yes) ASSUME_YES=1; shift ;;
    -h|--help)
      cat <<'EOF'
Usage: uninstall-codex.sh [--yes]

Removes the Dash0 Codex plugin. Only the marker-delimited managed block in
~/.codex/config.toml is stripped; user-authored hooks and other config stay.

Flags:
  -y, --yes   Skip the confirmation prompt.
  -h, --help  Show this help.
EOF
      exit 0 ;;
    *) printf "✗ unknown argument: %s (try --help)\n" "$1" >&2; exit 1 ;;
  esac
done

CONFIG_TOML="$HOME/.codex/config.toml"
CONFIG_PATH="$HOME/.codex/dash0-agent-plugin.local.md"
STATE_DIR="${XDG_STATE_HOME:-$HOME/.local/state}/dash0-agent-plugin/codex"

printf '%sDash0 → OpenAI Codex telemetry uninstaller%s\n\n' "$C_B" "$C_N"
printf "Will remove (if present):\n"
printf "  %s (managed block only; user hooks + config preserved)\n" "$CONFIG_TOML"
printf "  %s\n" "$CONFIG_PATH" "$STATE_DIR"
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

# Strip the managed block from config.toml, preserving everything else.
if [ -f "$CONFIG_TOML" ]; then
  if grep -q ">>> dash0-agent-plugin (managed)" "$CONFIG_TOML"; then
    STRIPPED_TMP=$(mktemp)
    awk '
      index($0, ">>> dash0-agent-plugin (managed)") { skip=1 }
      !skip { print }
      index($0, "<<< dash0-agent-plugin (managed)") { skip=0 }
    ' "$CONFIG_TOML" > "$STRIPPED_TMP" || { rm -f "$STRIPPED_TMP"; die "failed to read $CONFIG_TOML"; }
    # If nothing but whitespace remains, remove the file; else write it back.
    if [ -n "$(tr -d '[:space:]' < "$STRIPPED_TMP")" ]; then
      mv "$STRIPPED_TMP" "$CONFIG_TOML" && ok "stripped managed block from $CONFIG_TOML"
    else
      rm -f "$STRIPPED_TMP" "$CONFIG_TOML" && ok "removed $CONFIG_TOML (empty after strip)"
    fi
  else
    info "skip config.toml (no managed block): $CONFIG_TOML"
  fi
else
  info "skip config.toml (not present): $CONFIG_TOML"
fi

remove_path() {
  local p="$1" label="$2"
  if [ -e "$p" ] || [ -L "$p" ]; then
    rm -rf "$p" && ok "removed ${label} → ${p}"
  else
    info "skip ${label} (not present): ${p}"
  fi
}
remove_path "$CONFIG_PATH" "config file"
remove_path "$STATE_DIR"   "binary cache + bootstrap"

printf '\n%sDone.%s Start a new Codex session so it stops running the hooks.\n' "$C_B" "$C_N"
