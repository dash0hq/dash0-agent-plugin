#!/usr/bin/env bash
# Claude Code install/config contracts (runnable locally and in CI):
#   A — settings.json alone does NOT install the plugin
#   B — `claude plugin install --config` persists creds where the plugin reads them
#   C — credential delivery (config file + env vars) reaches a real OTLP request
# Requires: claude CLI (A/B) + network + a published marketplace, plus go/make/jq/curl.
set -euo pipefail
# shellcheck source=test/contracts/lib.sh
source "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/lib.sh"

command -v claude >/dev/null || { echo "ERROR: claude CLI required for Contracts A/B — npm install -g @anthropic-ai/claude-code"; exit 1; }

echo "== Contract A — settings.json alone does NOT install the plugin =="
export HOME=/tmp/home-neg; rm -rf "$HOME"; mkdir -p "$HOME/.claude"
cat > "$HOME/.claude/settings.json" <<'JSON'
{
  "extraKnownMarketplaces": {
    "dash0": { "source": { "source": "github", "repo": "dash0hq/claude-marketplace" } }
  },
  "enabledPlugins": { "dash0-agent-plugin@dash0": true }
}
JSON
claude plugin list 2>&1 | tee /tmp/list-neg.txt || true
if compgen -G "$HOME/.claude/plugins/cache/*dash0*" >/dev/null; then
  echo "ERROR: settings.json alone installed the plugin — fleet docs/assumptions must be revisited"; exit 1
fi
echo "PASS A: settings.json (extraKnownMarketplaces + enabledPlugins) alone does NOT install; explicit install required"

echo "== Contract B — claude plugin install --config persists creds where the plugin reads them =="
# B pins WHERE `--config` persists creds: non-sensitive → settings.json,
# AUTH_TOKEN → the secrets store (keychain, with a .credentials.json fallback on
# Linux). Those locations are OS-specific; the contract asserts the Linux layout
# that CI runs and the fleet docs depend on. On macOS the CLI uses the Keychain
# and a different config layout, so skip rather than emit a false failure.
if [ "$(uname -s)" != "Linux" ]; then
  echo "SKIP B: --config credential-storage layout is Linux-specific (validated in CI)"
else
export HOME=/tmp/home-pos; rm -rf "$HOME"; mkdir -p "$HOME/.claude"
# Published Dash0 marketplace installs into ~/.claude/plugins/cache — the
# cleanest signal to assert "installed" on.
claude plugin marketplace add dash0hq/claude-marketplace --scope user
claude plugin install dash0-agent-plugin@dash0 --scope user \
  --config OTLP_URL=https://probe.example.test \
  --config AUTH_TOKEN=contract-token-xyz \
  --config DATASET=contract-ds
cat "$HOME/.claude/settings.json"
cat "$HOME/.claude/.credentials.json" 2>/dev/null || echo "(none)"
fail=0
compgen -G "$HOME/.claude/plugins/cache/*dash0*" >/dev/null \
  || { echo "ERROR: plugin was not installed to cache"; fail=1; }
grep -q "https://probe.example.test" "$HOME/.claude/settings.json" \
  || { echo "ERROR: OTLP_URL not persisted to settings.json"; fail=1; }
grep -q "contract-ds" "$HOME/.claude/settings.json" \
  || { echo "ERROR: DATASET not persisted to settings.json"; fail=1; }
grep -q "contract-token-xyz" "$HOME/.claude/settings.json" \
  && { echo "ERROR: AUTH_TOKEN leaked into settings.json (should be in secrets store)"; fail=1; }
grep -q "contract-token-xyz" "$HOME/.claude/.credentials.json" 2>/dev/null \
  || { echo "ERROR: AUTH_TOKEN not stored in the secrets store (.credentials.json)"; fail=1; }
[ "$fail" -eq 0 ] || exit 1
echo "PASS B: --config installs + persists non-sensitive->settings.json, AUTH_TOKEN->secrets store"
fi

echo "== Contract C — credential delivery reaches a real OTLP request =="
start_mock_otlp   # http://localhost:4319
export CLAUDE_PLUGIN_DATA=/tmp/pdata
VERSION=$(grep '^VERSION=' "$REPO/scripts/on-event.sh" | sed 's/VERSION="//;s/"//')
rm -rf "$CLAUDE_PLUGIN_DATA"; mkdir -p "$CLAUDE_PLUGIN_DATA/bin"
make -C "$REPO" build-binary PKG=./cmd/on-event OUT="$CLAUDE_PLUGIN_DATA/bin/on-event-${VERSION}-$(os_arch)"

# C1 — credentials from ~/.claude/dash0-agent-plugin.local.md.
export HOME=/tmp/home-cfg; rm -rf "$HOME"; mkdir -p "$HOME/.claude"
cat > "$HOME/.claude/dash0-agent-plugin.local.md" <<'MD'
---
otlp_url: "http://localhost:4319"
auth_token: "cfg-file-token"
dataset: "cfg-file-ds"
---
MD
( cd "$(mktemp -d)" \
  && echo '{"hook_event_name":"SessionStart","session_id":"contract-c1","model":"opus"}' \
     | bash "$REPO/scripts/on-event.sh" )

# C2 — credentials from env vars only, no config file present.
export HOME=/tmp/home-env; rm -rf "$HOME"; mkdir -p "$HOME/.claude"
( cd "$(mktemp -d)" \
  && echo '{"hook_event_name":"SessionStart","session_id":"contract-c2","model":"opus"}' \
     | DASH0_OTLP_URL=http://localhost:4319 \
       CLAUDE_PLUGIN_OPTION_AUTH_TOKEN=env-token \
       DASH0_DATASET=env-ds \
       bash "$REPO/scripts/on-event.sh" )

sleep 2
RESULT=$(curl -s http://localhost:4319/requests)
echo "$RESULT" | jq .
fail=0
[ "$(echo "$RESULT" | jq '[.requests[]|select(.auth=="Bearer cfg-file-token")]|length')" -ge 1 ] \
  || { echo "ERROR: config-file token did not reach the OTLP request"; fail=1; }
[ "$(echo "$RESULT" | jq '[.requests[]|select(.auth=="Bearer env-token")]|length')" -ge 1 ] \
  || { echo "ERROR: env-var token did not reach the OTLP request"; fail=1; }
[ "$fail" -eq 0 ] || exit 1
echo "PASS C: config-file and env-var credentials flow through on-event.sh to real OTLP requests"
echo "ALL CLAUDE CONTRACTS PASSED"
