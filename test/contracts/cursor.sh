#!/usr/bin/env bash
# Cursor install/config contracts (runnable locally and in CI):
#   D — credential delivery (config file + env vars) reaches a real OTLP request
#   E — install-cursor.sh lays out the plugin dir + merges into ~/.cursor/hooks.json (preserving user entries)
#   F — uninstall-cursor.sh strips Dash0 entries, preserves non-Dash0 hooks
# Requires: go, make, jq, python3, curl, bash + network (E resolves + downloads
# the latest cursor release). No cursor CLI needed.
set -euo pipefail
# shellcheck source=test/contracts/lib.sh
source "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/lib.sh"

start_mock_otlp   # http://localhost:4319

echo "== Contract D — Cursor credential delivery reaches a real OTLP request =="
export DASH0_PLUGIN_DATA=/tmp/cursor-pdata
VERSION=$(grep '^VERSION=' "$REPO/scripts/cursor-on-event.sh" | sed 's/VERSION="//;s/"//')
rm -rf "$DASH0_PLUGIN_DATA"; mkdir -p "$DASH0_PLUGIN_DATA/bin"
make -C "$REPO" build-binary PKG=./cmd/cursor-on-event OUT="$DASH0_PLUGIN_DATA/bin/cursor-on-event-${VERSION}-$(os_arch)"

# D1 — credentials from ~/.cursor/dash0-agent-plugin.local.md.
export HOME=/tmp/cursor-home-cfg; rm -rf "$HOME"; mkdir -p "$HOME/.cursor"
cat > "$HOME/.cursor/dash0-agent-plugin.local.md" <<'MD'
---
otlp_url: "http://localhost:4319"
auth_token: "cursor-cfg-token"
dataset: "cursor-cfg-ds"
---
MD
# Clean cwd so the repo's own .cursor/ can't shadow the global config.
( cd "$(mktemp -d)" \
  && echo '{"hook_event_name":"sessionStart","session_id":"contract-d1","conversation_id":"contract-d1","model":"default"}' \
     | bash "$REPO/scripts/cursor-on-event.sh" )

# D2 — credentials from env vars only, no config file present.
export HOME=/tmp/cursor-home-env; rm -rf "$HOME"; mkdir -p "$HOME/.cursor"
( cd "$(mktemp -d)" \
  && echo '{"hook_event_name":"sessionStart","session_id":"contract-d2","conversation_id":"contract-d2","model":"default"}' \
     | DASH0_OTLP_URL=http://localhost:4319 \
       CURSOR_PLUGIN_OPTION_AUTH_TOKEN=cursor-env-token \
       DASH0_DATASET=cursor-env-ds \
       bash "$REPO/scripts/cursor-on-event.sh" )

