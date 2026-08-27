#!/usr/bin/env bash
# Bootstrap download contracts (runnable locally and in CI):
#   - every *-on-event.sh downloads via a private temp and renames into place
#   - none of them ends a hook with a non-zero exit
#   - a missing release disturbs nothing and installs nothing
#   - concurrent invocations against a cold cache all succeed and converge
#
# Hooks run concurrently — parallel tool calls each fire their own, and every
# session on the machine shares one plugin data directory — so the first run
# after a version bump has N processes finding no binary at once. Writing the
# final path directly made them interleave into one file: measured against
# v0.1.25, 48 of 48 staggered invocations failed, each computing a different
# checksum, plus one process's cleanup deleting the file another was chmod'ing.
#
# Requires: curl, bash, sha256sum or shasum. Network for the second contract.
set -euo pipefail
# shellcheck source=test/contracts/lib.sh
source "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/lib.sh"

SCRIPTS=(claude/claude-on-event.sh cursor/cursor-on-event.sh
         codex/codex-on-event.sh copilot/copilot-on-event.sh)

echo "== Every bootstrap writes the binary only by rename =="
# Static, so it holds regardless of whether a race reproduces on this machine or
# this runner. Inside the download block the final path may appear only in the
# guard that opens it, the temp name derived from it, and the closing rename —
# any other use is a write to a path a concurrent process may already be
# exec'ing. This is the check that fails if someone restores `-o "$BINARY"`.
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
# Every one of these paths means "we can't safely run the binary", and none of
# them is worth interrupting a session for. The commonest is transient: for
# about a minute after a release bump lands on main, main names a version whose
# binaries are still building. Claude used to exit 1 there and surface a hook
# error per event; the other three already exited 0.
fail=0
for s in "${SCRIPTS[@]}"; do
  bad=$(sed 's/#.*//' "$REPO/$s" | grep -nE '^[[:space:]]*exit [1-9]' || true)
  if [ -n "$bad" ]; then
    echo "  FAIL $s: non-zero exit before exec:"
    printf '    %s\n' "$bad"
    fail=1
  else
    echo "  ok $s"
  fi
done
[ "$fail" -eq 0 ] || exit 1
echo "PASS: all ${#SCRIPTS[@]} bootstraps fail open"

echo "== A missing release does not disturb the session, and installs nothing =="
# Points the Claude bootstrap at a version that will never exist. Exercises the
# path a real install hits inside the release window.
missing=$(mktemp -d)
sed 's/^VERSION="[^"]*"/VERSION="9.9.9"/' "$REPO/claude/claude-on-event.sh" >"$missing/probe.sh"
data=$(mktemp -d)
status=0
CLAUDE_PLUGIN_DATA="$data" bash "$missing/probe.sh" \
  <<<'{"hook_event_name":"SessionStart","session_id":"contract"}' >/dev/null 2>&1 || status=$?
if [ "$status" -ne 0 ]; then
  echo "ERROR: a missing release exited $status — a hook error the user cannot act on" >&2
  exit 1
fi
left=$(find "$data" -type f 2>/dev/null | wc -l | tr -d ' ')
[ "$left" -eq 0 ] || { echo "ERROR: $left file(s) left behind after a failed download" >&2; exit 1; }
rm -rf "$missing" "$data"
echo "PASS: a missing release exits 0 and leaves nothing behind"

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
# Dead endpoint: the exported telemetry is irrelevant here, and the binary exits
# 0 when it can't reach a collector, so a nonzero exit means the bootstrap failed.
export DASH0_OTLP_URL="http://127.0.0.1:1"
# Staggered, not simultaneous — the damaging overlap is one process exec'ing
# while a later one truncates the same path, which a burst of identical starts
# mostly misses.
for i in $(seq 8); do
  ( echo '{"hook_event_name":"SessionStart","session_id":"contract","model":"opus"}' \
      | bash "$REPO/claude/claude-on-event.sh" >/dev/null 2>"$DATA/err.$i"
    echo "$?" >"$DATA/rc.$i" ) &
  sleep 0.35
done
wait

bad=$(cat "$DATA"/rc.* | grep -vc '^0$' || true)
if [ "$bad" -ne 0 ]; then
  echo "ERROR: $bad of 8 concurrent invocations failed" >&2
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
