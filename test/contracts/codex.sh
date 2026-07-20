#!/usr/bin/env bash
# Codex install/config contracts (runnable locally and in CI):
#   - credential delivery (config file + env vars) reaches a real OTLP request
#   - install-codex.sh merges hooks + pre-trust into config.toml, preserving user content
#   - uninstall-codex.sh strips the managed block, preserving user content
# Requires: go, make, jq, python3, curl, bash. No codex CLI needed.
set -euo pipefail
# shellcheck source=test/contracts/lib.sh
source "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/lib.sh"

VERSION=$(grep '^VERSION=' "$REPO/scripts/codex-on-event.sh" | sed 's/VERSION="//;s/"//')
BINNAME="codex-on-event-${VERSION}-$(os_arch)"
start_mock_otlp   # http://localhost:4319

echo "== Codex credential delivery reaches a real OTLP request =="
# Bootstrap is version-pinned; build the binary at that exact path so no release
# download is needed.
export DASH0_PLUGIN_DATA=/tmp/codex-pdata
rm -rf "$DASH0_PLUGIN_DATA"; mkdir -p "$DASH0_PLUGIN_DATA/bin"
make -C "$REPO" build-binary PKG=./cmd/codex-on-event OUT="$DASH0_PLUGIN_DATA/bin/$BINNAME"

# credentials from ~/.codex/dash0-agent-plugin.local.md.
export HOME=/tmp/codex-home-cfg; rm -rf "$HOME"; mkdir -p "$HOME/.codex"
cat > "$HOME/.codex/dash0-agent-plugin.local.md" <<'MD'
---
otlp_url: "http://localhost:4319"
auth_token: "codex-cfg-token"
dataset: "codex-cfg-ds"
---
MD
# Clean cwd so the repo's own .codex/ can't shadow the global config
# (codex-on-event.sh checks the project file first).
( cd "$(mktemp -d)" \
  && echo '{"hook_event_name":"SessionStart","session_id":"contract-g1","model":"gpt-5.5","source":"startup"}' \
     | bash "$REPO/scripts/codex-on-event.sh" )

# credentials from env vars only, no config file present.
export HOME=/tmp/codex-home-env; rm -rf "$HOME"; mkdir -p "$HOME/.codex"
( cd "$(mktemp -d)" \
  && echo '{"hook_event_name":"SessionStart","session_id":"contract-g2","model":"gpt-5.5","source":"startup"}' \
     | DASH0_OTLP_URL=http://localhost:4319 \
       CODEX_PLUGIN_OPTION_AUTH_TOKEN=codex-env-token \
       DASH0_DATASET=codex-env-ds \
       bash "$REPO/scripts/codex-on-event.sh" )

