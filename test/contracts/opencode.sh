#!/usr/bin/env bash
# OpenCode install/config contracts (runnable locally and in CI):
#   - credential delivery (config file + env vars) reaches a real OTLP request
#   - the shared DASH0_AUTH_TOKEN namespace is never read for the token
#   - install-opencode.sh lays out the plugin file, wrapper and config
#   - uninstall-opencode.sh removes them and leaves the user's config in place
# Requires: go, make, jq, curl, bash + network (the install contract resolves +
# downloads the latest opencode release). No opencode CLI needed.
set -euo pipefail
# shellcheck source=test/contracts/lib.sh
source "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/lib.sh"

start_mock_otlp   # http://localhost:4319

WRAPPER="$REPO/opencode/opencode-on-event.sh"

# What the plugin writes to the wrapper's stdin for a new session: a
# session.created envelope, which the pipeline turns into SessionStart and
# answers with the connectivity check that carries the resolved credentials.
session_start() {
  printf '{"kind":"event","name":"session.created","payload":{"properties":{"info":{"id":"%s","directory":"/tmp"}}},"cwd":"/tmp","root_session_id":"%s","assistants":{}}' "$1" "$1"
}

echo "== OpenCode credential delivery reaches a real OTLP request =="
export DASH0_PLUGIN_DATA=/tmp/opencode-pdata
VERSION=$(grep '^VERSION=' "$WRAPPER" | sed 's/VERSION="//;s/"//')
rm -rf "$DASH0_PLUGIN_DATA"; mkdir -p "$DASH0_PLUGIN_DATA/bin"
make -C "$REPO" build-binary PKG=./cmd/opencode-on-event OUT="$DASH0_PLUGIN_DATA/bin/opencode-on-event-${VERSION}-$(os_arch)"

# credentials from ~/.config/opencode/dash0-agent-plugin.local.md.
export HOME=/tmp/opencode-home-cfg; rm -rf "$HOME"; mkdir -p "$HOME/.config/opencode"
cat > "$HOME/.config/opencode/dash0-agent-plugin.local.md" <<'MD'
---
otlp_url: "http://localhost:4319"
auth_token: "opencode-cfg-token"
dataset: "opencode-cfg-ds"
---
MD
# Clean cwd so the repo's own .opencode/ can't shadow the global config.
( cd "$(mktemp -d)" && session_start contract-o1 | bash "$WRAPPER" )

# credentials from env vars only, no config file present. DASH0_AUTH_TOKEN is set
# to a value that must never be used: the token lives in the agent-scoped secure
# namespace only, so a token meant for another tool cannot leak into OpenCode.
export HOME=/tmp/opencode-home-env; rm -rf "$HOME"; mkdir -p "$HOME/.config/opencode"
( cd "$(mktemp -d)" \
  && session_start contract-o2 \
     | DASH0_OTLP_URL=http://localhost:4319 \
       OPENCODE_PLUGIN_OPTION_AUTH_TOKEN=opencode-env-token \
       DASH0_AUTH_TOKEN=opencode-shared-token \
       DASH0_DATASET=opencode-env-ds \
       bash "$WRAPPER" )

# `enabled: false` must exit silently without exporting anything.
export HOME=/tmp/opencode-home-off; rm -rf "$HOME"; mkdir -p "$HOME/.config/opencode"
cat > "$HOME/.config/opencode/dash0-agent-plugin.local.md" <<'MD'
---
enabled: false
otlp_url: "http://localhost:4319"
auth_token: "opencode-disabled-token"
---
MD
( cd "$(mktemp -d)" && session_start contract-o3 | bash "$WRAPPER" )

