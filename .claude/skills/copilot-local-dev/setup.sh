#!/usr/bin/env bash
# SPDX-FileCopyrightText: Copyright 2026 Dash0 Inc.
# SPDX-License-Identifier: Apache-2.0
#
# Install the Dash0 GitHub Copilot CLI plugin LOCALLY for dev/testing — no
# GitHub push or release needed. It registers a throwaway local marketplace
# pointing at this repo's copilot/ package, installs it the real way (so manifest
# loading, camelCase hooks, ${PLUGIN_ROOT}, the dash0-configure skill and the
# bare-install guard are all exercised), and drops a locally-built binary where
# the bootstrap looks so it skips the GitHub Releases download.
#
# Idempotent — safe to re-run. Usage:
#   setup.sh            full (re)install (run after changing plugin FILES:
#                       hooks.json / plugin.json / skill / bootstrap)
#   setup.sh --rebuild  only rebuild the Go binary (run after changing Go code)
#
# Layout discovered against Copilot CLI 1.0.71:
#   PLUGIN_ROOT         ~/.copilot/installed-plugins/<marketplace>/<plugin>
#   COPILOT_PLUGIN_DATA ~/.copilot/plugin-data/<marketplace>/<plugin>   (bin/ here)
set -euo pipefail

SKILL_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO="$(git -C "$SKILL_DIR" rev-parse --show-toplevel)"

MP_NAME="dash0-local"
PLUGIN="dash0-agent-plugin"
MP_DIR="$HOME/.local/state/dash0-agent-plugin/copilot-dev-marketplace"
PLUGIN_DATA="$HOME/.copilot/plugin-data/$MP_NAME/$PLUGIN"
OTEL_DIR="$HOME/.local/state/dash0-agent-plugin/copilot/otel"

command -v copilot >/dev/null || { echo "error: copilot CLI not found — npm install -g @github/copilot" >&2; exit 1; }
command -v go >/dev/null || { echo "error: go not found" >&2; exit 1; }

VERSION="$(grep '^VERSION=' "$REPO/copilot/copilot-on-event.sh" | cut -d'"' -f2)"
OS="$(go env GOOS)"; ARCH="$(go env GOARCH)"
BIN="$PLUGIN_DATA/bin/copilot-on-event-$VERSION-$OS-$ARCH"

build_binary() {
  mkdir -p "$(dirname "$BIN")"
  ( cd "$REPO" && go build -o "$BIN" ./cmd/copilot-on-event )
  echo "→ built binary: $BIN"
}

if [ "${1:-}" = "--rebuild" ]; then
  build_binary
  echo "Rebuilt. Start a NEW copilot session to pick it up."
  exit 0
fi

# 1. Stage a local marketplace: marketplace.json + a CLEAN copy of copilot/
#    (excluding the dev-only capture harness and git-ignored captures, which
#    must not ship inside the plugin).
echo "→ staging local marketplace at $MP_DIR"
rm -rf "$MP_DIR"
mkdir -p "$MP_DIR/.github/plugin" "$MP_DIR/copilot"
rsync -a --exclude 'capture' --exclude 'captured' "$REPO/copilot/" "$MP_DIR/copilot/"
cat > "$MP_DIR/.github/plugin/marketplace.json" <<JSON
{
  "name": "$MP_NAME",
  "owner": { "name": "Dash0 (local dev)" },
  "metadata": { "description": "Local dev marketplace for the Dash0 Copilot plugin", "version": "0.0.0" },
  "plugins": [
    { "name": "$PLUGIN", "description": "Dash0 OpenTelemetry for GitHub Copilot CLI", "version": "$VERSION", "source": "copilot" }
  ]
}
JSON

# 2. (Re)register the marketplace and (re)install the plugin the real way.
echo "→ registering marketplace + installing plugin"
# Uninstall the plugin BEFORE removing the marketplace (remove needs --force
# while its plugins are installed); then re-add so it points at the freshly
# staged $MP_DIR, and reinstall.
copilot plugin uninstall "$PLUGIN" >/dev/null 2>&1 || true
copilot plugin marketplace remove "$MP_NAME" --force >/dev/null 2>&1 || true
copilot plugin marketplace add "$MP_DIR" >/dev/null
copilot plugin install "$PLUGIN@$MP_NAME"

# 3. Drop the locally-built binary where the bootstrap expects it, so it skips
#    the release download (there's no build for a local/unreleased version).
build_binary
mkdir -p "$OTEL_DIR"

cat <<MSG

✅ Plugin installed locally (marketplace: $MP_NAME).

One manual step remains — set your Dash0 credentials AND enable native OTel.
Start copilot and run the bundled skill:

    /dash0-configure

It writes ~/.copilot/dash0-agent-plugin.local.md (your OTLP URL + token) and
installs a launch function that enables Copilot's native OTel into
$OTEL_DIR (the source of per-turn token/cost/model).

Then open a FRESH shell and use \`copilot\` — tool + chat spans with per-turn
tokens land in your Dash0 dataset.

  (Prefer no profile function? Instead of step B of /dash0-configure, export
   per-session:  COPILOT_OTEL_ENABLED=true
                 COPILOT_OTEL_FILE_EXPORTER_PATH="$OTEL_DIR/otel.jsonl"
                 OTEL_INSTRUMENTATION_GENAI_CAPTURE_MESSAGE_CONTENT=true  # prompt/response text)

Iterate:
  • Go code changed       →  $SKILL_DIR/setup.sh --rebuild   (+ new copilot session)
  • plugin files changed  →  $SKILL_DIR/setup.sh             (re-installs)
  • remove everything     →  $SKILL_DIR/teardown.sh
MSG
