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
PLUGIN_ROOT="$HOME/.copilot/installed-plugins/$MP_NAME/$PLUGIN"
OTEL_DIR="$HOME/.local/state/dash0-agent-plugin/copilot/otel"
# Left behind by an earlier version of this script, which registered the hooks at
# user scope. The install itself registers them (the manifest's `hooks` key), so
# both files firing means every event runs the bootstrap twice.
STALE_HOOKS_FILE="$HOME/.copilot/hooks/$MP_NAME.json"

command -v copilot >/dev/null || { echo "error: copilot CLI not found — npm install -g @github/copilot" >&2; exit 1; }
command -v go >/dev/null || { echo "error: go not found" >&2; exit 1; }

VERSION="$(grep '^VERSION=' "$REPO/copilot/copilot-on-event.sh" | cut -d'"' -f2)"
OS="$(go env GOOS)"; ARCH="$(go env GOARCH)"
# EXE mirrors what copilot-on-event.sh derives: GoReleaser appends .exe to the
# Windows build, and the bootstrap looks for that exact name. Without it the
# binary is staged under a name nothing resolves, so the bootstrap falls through
# to the release download and 404s.
EXE=""
[ "$OS" = "windows" ] && EXE=".exe"
BIN="$PLUGIN_DATA/bin/copilot-on-event-$VERSION-$OS-$ARCH$EXE"

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

# 1. Stage a local marketplace from the REAL shipped files: the repo's
#    .github/plugin/marketplace.json (only its `name` swapped to a dev name so it
#    can't clash with the production `dash0` marketplace) + a copy of copilot/.
#    Consuming the real file means a typo/schema drift there breaks local dev too.
echo "→ staging local marketplace at $MP_DIR"
rm -rf "$MP_DIR"
mkdir -p "$MP_DIR/.github/plugin" "$MP_DIR/copilot"
# cp, not rsync: Git Bash on Windows ships no rsync, and copying into the empty
# dir just created makes `rsync -a` and `cp -R` equivalent here.
cp -R "$REPO/copilot/." "$MP_DIR/copilot/"
# Only the top-level `name` changes, so jq is the clean way when it is installed.
# Git Bash has no jq, hence the fallback. It anchors on the two-space indentation,
# which belongs to the top-level key alone: `owner.name` sits at four spaces and
# each plugin entry's at six, and those must keep their values. GNU sed's
# `0,/re/` first-match range would be the obvious way and is the wrong one — BSD
# sed accepts it, substitutes nothing and still exits 0, so the staged file would
# keep the name `dash0` and the `marketplace add` below would register itself over
# the production marketplace.
if command -v jq >/dev/null; then
  jq --arg n "$MP_NAME" '.name = $n' \
    "$REPO/.github/plugin/marketplace.json" > "$MP_DIR/.github/plugin/marketplace.json"
else
  sed 's/^  "name": ".*"/  "name": "'"$MP_NAME"'"/' \
    "$REPO/.github/plugin/marketplace.json" > "$MP_DIR/.github/plugin/marketplace.json"
fi
# Verify rather than trust. The failure this guards against was silent, and the
# consequence of missing it lands on the developer's real marketplace.
grep -q "^  \"name\": \"$MP_NAME\"" "$MP_DIR/.github/plugin/marketplace.json" \
  || { echo "error: could not rename the staged marketplace to $MP_NAME" >&2; exit 1; }

# 2. (Re)register the marketplace and (re)install the plugin the real way.
echo "→ registering marketplace + installing plugin"
# Uninstall the plugin BEFORE removing the marketplace (remove needs --force
# while its plugins are installed); then re-add so it points at the freshly
# staged $MP_DIR, and reinstall.
copilot plugin uninstall "$PLUGIN" >/dev/null 2>&1 || true
copilot plugin marketplace remove "$MP_NAME" --force >/dev/null 2>&1 || true
# A previous run's files left behind make `plugin install` fail with "Zugriff
# verweigert / access denied", and since the uninstall above already ran, that
# would leave no plugin installed at all.
rm -rf "$PLUGIN_ROOT"
copilot plugin marketplace add "$MP_DIR" >/dev/null
copilot plugin install "$PLUGIN@$MP_NAME"

# 2b. Drop the user-scope hooks file an earlier version of this script wrote, so
#     the install's own hooks are the only ones that fire.
if [ -f "$STALE_HOOKS_FILE" ]; then
  echo "→ removing stale user-scope hooks file $STALE_HOOKS_FILE"
  rm -f "$STALE_HOOKS_FILE"
fi

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
