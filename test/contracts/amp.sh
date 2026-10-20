#!/usr/bin/env bash
# Amp install/uninstall contracts (runnable locally and in CI):
#   - install-amp.sh lays out the plugin dir with the two names the bridge needs
#   - --project installs into the workspace instead of the user directory
#   - uninstall-amp.sh removes only the dash0 plugin directory
#   - --project uninstall leaves the machine-wide config file in place
# Requires: go, make, bash. No network: the contract installs from a local build
# via DASH0_SOURCE_DIR, which is also how a contributor installs a working tree.
# The release download path needs a published release and is not covered here.
set -euo pipefail
# shellcheck source=test/contracts/lib.sh
source "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/lib.sh"

SRC=$(mktemp -d)
make -C "$REPO" build-binary PKG=./cmd/amp-on-event OUT="$SRC/amp-on-event" >/dev/null
cp "$REPO/amp/index.ts" "$SRC/index.ts"
mkdir -p "$SRC/skills/dash0-configure"
cp "$REPO/amp/skills/dash0-configure/SKILL.md" "$SRC/skills/dash0-configure/SKILL.md"

export HOME=/tmp/amp-contract-home; rm -rf "$HOME"; mkdir -p "$HOME"
export DASH0_SOURCE_DIR="$SRC"
export DASH0_OTLP_URL=http://localhost:4319
export DASH0_AUTH_TOKEN=amp-contract-token
export DASH0_DATASET=amp-contract-ds

echo "== install-amp.sh installs index.ts + the helper under the names the bridge resolves =="
bash "$REPO/install-amp.sh" < /dev/null
PLUGIN_DIR="$HOME/.config/amp/plugins/dash0"
fail=0
# amp/index.ts spawns exactly ./amp-on-event next to itself, so the asset's
# -<os>-<arch> suffix must be gone and the file must be executable.
[ -f "$PLUGIN_DIR/index.ts" ]  || { echo "ERROR: no index.ts in $PLUGIN_DIR"; fail=1; }
[ -x "$PLUGIN_DIR/amp-on-event" ] || { echo "ERROR: no executable amp-on-event in $PLUGIN_DIR"; fail=1; }
# index.ts registers this exact relative path; Amp resolves it against the
# plugin directory, so a flat copy would register a skill that does not exist.
[ -f "$PLUGIN_DIR/skills/dash0-configure/SKILL.md" ] \
  || { echo "ERROR: no skills/dash0-configure/SKILL.md in $PLUGIN_DIR"; fail=1; }
grep -q 'auth_token: "amp-contract-token"' "$HOME/.amp/dash0-agent-plugin.local.md" \
  || { echo "ERROR: the token did not reach ~/.amp/dash0-agent-plugin.local.md"; fail=1; }
[ "$fail" -eq 0 ] || exit 1
echo "PASS: install-amp.sh laid out the plugin directory and wrote the config"

echo "== a re-run with no credentials keeps the config it would otherwise blank =="
# The config file is machine-wide and shared with every --project install, so a
# non-interactive re-run that answers nothing used to write auth_token: "" and
# stop every other project exporting. It also dropped export_usage: false, the
# one key step 8 tells the user to add.
echo 'export_usage: false' >> "$HOME/.amp/dash0-agent-plugin.local.md"
( unset DASH0_OTLP_URL DASH0_AUTH_TOKEN DASH0_DATASET; bash "$REPO/install-amp.sh" < /dev/null ) >/dev/null
grep -q 'auth_token: "amp-contract-token"' "$HOME/.amp/dash0-agent-plugin.local.md" \
  || { echo "ERROR: a credential-free re-run blanked the token"; exit 1; }
grep -q '^export_usage: false$' "$HOME/.amp/dash0-agent-plugin.local.md" \
  || { echo "ERROR: a re-run dropped the user's export_usage setting"; exit 1; }
[ "$(grep -c 'auth_token:' "$HOME/.amp/dash0-agent-plugin.local.md")" -eq 1 ] \
  || { echo "ERROR: the re-run duplicated keys instead of rewriting them"; exit 1; }
echo "PASS: the re-run preserved the token and the user's own keys"

echo "== --project installs into the workspace instead of the user directory =="
WORKSPACE=$(mktemp -d)
( cd "$WORKSPACE" && bash "$REPO/install-amp.sh" --project < /dev/null )
[ -x "$WORKSPACE/.amp/plugins/dash0/amp-on-event" ] \
  || { echo "ERROR: --project did not install into $WORKSPACE/.amp/plugins/dash0"; exit 1; }
echo "PASS: --project installed into the workspace"

echo "== --project uninstall leaves the machine-wide config alone =="
# Before the user-level uninstall, which removes that config itself.
( cd "$WORKSPACE" && bash "$REPO/uninstall-amp.sh" --project --yes )
[ ! -e "$WORKSPACE/.amp/plugins/dash0" ] || { echo "ERROR: --project uninstall left the plugin behind"; exit 1; }
[ -f "$HOME/.amp/dash0-agent-plugin.local.md" ] \
  || { echo "ERROR: --project uninstall removed the machine-wide config file"; exit 1; }
[ -x "$PLUGIN_DIR/amp-on-event" ] || { echo "ERROR: --project uninstall removed the user-level plugin"; exit 1; }
echo "PASS: --project uninstall stayed inside the workspace"

echo "== uninstall-amp.sh removes only the dash0 plugin directory =="
mkdir -p "$HOME/.config/amp/plugins/other"; echo keep > "$HOME/.config/amp/plugins/other/index.ts"
bash "$REPO/uninstall-amp.sh" --yes
[ ! -e "$PLUGIN_DIR" ] || { echo "ERROR: $PLUGIN_DIR survived the uninstall"; exit 1; }
[ ! -e "$HOME/.amp/dash0-agent-plugin.local.md" ] || { echo "ERROR: the config file survived the uninstall"; exit 1; }
[ -f "$HOME/.config/amp/plugins/other/index.ts" ] || { echo "ERROR: uninstall removed another plugin"; exit 1; }
echo "PASS: uninstall-amp.sh removed the Dash0 plugin and left the other one alone"
