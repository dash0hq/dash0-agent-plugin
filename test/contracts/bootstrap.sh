#!/usr/bin/env bash
# Bootstrap download contracts (runnable locally and in CI):
#   - every *-on-event.sh downloads via a private temp and renames into place
#   - none of them ends a hook with a non-zero exit
#   - a missing release disturbs nothing and installs nothing, but is reported
#   - concurrent invocations against a cold cache all succeed and converge
#
# Hooks run concurrently — parallel tool calls each fire their own, and every
# session on the machine shares one plugin data directory — so the first run
# after a version bump has N processes finding no binary at once. Writing the
# final path directly made them interleave into one file: measured against
# v0.1.25, 48 of 48 staggered invocations failed, each computing a different
# checksum, plus one process's cleanup deleting the file another was chmod'ing.
#
# Requires: curl, bash, sha256sum or shasum; jq for the JSON check (skipped
# without it). Network only for the concurrency contract.
set -euo pipefail
# shellcheck source=test/contracts/lib.sh
source "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/lib.sh"

SCRIPTS=(claude/claude-on-event.sh cursor/cursor-on-event.sh
         codex/codex-on-event.sh copilot/copilot-on-event.sh)

echo "== Every bootstrap writes the binary only by rename =="
# Static, so it holds whether or not a race reproduces here. Inside the download
# block the final path may appear only in the guard, the temp name derived from
# it, and the rename — any other use writes to a path a concurrent process may
# be exec'ing. Fails if someone restores `-o "$BINARY"`.
fail=0
for s in "${SCRIPTS[@]}"; do
  block=$(awk '/^if \[ ! -x "\$BINARY" \]/,/^fi$/' "$REPO/$s" | sed 's/#.*//')
  if [ -z "$block" ]; then
    echo "  FAIL $s: could not locate the download block — update this parser"
    fail=1
    continue
  fi
  # shellcheck disable=SC2016  # matching the literal string $BINARY, not expanding it
  bad=$(echo "$block" | grep -F '"$BINARY"' \
    | grep -vE '^if \[ ! -x "\$BINARY" \]|TMP="\$BINARY|mv -f "\$TMP" "\$BINARY"' || true)
  if [ -n "$bad" ]; then
    echo "  FAIL $s: download block touches \$BINARY outside the guard/temp/rename:"
    printf '    %s\n' "$bad"
    fail=1
    continue
  fi
  # shellcheck disable=SC2016
  echo "$block" | grep -qF 'mv -f "$TMP" "$BINARY"' || {
    echo "  FAIL $s: no rename into place"; fail=1; continue; }
  echo "  ok $s"
done
[ "$fail" -eq 0 ] || exit 1
echo "PASS: all ${#SCRIPTS[@]} bootstraps stage downloads in a temp and rename"

echo "== No bootstrap ends a hook with a non-zero exit =="
# Behavioural, not textual: a grep for `exit [1-9]` cannot see a `set -e` exit, a
# `:?` expansion or a failing exec, and the version of this check that did that
# printed PASS against a script that exited 1 in two reproducible cases. A
# read-only data directory poisons the first thing every bootstrap does.
fail=0
readonly_root=$(mktemp -d)
chmod a-w "$readonly_root"
if touch "$readonly_root/probe" 2>/dev/null; then
  # Running as root, or on a filesystem that ignores the mode bits.
  rm -f "$readonly_root/probe"
  echo "  SKIP: cannot make a directory unwritable here"
else
  for s in "${SCRIPTS[@]}"; do
    status=0
    # cwd as well as HOME: PROJECT_SETTINGS is the relative path
    # .claude/dash0-agent-plugin.local.md, so running from a directory holding
    # one with `enabled: false` exits every bootstrap before it reaches the
    # data directory, and this loop prints ok having exercised nothing.
    status=0
    ( cd "$readonly_root" && env -i PATH="$PATH" HOME="$readonly_root/nohome" \
      CLAUDE_PLUGIN_DATA="$readonly_root/d" \
      DASH0_PLUGIN_DATA="$readonly_root/d" \
      COPILOT_PLUGIN_DATA="$readonly_root/d" \
      bash "$REPO/$s" someEvent <<<'{"hook_event_name":"SessionStart"}' \
      >/dev/null 2>&1 ) || status=$?
    if [ "$status" -ne 0 ]; then
      echo "  FAIL $s: exited $status when its data directory could not be created"
      fail=1
    else
      echo "  ok $s"
    fi
  done