sleep 2
RESULT=$(curl -s http://localhost:4319/requests)
echo "$RESULT" | jq .
fail=0
[ "$(echo "$RESULT" | jq '[.requests[]|select(.auth=="Bearer codex-cfg-token")]|length')" -ge 1 ] \
  || { echo "ERROR: codex config-file token did not reach the OTLP request"; fail=1; }
[ "$(echo "$RESULT" | jq '[.requests[]|select(.auth=="Bearer codex-env-token")]|length')" -ge 1 ] \
  || { echo "ERROR: codex env-var token did not reach the OTLP request"; fail=1; }
[ "$fail" -eq 0 ] || exit 1
echo "PASS: config-file and env-var credentials flow through codex-on-event.sh to real OTLP requests"

echo "== install-codex.sh merges hooks + pre-trust into config.toml, preserving user content =="
# Codex has no release yet, so pre-stage the version-pinned binary + bootstrap;
# install-codex.sh skips the download when they're present.
export HOME=/tmp/codex-installer-home XDG_STATE_HOME=/tmp/codex-installer-state
rm -rf "$HOME" "$XDG_STATE_HOME"
STATE_BASE="$XDG_STATE_HOME/dash0-agent-plugin/codex"
mkdir -p "$STATE_BASE/bin" "$HOME/.codex"
make -C "$REPO" build-binary PKG=./cmd/codex-on-event OUT="$STATE_BASE/bin/$BINNAME"
cp "$REPO/scripts/codex-on-event.sh" "$STATE_BASE/codex-on-event.sh"

# Seed config.toml with an unrelated setting AND a user-authored hook the
# installer must preserve.
cat > "$HOME/.codex/config.toml" <<'TOML'
model = "gpt-5.5"

[[hooks.PreToolUse]]
matcher = "*"
[[hooks.PreToolUse.hooks]]
type = "command"
command = 'echo user-hook'
TOML

DASH0_VERSION="$VERSION" \
DASH0_OTLP_URL=http://localhost:4319 \
DASH0_AUTH_TOKEN=codex-install-token \
  bash "$REPO/install-codex.sh" 2>&1 | tail -25

CONFIG_TOML="$HOME/.codex/config.toml"
cat "$CONFIG_TOML"
fail=0
[ -f "$HOME/.codex/dash0-agent-plugin.local.md" ] \
  || { echo "ERROR: installer did not write the config .local.md"; fail=1; }
grep -q ">>> dash0-agent-plugin (managed)" "$CONFIG_TOML" \
  || { echo "ERROR: managed block not appended to config.toml"; fail=1; }
trust_n=$(grep -c 'trusted_hash = "sha256:' "$CONFIG_TOML" || true)
[ "$trust_n" -eq 10 ] \
  || { echo "ERROR: expected 10 pre-trust entries, found $trust_n"; fail=1; }
python3 - "$CONFIG_TOML" <<'PY' || fail=1
import sys, tomllib
d = tomllib.load(open(sys.argv[1], "rb"))
assert d.get("model") == "gpt-5.5", "user setting lost"
pre = d["hooks"]["PreToolUse"]
cmds = [h["command"] for g in pre for h in g["hooks"]]
assert any(c == "echo user-hook" for c in cmds), "user hook lost"
assert any("codex-on-event.sh" in c for c in cmds), "dash0 PreToolUse hook missing"
assert len(d["hooks"]["state"]) == 10, f"expected 10 trust keys, got {len(d['hooks']['state'])}"
print("TOML OK: user content preserved, dash0 hooks + trust present")
PY
[ "$fail" -eq 0 ] || exit 1
echo "PASS: install-codex.sh merged hooks + pre-trust and preserved user config"

echo "== uninstall-codex.sh strips the managed block, preserves user content =="
# Depends on the install step's merged config.toml above.
[ -f "$CONFIG_TOML" ] || { echo "ERROR: the install step did not produce a config.toml"; exit 1; }
bash "$REPO/uninstall-codex.sh" --yes 2>&1 | tail -20
cat "$CONFIG_TOML"
fail=0
for p in "$HOME/.codex/dash0-agent-plugin.local.md" "$XDG_STATE_HOME/dash0-agent-plugin/codex"; do
  [ -e "$p" ] && { echo "ERROR: uninstaller left behind: $p"; fail=1; }
done
grep -q ">>> dash0-agent-plugin (managed)" "$CONFIG_TOML" \
  && { echo "ERROR: managed block survived uninstall"; fail=1; }
grep -q 'codex-on-event.sh' "$CONFIG_TOML" \
  && { echo "ERROR: dash0 hook command survived uninstall"; fail=1; }
python3 - "$CONFIG_TOML" <<'PY' || fail=1
import sys, tomllib
d = tomllib.load(open(sys.argv[1], "rb"))
assert d.get("model") == "gpt-5.5", "user setting lost on uninstall"
cmds = [h["command"] for g in d["hooks"]["PreToolUse"] for h in g["hooks"]]
assert cmds == ["echo user-hook"], f"user hook not cleanly preserved: {cmds}"
assert "state" not in d.get("hooks", {}), "trust state survived uninstall"
print("TOML OK: user content intact, dash0 fully removed")
PY
[ "$fail" -eq 0 ] || exit 1
echo "PASS: uninstall-codex.sh stripped the managed block and preserved user config"

echo "ALL CODEX CONTRACTS PASSED"