sleep 2
RESULT=$(curl -s http://localhost:4319/requests)
echo "$RESULT" | jq .
fail=0
[ "$(echo "$RESULT" | jq '[.requests[]|select(.auth=="Bearer cursor-cfg-token")]|length')" -ge 1 ] \
  || { echo "ERROR: cursor config-file token did not reach the OTLP request"; fail=1; }
[ "$(echo "$RESULT" | jq '[.requests[]|select(.auth=="Bearer cursor-env-token")]|length')" -ge 1 ] \
  || { echo "ERROR: cursor env-var token did not reach the OTLP request"; fail=1; }
[ "$fail" -eq 0 ] || exit 1
echo "PASS D: config-file and env-var credentials flow through cursor-on-event.sh to real OTLP requests"

echo "== Contract E — install-cursor.sh lays out the plugin dir + merges into ~/.cursor/hooks.json =="
# Capture curl output first, then parse — piping directly into `grep -m1` closes
# the pipe early and makes curl exit 23 (write error) under `set -o pipefail`.
latest_json=$(curl -fsSL https://api.github.com/repos/dash0hq/dash0-agent-plugin/releases/latest) || true
DASH0_VERSION=$(printf '%s' "$latest_json" | grep -m1 '"tag_name"' | cut -d'"' -f4 | sed 's/^v//' || true)
[ -n "$DASH0_VERSION" ] || { echo "WARNING: could not resolve latest release, skipping Contracts E/F"; exit 0; }
echo "testing installer against v$DASH0_VERSION artifacts"

export HOME=/tmp/cursor-installer-home XDG_STATE_HOME=/tmp/cursor-installer-state
rm -rf "$HOME" "$XDG_STATE_HOME"; mkdir -p "$HOME/.cursor"

# Seed a foreign hook the installer must preserve.
cat > "$HOME/.cursor/hooks.json" <<'JSON'
{
  "version": 1,
  "hooks": {
    "beforeSubmitPrompt": [{"command": "/tmp/user-owned-hook.sh"}]
  }
}
JSON

DASH0_VERSION="$DASH0_VERSION" \
DASH0_OTLP_URL=http://localhost:4319 \
DASH0_AUTH_TOKEN=e2e-token \
  bash "$REPO/install-cursor.sh" 2>&1 | tail -25

fail=0
EXPECTED_PATHS=(
  "$HOME/.cursor/plugins/local/dash0-agent-plugin/.cursor-plugin/plugin.json"
  "$HOME/.cursor/plugins/local/dash0-agent-plugin/cursor/plugin-hooks.json"
  "$HOME/.cursor/plugins/local/dash0-agent-plugin/cursor/skills/dash0-configure/SKILL.md"
  "$HOME/.cursor/plugins/local/dash0-agent-plugin/scripts/cursor-on-event.sh"
  "$HOME/.cursor/dash0-agent-plugin.local.md"
  "$HOME/.cursor/hooks.json"
)
for p in "${EXPECTED_PATHS[@]}"; do
  [ -f "$p" ] || { echo "ERROR: installer did not create expected file: $p"; fail=1; }
done
[ -x "$HOME/.cursor/plugins/local/dash0-agent-plugin/scripts/cursor-on-event.sh" ] \
  || { echo "ERROR: bootstrap script is not executable"; fail=1; }
cat "$HOME/.cursor/hooks.json"

# shellcheck disable=SC2016  # literal $HOME — Cursor expands it at hook invocation time
EXPECTED_CMD='$HOME/.cursor/plugins/local/dash0-agent-plugin/scripts/cursor-on-event.sh'
for ev in sessionStart sessionEnd beforeSubmitPrompt afterAgentResponse preToolUse postToolUse postToolUseFailure subagentStart subagentStop; do
  got=$(jq -r --arg ev "$ev" '.hooks[$ev] // [] | map(select(.command | contains("cursor-on-event.sh"))) | .[0].command // ""' "$HOME/.cursor/hooks.json")
  [ "$got" = "$EXPECTED_CMD" ] || { echo "ERROR: hooks.json missing or wrong command for $ev (got: $got)"; fail=1; }
done
user_hook=$(jq -r '.hooks.beforeSubmitPrompt[] | select(.command == "/tmp/user-owned-hook.sh") | .command' "$HOME/.cursor/hooks.json")
[ "$user_hook" = "/tmp/user-owned-hook.sh" ] \
  || { echo "ERROR: installer removed user-authored beforeSubmitPrompt hook"; fail=1; }
[ "$fail" -eq 0 ] || exit 1
echo "PASS E: installer produced expected plugin dir + merged hooks with foreign entry preserved"

echo "== Contract F — uninstall-cursor.sh strips Dash0 entries, preserves non-Dash0 hooks =="
[ -f "$HOME/.cursor/hooks.json" ] || { echo "ERROR: Contract E did not produce a hooks.json"; exit 1; }
bash "$REPO/uninstall-cursor.sh" --yes 2>&1 | tail -20
fail=0
for p in \
  "$HOME/.cursor/plugins/local/dash0-agent-plugin" \
  "$HOME/.cursor/dash0-agent-plugin.local.md" \
  "$XDG_STATE_HOME/dash0-agent-plugin/cursor" ; do
  [ -e "$p" ] && { echo "ERROR: uninstaller left behind: $p"; fail=1; }
done
if [ ! -f "$HOME/.cursor/hooks.json" ]; then
  echo "ERROR: uninstaller deleted ~/.cursor/hooks.json but a user-owned entry was present"; fail=1
else
  cat "$HOME/.cursor/hooks.json"
  dash0_left=$(jq -r '.hooks | to_entries[] | .value[]? | .command // empty' "$HOME/.cursor/hooks.json" | grep -c "cursor-on-event.sh" || true)
  [ "$dash0_left" -eq 0 ] || { echo "ERROR: uninstaller left $dash0_left Dash0 hook entry/entries"; fail=1; }
  user_hook=$(jq -r '.hooks.beforeSubmitPrompt[]? | select(.command == "/tmp/user-owned-hook.sh") | .command' "$HOME/.cursor/hooks.json")
  [ "$user_hook" = "/tmp/user-owned-hook.sh" ] \
    || { echo "ERROR: uninstaller removed user-authored beforeSubmitPrompt hook"; fail=1; }
fi
[ "$fail" -eq 0 ] || exit 1
echo "PASS F: uninstaller stripped Dash0 entries and preserved the user-authored hook"
echo "ALL CURSOR CONTRACTS PASSED"