fi
chmod u+w "$readonly_root"; rm -rf "$readonly_root"
[ "$fail" -eq 0 ] || exit 1
echo "PASS: all ${#SCRIPTS[@]} bootstraps fail open"

echo "== A missing release does not disturb the session, and installs nothing =="
# The path a real install hits inside the release window.
missing=$(mktemp -d)
sed 's/^VERSION="[^"]*"/VERSION="9.9.9"/' "$REPO/claude/claude-on-event.sh" >"$missing/probe.sh"
data=$(mktemp -d)
status=0
# Hermetic HOME and cwd, as below: an `enabled: false` in either config exits
# before any download logic, and both assertions would pass on a path nothing
# executed.
( cd "$missing" && env -i PATH="$PATH" HOME="$missing/home" CLAUDE_PLUGIN_DATA="$data" \
    bash "$missing/probe.sh" <<<'{"hook_event_name":"SessionStart","session_id":"contract"}' \
    >/dev/null 2>&1 ) || status=$?
if [ "$status" -ne 0 ]; then
  echo "ERROR: a missing release exited $status — a hook error the user cannot act on" >&2
  exit 1
fi
# No binary, no half-written temp. The marker is deliberate state.
left=$(find "$data" -type f ! -name '.download-failing' 2>/dev/null | wc -l | tr -d ' ')
[ "$left" -eq 0 ] || { echo "ERROR: $left file(s) left behind after a failed download" >&2; exit 1; }
rm -rf "$missing" "$data"
echo "PASS: a missing release exits 0 and leaves nothing behind"

echo "== A failure is reported, once, not on every hook =="
# Exiting 0 alone would hide a proxy blocking github or a release that never
# published. Hermetic HOME and cwd: with the caller's, an `enabled: false` in
# ~/.claude/dash0-agent-plugin.local.md exits the probe before any download
# logic, and every assertion below would pass having tested nothing.
probe=$(mktemp -d); pdata=$(mktemp -d)
sed 's/^VERSION="[^"]*"/VERSION="9.9.9"/' "$REPO/claude/claude-on-event.sh" >"$probe/p.sh"
say() {
  ( cd "$probe" && env -i PATH="$PATH" HOME="$probe/home" CLAUDE_PLUGIN_DATA="$pdata" \
      bash "$probe/p.sh" <<<'{"hook_event_name":"SessionStart"}' 2>/dev/null )
}

msg=$(say)
[ -n "$msg" ] || { echo "ERROR: silent — a broken install would never be noticed" >&2; exit 1; }
if command -v jq >/dev/null 2>&1; then
  printf '%s' "$msg" | jq -e '.systemMessage' >/dev/null \
    || { echo "ERROR: not valid JSON: $msg" >&2; exit 1; }
else
  echo "  (jq absent — JSON validity not checked)"
fi

[ -z "$(say)" ] || { echo "ERROR: repeated immediately — hooks fire many times a turn" >&2; exit 1; }

awk '{print $1, $2-3700}' "$pdata/bin/.download-failing" >"$probe/m"
cp "$probe/m" "$pdata/bin/.download-failing"
[ -n "$(say)" ] || { echo "ERROR: never reminded again after an hour" >&2; exit 1; }

# A marker left by an earlier version must not silence the current one.
printf '0.1.20 %s\n' "$(date +%s)" >"$pdata/bin/.download-failing"
[ -n "$(say)" ] || { echo "ERROR: a stale marker from another version silenced it" >&2; exit 1; }

rm -rf "$probe" "$pdata"
echo "PASS: reported once, rate-limited, per version, valid JSON"