sleep 2
RESULT=$(curl -s http://localhost:4319/requests)
echo "$RESULT" | jq .
fail=0
[ "$(echo "$RESULT" | jq '[.requests[]|select(.auth=="Bearer opencode-cfg-token")]|length')" -ge 1 ] \
  || { echo "ERROR: opencode config-file token did not reach the OTLP request"; fail=1; }
[ "$(echo "$RESULT" | jq '[.requests[]|select(.auth=="Bearer opencode-env-token")]|length')" -ge 1 ] \
  || { echo "ERROR: opencode env-var token did not reach the OTLP request"; fail=1; }
[ "$(echo "$RESULT" | jq '[.requests[]|select(.auth=="Bearer opencode-shared-token")]|length')" -eq 0 ] \
  || { echo "ERROR: DASH0_AUTH_TOKEN was used as the OpenCode auth token"; fail=1; }
[ "$(echo "$RESULT" | jq '[.requests[]|select(.auth=="Bearer opencode-disabled-token")]|length')" -eq 0 ] \
  || { echo "ERROR: a config with enabled: false still exported"; fail=1; }
[ "$fail" -eq 0 ] || exit 1
echo "PASS: config-file and env-var credentials flow through opencode-on-event.sh to real OTLP requests"

echo "== a cached binary that fails checksum verification is discarded, not run =="
export HOME=/tmp/opencode-home-checksum; rm -rf "$HOME"; mkdir -p "$HOME/.config/opencode"
BINARY="$DASH0_PLUGIN_DATA/bin/opencode-on-event-${VERSION}-$(os_arch)"
cp "$BINARY" "$BINARY.orig"
# Record a digest the binary cannot match, standing in for bytes tampered with
# after a verified download.
echo "0000000000000000000000000000000000000000000000000000000000000000" > "$BINARY.sha256"
( cd "$(mktemp -d)" && session_start contract-o4 | bash "$WRAPPER" ) && rc=0 || rc=$?
fail=0
[ "$rc" -eq 0 ] || { echo "ERROR: the wrapper exited $rc instead of failing open"; fail=1; }
[ -e "$BINARY" ] && { echo "ERROR: the wrapper kept a binary that failed verification"; fail=1; }
[ "$fail" -eq 0 ] || exit 1
mv "$BINARY.orig" "$BINARY"
echo "PASS: a corrupted cached binary is removed and the wrapper exits 0"

# The installers land in task 6.4. Until then this is a plain skip rather than
# skip_or_fail, because this script is already a required CI step and must not
# report red for work that has not been written yet. Turn it into skip_or_fail
# once install-opencode.sh exists, so a later deletion cannot silently pass.
if [ ! -f "$REPO/install-opencode.sh" ]; then
  echo "SKIP: install-opencode.sh is not present yet (task 6.4) — the install and uninstall contracts do not run"
  exit 0
fi

# The credential contracts above pin DASH0_PLUGIN_DATA to a scratch directory.
# The install and uninstall contracts must see the real default cache location
# instead, which is where uninstall-opencode.sh is expected to strip binaries.
unset DASH0_PLUGIN_DATA OPENCODE_PLUGIN_DATA

echo "== install-opencode.sh lays out the plugin file, wrapper and config =="
# Capture curl output first, then parse — piping directly into `grep -m1` closes
# the pipe early and makes curl exit 23 (write error) under `set -o pipefail`.
latest_json=$(curl -fsSL https://api.github.com/repos/dash0hq/dash0-agent-plugin/releases/latest) \
  || skip_or_fail "could not reach the GitHub releases API (network or rate limit)"
DASH0_VERSION=$(printf '%s' "$latest_json" | grep -m1 '"tag_name"' | cut -d'"' -f4 | sed 's/^v//' || true)
[ -n "$DASH0_VERSION" ] \
  || skip_or_fail "the releases API returned no tag_name — no published release to test the installer against"
echo "testing installer against v$DASH0_VERSION artifacts"

export HOME=/tmp/opencode-installer-home XDG_STATE_HOME=/tmp/opencode-installer-state
rm -rf "$HOME" "$XDG_STATE_HOME"; mkdir -p "$HOME/.config/opencode"

# Seed a user-authored config the installer must preserve wholesale.
cat > "$HOME/.config/opencode/opencode.json" <<'JSON'
{
  "$schema": "https://opencode.ai/config.json",
  "theme": "user-chosen"
}
JSON

DASH0_VERSION="$DASH0_VERSION" \
DASH0_OTLP_URL=http://localhost:4319 \
DASH0_AUTH_TOKEN=e2e-token \
  bash "$REPO/install-opencode.sh" 2>&1 | tail -25

fail=0
PLUGIN_DIR="$HOME/.config/opencode/plugin"
for p in \
  "$PLUGIN_DIR/dash0-opencode-plugin.js" \
  "$PLUGIN_DIR/opencode-on-event.sh" \
  "$HOME/.config/opencode/dash0-agent-plugin.local.md" ; do
  [ -f "$p" ] || { echo "ERROR: installer did not create expected file: $p"; fail=1; }
done
[ -x "$PLUGIN_DIR/opencode-on-event.sh" ] \
  || { echo "ERROR: the wrapper is not executable"; fail=1; }
theme=$(jq -r '.theme // ""' "$HOME/.config/opencode/opencode.json")
[ "$theme" = "user-chosen" ] \
  || { echo "ERROR: installer clobbered the user's opencode.json (theme: $theme)"; fail=1; }
[ "$fail" -eq 0 ] || exit 1
echo "PASS: installer produced the plugin file, wrapper and config with the user's own config preserved"

echo "== uninstall-opencode.sh strips the plugin and leaves the user config in place =="
# The wrapper caches downloaded binaries under the data dir on first run, which
# the installer never creates. Stand one in so the uninstaller has something to
# strip and the assertion below can actually fail.
CACHE_DIR="$XDG_STATE_HOME/dash0-agent-plugin/opencode"
mkdir -p "$CACHE_DIR/bin"
touch "$CACHE_DIR/bin/opencode-on-event-${VERSION}-$(os_arch)"

bash "$REPO/uninstall-opencode.sh" --yes 2>&1 | tail -20
fail=0
for p in \
  "$PLUGIN_DIR/dash0-opencode-plugin.js" \
  "$PLUGIN_DIR/opencode-on-event.sh" \
  "$CACHE_DIR" ; do
  [ -e "$p" ] && { echo "ERROR: uninstaller left behind: $p"; fail=1; }
done
[ -f "$HOME/.config/opencode/opencode.json" ] \
  || { echo "ERROR: uninstaller deleted the user's opencode.json"; fail=1; }
[ "$fail" -eq 0 ] || exit 1
echo "PASS: uninstaller removed the plugin and preserved the user's own config"
echo "ALL OPENCODE CONTRACTS PASSED"
