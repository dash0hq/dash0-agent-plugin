#!/usr/bin/env bash
# SPDX-FileCopyrightText: Copyright 2026 Dash0 Inc.
# SPDX-License-Identifier: Apache-2.0
#
# Remove the local dev install created by setup.sh. Best-effort; safe to re-run.
set -uo pipefail

MP_NAME="dash0-local"
PLUGIN="dash0-agent-plugin"
MP_DIR="$HOME/.local/state/dash0-agent-plugin/copilot-dev-marketplace"

copilot plugin uninstall "$PLUGIN" >/dev/null 2>&1 || true
copilot plugin marketplace remove "$MP_NAME" --force >/dev/null 2>&1 || true
rm -rf "$MP_DIR"
rm -rf "$HOME/.copilot/plugin-data/$MP_NAME"
rm -rf "$HOME/.local/state/dash0-agent-plugin/copilot/otel"
rm -f "$HOME/.copilot/dash0-agent-plugin.local.md"

echo "✅ Local dev install removed."
echo "If you added the launch function / COPILOT_OTEL_* exports to your shell profile"
echo "(via /dash0-configure or by hand), remove them there too."
