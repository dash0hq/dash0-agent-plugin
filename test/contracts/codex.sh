#!/usr/bin/env bash
# Codex install/config contracts (runnable locally and in CI):
#   - credential delivery (config file + env vars) reaches a real OTLP request
#   - install-codex.sh merges hooks + pre-trust into config.toml, preserving user content
#   - uninstall-codex.sh strips the managed block, preserving user content
# Requires: go, make, jq, python3, curl, bash. No codex CLI needed.
set -euo pipefail
# shellcheck source=test/contracts/lib.sh
source "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/lib.sh"

VERSION=$(grep '^VERSION=' "$REPO/codex/codex-on-event.sh" | sed 's/VERSION="//;s/"//')
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
     | bash "$REPO/codex/codex-on-event.sh" )

# credentials from env vars only, no config file present.
export HOME=/tmp/codex-home-env; rm -rf "$HOME"; mkdir -p "$HOME/.codex"
( cd "$(mktemp -d)" \
  && echo '{"hook_event_name":"SessionStart","session_id":"contract-g2","model":"gpt-5.5","source":"startup"}' \
     | DASH0_OTLP_URL=http://localhost:4319 \
       CODEX_PLUGIN_OPTION_AUTH_TOKEN=codex-env-token \
       DASH0_DATASET=codex-env-ds \
       bash "$REPO/codex/codex-on-event.sh" )

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
# Pre-stage the version-pinned binary so no release download is needed. The
# bootstrap is pre-staged too, but the installer always re-fetches it now (see the
# upgrade contract below), so point it at the working copy over file:// to keep
# this case offline and testing the code in the tree.
export DASH0_RAW_BASE="file://$REPO"
export HOME=/tmp/codex-installer-home XDG_STATE_HOME=/tmp/codex-installer-state
rm -rf "$HOME" "$XDG_STATE_HOME"
STATE_BASE="$XDG_STATE_HOME/dash0-agent-plugin/codex"
mkdir -p "$STATE_BASE/bin" "$HOME/.codex"
make -C "$REPO" build-binary PKG=./cmd/codex-on-event OUT="$STATE_BASE/bin/$BINNAME"
cp "$REPO/codex/codex-on-event.sh" "$STATE_BASE/codex-on-event.sh"

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

echo "== install-codex.sh replaces an outdated bootstrap on re-install =="
# Re-running the installer is the documented upgrade path. SCRIPT_PATH is not
# version-pinned (unlike the binary), so the installer used to skip it when the
# file existed — leaving the old bootstrap, and with it the old VERSION pin, so
# the freshly downloaded binary was never the one that ran.
STALE_HOME=/tmp/codex-upgrade-home
export HOME=$STALE_HOME XDG_STATE_HOME=/tmp/codex-upgrade-state
rm -rf "$HOME" "$XDG_STATE_HOME"
UP_STATE="$XDG_STATE_HOME/dash0-agent-plugin/codex"
mkdir -p "$UP_STATE/bin" "$HOME/.codex"
make -C "$REPO" build-binary PKG=./cmd/codex-on-event OUT="$UP_STATE/bin/$BINNAME"

# A bootstrap from an imaginary older release, pinned to a version that is not
# the one being installed.
cat > "$UP_STATE/codex-on-event.sh" <<'SH'
#!/usr/bin/env bash
VERSION="0.0.1-stale"
exit 0
SH
chmod +x "$UP_STATE/codex-on-event.sh"

DASH0_VERSION="$VERSION" \
DASH0_RAW_BASE="file://$REPO" \
DASH0_OTLP_URL=http://localhost:4319 \
DASH0_AUTH_TOKEN=codex-upgrade-token \
  bash "$REPO/install-codex.sh" 2>&1 | tail -12

fail=0
INSTALLED_VERSION=$(grep '^VERSION=' "$UP_STATE/codex-on-event.sh" | sed 's/VERSION="//;s/"//')
[ "$INSTALLED_VERSION" = "$VERSION" ] \
  || { echo "ERROR: bootstrap still pinned to '$INSTALLED_VERSION', expected '$VERSION' — the upgrade did not replace it"; fail=1; }
[ -x "$UP_STATE/codex-on-event.sh" ] \
  || { echo "ERROR: installed bootstrap is not executable"; fail=1; }
# The guard has to reach config.toml, or a missing bootstrap surfaces as a bare
# nonzero exit code in the user's TUI.
grep -q 'hook_script_missing' "$HOME/.codex/config.toml" \
  || { echo "ERROR: hook command in config.toml has no missing-bootstrap guard"; fail=1; }
[ "$fail" -eq 0 ] || exit 1
echo "PASS: re-install replaced the outdated bootstrap and registered a guarded hook command"

echo "== uninstall-codex.sh strips the managed block, preserves user content =="
# The uninstall case below asserts against the install case's HOME, so restore it.
export HOME=/tmp/codex-installer-home XDG_STATE_HOME=/tmp/codex-installer-state
CONFIG_TOML="$HOME/.codex/config.toml"
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
