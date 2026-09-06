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

echo "== the four privacy dimensions in a config file reach the exported spans =="
# The wrapper is the only reader that finds the user-scoped config file, so this
# is what proves the four keys are wired end to end. The fixture is the same
# recorded OpenCode session the Go golden test replays.
ENVELOPES="$REPO/internal/source/opencode/testdata/forwarded_envelopes.jsonl"
PROMPT_TEXT="Read the readme, then read a missing file"
TOOL_ARGUMENT="hello from mcp"

# replay TOKEN DIMENSIONS — drive the recorded session through the wrapper with a
# config file carrying those dimensions, under a data dir of its own so the
# fixture's fixed session ids cannot carry state between scenarios.
replay() {
  local token="$1" dimensions="$2"
  export HOME="/tmp/opencode-home-$token"; rm -rf "$HOME"; mkdir -p "$HOME/.config/opencode"
  {
    echo "---"
    echo 'otlp_url: "http://localhost:4319"'
    echo "auth_token: \"$token\""
    printf '%s\n' "$dimensions"
    echo "---"
  } > "$HOME/.config/opencode/dash0-agent-plugin.local.md"

  export DASH0_PLUGIN_DATA="/tmp/opencode-pdata-$token"
  rm -rf "$DASH0_PLUGIN_DATA"; mkdir -p "$DASH0_PLUGIN_DATA/bin"
  cp "/tmp/opencode-pdata/bin/opencode-on-event-${VERSION}-$(os_arch)" \
     "$DASH0_PLUGIN_DATA/bin/opencode-on-event-${VERSION}-$(os_arch)"

  ( cd "$(mktemp -d)" && while IFS= read -r envelope; do
      printf '%s\n' "$envelope" | bash "$WRAPPER"
    done < "$ENVELOPES" )
}

# bodies TOKEN — every OTLP payload that carried that token, concatenated.
bodies() {
  curl -s http://localhost:4319/requests \
    | jq -r --arg auth "Bearer $1" '[.requests[]|select(.auth==$auth)|.body]|join("\n")'
}

replay opencode-dims-a "$(printf 'prompts: disabled\ntools: full')"
replay opencode-dims-b "$(printf 'prompts: full\ntools: disabled')"
replay opencode-dims-c "$(printf 'prompts: disabled\ntools: full\nskills: disabled\nagents: disabled')"

sleep 2
fail=0
A=$(bodies opencode-dims-a)
[ -n "$A" ] || { echo "ERROR: prompts: disabled / tools: full exported nothing at all"; fail=1; }
case "$A" in
  *"$PROMPT_TEXT"*) echo "ERROR: prompt text was exported at prompts: disabled"; fail=1 ;;
esac
case "$A" in
  *"$TOOL_ARGUMENT"*) ;;
  *) echo "ERROR: tool arguments were withheld at tools: full"; fail=1 ;;
esac

B=$(bodies opencode-dims-b)
[ -n "$B" ] || { echo "ERROR: prompts: full / tools: disabled exported nothing at all"; fail=1; }
case "$B" in
  *"$PROMPT_TEXT"*) ;;
  *) echo "ERROR: prompt text was withheld at prompts: full"; fail=1 ;;
esac
case "$B" in
  *"$TOOL_ARGUMENT"*) echo "ERROR: tool arguments were exported at tools: disabled"; fail=1 ;;
esac
case "$B" in
  *execute_tool*) echo "ERROR: an execute_tool span survived tools: disabled"; fail=1 ;;
esac
# agents was left unset here, so it sits at the default: the recorded session's
# delegation still gets its invoke_agent span. This is the contrast that gives
# the assertion below its teeth.
case "$B" in
  *invoke_agent*) ;;
  *) echo "ERROR: no invoke_agent span at the default agents level"; fail=1 ;;
esac

C=$(bodies opencode-dims-c)
[ -n "$C" ] || { echo "ERROR: the four-dimension config exported nothing at all"; fail=1; }
case "$C" in
  *invoke_agent*) echo "ERROR: an invoke_agent span survived agents: disabled"; fail=1 ;;
esac
case "$C" in
  *"$PROMPT_TEXT"*) echo "ERROR: prompt text was exported at prompts: disabled"; fail=1 ;;
esac
case "$C" in
  *"$TOOL_ARGUMENT"*) ;;
  *) echo "ERROR: tool arguments were withheld at tools: full"; fail=1 ;;
esac
[ "$fail" -eq 0 ] || exit 1
echo "PASS: the four dimensions configured in a config file govern the exported spans"

echo "== all four privacy keys travel from the config file into the binary's environment =="
# The recorded session invokes no skill, so `skills:` has no span of its own to
# assert against. Stand a stub in for the binary and read the environment the
# wrapper handed it, which is what the four keys are wired through.
export HOME=/tmp/opencode-home-dims-env; rm -rf "$HOME"; mkdir -p "$HOME/.config/opencode"
cat > "$HOME/.config/opencode/dash0-agent-plugin.local.md" <<'MD'
---
prompts: disabled
tools: full
skills: limited
agents: disabled
---
MD
export DASH0_PLUGIN_DATA=/tmp/opencode-pdata-dims-env
rm -rf "$DASH0_PLUGIN_DATA"; mkdir -p "$DASH0_PLUGIN_DATA/bin"
STUB="$DASH0_PLUGIN_DATA/bin/opencode-on-event-${VERSION}-$(os_arch)"
cat > "$STUB" <<'SH'
#!/usr/bin/env bash
env | grep '^DASH0_\(PROMPTS\|TOOLS\|SKILLS\|AGENTS\)='
SH
chmod +x "$STUB"
# No .sha256 next to the stub: an unrecorded digest is the one case the wrapper
# lets a cached binary through unverified.
DIMS_ENV=$( cd "$(mktemp -d)" && session_start contract-o5 | bash "$WRAPPER" )
fail=0
for expected in DASH0_PROMPTS=disabled DASH0_TOOLS=full DASH0_SKILLS=limited DASH0_AGENTS=disabled; do
  case "$DIMS_ENV" in
    *"$expected"*) ;;
    *) echo "ERROR: the wrapper did not export $expected (got: $DIMS_ENV)"; fail=1 ;;
  esac
done
[ "$fail" -eq 0 ] || exit 1
echo "PASS: prompts, tools, skills and agents all reach the binary as DASH0_* variables"

# The scenarios above each ran under a data dir of their own. The checksum
# contract below asserts against the binary the credential contracts built.
export DASH0_PLUGIN_DATA=/tmp/opencode-pdata

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