echo "== Concurrent cold-cache invocations all succeed =="
VERSION=$(sed -n 's/^VERSION="\(.*\)"/\1/p' "$REPO/claude/claude-on-event.sh")
CHECKSUMS_URL="https://github.com/dash0hq/dash0-agent-plugin/releases/download/v${VERSION}/checksums.txt"
CHECKSUMS=$(curl -fsSL "$CHECKSUMS_URL" 2>/dev/null || true)
if [ -z "$CHECKSUMS" ]; then
  # Expected on a version-bump PR, where the release is tagged only after merge.
  # A warning rather than skip_or_fail: this must not turn every bump PR red, and
  # the static contract above still ran.
  echo "SKIP: release v${VERSION} is not published yet — static contract still enforced"
  exit 0
fi
EXPECTED=$(printf '%s\n' "$CHECKSUMS" | awk -v a="claude-on-event-$(os_arch)" '$2 == a { print $1 }')
[ -n "$EXPECTED" ] || skip_or_fail "no checksum for claude-on-event-$(os_arch) in v${VERSION}"

DATA=$(mktemp -d)
export CLAUDE_PLUGIN_DATA="$DATA"
# Hermetic HOME and cwd, as above: an `enabled: false` in the caller's config
# exits the script before it downloads anything, and every assertion below would
# then be measuring a directory that was never created.
export HOME="$DATA/home"
mkdir -p "$HOME"
# Dead endpoint: the exported telemetry is irrelevant here, and the binary exits
# 0 when it can't reach a collector, so a nonzero exit means the bootstrap failed.
export DASH0_OTLP_URL="http://127.0.0.1:1"
# Staggered, not simultaneous — the damaging overlap is one process exec'ing
# while a later one truncates the same path, which a burst of identical starts
# mostly misses.
for i in $(seq 8); do
  ( cd "$DATA" \
    && echo '{"hook_event_name":"SessionStart","session_id":"contract","model":"opus"}' \
      | bash "$REPO/claude/claude-on-event.sh" >/dev/null 2>"$DATA/err.$i"
    echo "$?" >"$DATA/rc.$i" ) &
  sleep 0.35
done
wait

# Exit codes alone no longer signal anything: every bootstrap failure exits 0 by
# design now, so the original race — 48 of 48 invocations failing, each on a
# different checksum — would reach fail_open, exit 0, and pass this check. A
# successful run is also SILENT, so stderr is the signal that survived the
# flattening. Both are asserted.
bad=$(cat "$DATA"/rc.* | grep -vc '^0$' || true)
if [ "$bad" -ne 0 ]; then
  echo "ERROR: $bad of 8 concurrent invocations exited non-zero" >&2
  cat "$DATA"/err.* | sort -u | sed 's/^/  /' >&2
  exit 1
fi
noisy=$(cat "$DATA"/err.* 2>/dev/null | grep -c . || true)
if [ "$noisy" -ne 0 ]; then
  echo "ERROR: $noisy line(s) on stderr — a download failed and failed open" >&2
  cat "$DATA"/err.* | sort -u | sed 's/^/  /' >&2
  exit 1
fi
leftover=$(find "$DATA/bin" -name '*.tmp.*' | wc -l | tr -d ' ')
[ "$leftover" -eq 0 ] || { echo "ERROR: $leftover temp file(s) left in $DATA/bin" >&2; exit 1; }
BIN="$DATA/bin/on-event-${VERSION}-$(os_arch)"
if command -v sha256sum &>/dev/null; then ACTUAL=$(sha256sum "$BIN" | cut -d' ' -f1)
else ACTUAL=$(shasum -a 256 "$BIN" | cut -d' ' -f1); fi
[ "$ACTUAL" = "$EXPECTED" ] || { echo "ERROR: cached binary is corrupt (expected $EXPECTED, got $ACTUAL)" >&2; exit 1; }
rm -rf "$DATA"
echo "PASS: 8 concurrent cold-cache invocations converged on the published binary"
