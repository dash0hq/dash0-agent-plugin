#!/usr/bin/env bash
# Bootstrap download contracts (runnable locally and in CI):
#   - every *-on-event.sh downloads via a private temp and renames into place
#   - neither the scripts nor the Claude binary ends a hook non-zero
#   - an unrunnable cached binary neither errors nor re-downloads in a loop
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
# The contracts derive expected cache paths from the pinned VERSION in each
# script. A developer with DASH0_VERSION exported would otherwise see the
# bootstraps download something else and get a false failure.
unset DASH0_VERSION
# shellcheck source=test/contracts/lib.sh
source "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/lib.sh"

SCRIPTS=(claude/claude-on-event.sh cursor/cursor-on-event.sh
         codex/codex-on-event.sh copilot/copilot-on-event.sh)

echo "== Every bootstrap writes the binary only by rename =="
# Static, so it holds regardless of whether a race reproduces on this machine or
# this runner. Inside the download block the final path may appear only in the
# guard that opens it, the temp name derived from it, the closing rename, and a
# read-only `-x` test — any other use is a write to a path a concurrent process
# may already be exec'ing. This is the check that fails if someone restores
# `-o "$BINARY"`.
#
# The `-x` test is allowed because Windows refuses to rename over a running .exe,
# so a bootstrap that loses that race has to ask whether the winner's file is
# already in place before it reports a failure. A test cannot damage the file the
# way a write or a redirect can.
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
    | grep -vE '\[ ! -x "\$BINARY" \]|\[ -x "\$BINARY" \]|TMP="\$BINARY|mv -f "\$TMP" "\$BINARY"' || true)
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
# `:?` expansion or a failing exec — which is how two of these shipped. A
# read-only data directory poisons the first thing every bootstrap does.
#
# HOME and cwd are both thrown away: PROJECT_SETTINGS is a relative path, so a
# config with `enabled: false` in either place exits every script before it
# reaches the data directory, and this loop would print ok having run nothing.
fail=0
ro=$(mktemp -d); chmod a-w "$ro"
if touch "$ro/probe" 2>/dev/null; then
  rm -f "$ro/probe"
  echo "  SKIP: cannot make a directory unwritable here"
else
  for s in "${SCRIPTS[@]}"; do
    status=0
    ( cd "$ro" && env -i PATH="$PATH" HOME="$ro/nohome" \
        CLAUDE_PLUGIN_DATA="$ro/d" DASH0_PLUGIN_DATA="$ro/d" COPILOT_PLUGIN_DATA="$ro/d" \
        bash "$REPO/$s" someEvent <<<'{"hook_event_name":"SessionStart"}' >/dev/null 2>&1 ) \
      || status=$?
    if [ "$status" -ne 0 ]; then
      echo "  FAIL $s: exited $status when its data directory could not be created"; fail=1
    else
      echo "  ok $s"
    fi
  done
fi
chmod u+w "$ro"; rm -rf "$ro"
[ "$fail" -eq 0 ] || exit 1
echo "PASS: all ${#SCRIPTS[@]} bootstraps fail open"

echo "== DASH0_VERSION cannot retarget the download or escape BIN_DIR =="
# It reaches a URL and a filesystem path. curl squashes `..`, so an unvalidated
# value points BASE_URL — and checksums.txt with it — at another repository,
# which makes verification pass against the attacker's own manifest. The hook
# runs inside an agent session, so a project .envrc is a plausible source.
fail=0
vdata=$(mktemp -d)
# Hermetic, like the probes below: an `enabled: false` in the caller's HOME or
# cwd exits the script before it reaches the regex, and every assertion here
# would then pass having tested nothing.
export HOME="$vdata/home"; mkdir -p "$HOME"
# The cwd is thrown away per invocation, in a subshell. A bare `cd` here leaks
# into the contracts below, which `go build` from the repo root — they then fail
# to build and skip_or_fail turns that into a red CI run.

# The pinned default, which a rejected override must fall back to.
pinned=$(sed -n 's/^VERSION="\(.*\)"/\1/p' "$REPO/claude/claude-on-event.sh")
# Whether that version is downloadable decides how much can be asserted. On a
# bump PR main pins a release that does not exist yet, and "the hook did not
# cache anything" then means "there was nothing to cache", not "the hook stopped".
CHECKSUMS_URL="https://github.com/dash0hq/dash0-agent-plugin/releases/download/v${pinned}/checksums.txt"
published=0
curl -fsSL -o /dev/null "$CHECKSUMS_URL" 2>/dev/null && published=1
[ "$published" -eq 1 ] \
  || echo "  note: v$pinned is not published — asserting refusal only, not the fallback"

for bad in '../../../../attacker/repo/releases/download/v9' '../../etc' 'v0.1.25' '0.1.25; id'; do
  bdata=$(mktemp -d)
  out=$(cd "$vdata" && DASH0_VERSION="$bad" CLAUDE_PLUGIN_DATA="$bdata" \
        bash "$REPO/claude/claude-on-event.sh" <<<'{}' 2>&1) || true
  case "$out" in
    *ignoring*) ;;
    *) echo "  FAIL accepted: $bad"; fail=1 ;;
  esac
  # Behaviour, not wording. Rejecting the value must leave the hook running on
  # the pinned version — the message says "ignoring", and for a long time the
  # code exited instead, turning a typo like v0.1.25 into a session with no
  # telemetry at all. Asserting only on the message could not tell the two apart.
  if [ "$published" -eq 1 ]; then
    cached=$(find "$bdata/bin" -type f -name "*-${pinned}-*" 2>/dev/null | head -1) || true
    [ -n "$cached" ] \
      || { echo "  FAIL '$bad' stopped the hook instead of falling back to $pinned"; fail=1; }
  fi
  # And the rejected value must reach neither a path nor a URL.
  find "$bdata" -path '*attacker*' -o -name "*${bad##*/}*" 2>/dev/null | grep -q . \
    && { echo "  FAIL '$bad' reached the filesystem"; fail=1; }
  rm -rf "$bdata"
done
for ok in "$pinned" "${pinned}-dev.7"; do
  # `|| true`, matching the download probe below: a valid DASH0_VERSION means
  # the script runs on into settings and the download, so it keeps writing after
  # head -1 has exited. Under `set -o pipefail` that is a 141 which ends the
  # whole suite with no diagnostic — and how much it writes depends on the
  # machine, so it would reproduce on a laptop and not in CI.
  out=$(cd "$vdata" && DASH0_VERSION="$ok" CLAUDE_PLUGIN_DATA="$vdata" \
        bash "$REPO/claude/claude-on-event.sh" <<<'{}' 2>&1 | head -1) || true
  case "$out" in
    *ignoring*) echo "  FAIL rejected a real version: $ok"; fail=1 ;;
  esac
done
escaped=$(find "$vdata/.." -maxdepth 1 -name 'attacker' 2>/dev/null | wc -l | tr -d ' ')
[ "$escaped" -eq 0 ] || { echo "  FAIL wrote outside BIN_DIR"; fail=1; }
rm -rf "$vdata"
[ "$fail" -eq 0 ] || exit 1
echo "PASS: traversal and injection refused, real versions accepted"

echo "== The binary itself never ends a hook non-zero =="
# The check above poisons the *shell's* environment, so it never gets as far as
# exec'ing a real binary — which is how claude-on-event kept an os.Exit(1) that
# cursor, codex and copilot do not have. pipeline.go already logs a failed span
# export rather than raising it; main.go was the outlier.
bin=$(mktemp -d)/on-event
if ! go build -o "$bin" "$REPO/cmd/claude-on-event" 2>/dev/null; then
  echo "SKIP: could not build the binary"
else
  fail=0
  probe() {
    local name="$1" payload="$2"; shift 2
    local status=0
    printf '%s' "$payload" | env -i PATH="$PATH" "$@" "$bin" >/dev/null 2>&1 || status=$?
    if [ "$status" -ne 0 ]; then
      echo "  FAIL $name: exited $status"; fail=1
    else
      echo "  ok   $name"
    fi
  }
  nowhere=$(mktemp -d); chmod a-w "$nowhere"
  probe "no CLAUDE_PLUGIN_DATA"     '{"hook_event_name":"SessionStart"}'
  probe "malformed payload"         'not json'          CLAUDE_PLUGIN_DATA="$(mktemp -d)"
  probe "null payload"              'null'              CLAUDE_PLUGIN_DATA="$(mktemp -d)"
  probe "unwritable session dir"    '{"hook_event_name":"SessionStart","session_id":"x"}' \
                                    CLAUDE_PLUGIN_DATA="$nowhere/d"
  chmod u+w "$nowhere"; rm -rf "$nowhere"
  [ "$fail" -eq 0 ] || exit 1
  echo "PASS: the binary logs and exits 0 on every telemetry failure"
fi

echo "== A cached binary that will not run does not loop =="
# Deleting it would make the next hook re-download the whole asset, fail to exec
# it again, and repeat — a multi-MB fetch per tool call. It is kept instead.
probe=$(mktemp -d); pdata=$(mktemp -d)
# Hermetic HOME and cwd, as everywhere else here: with the caller's, a config
# carrying `enabled: false` exits before the download and this would skip
# rather than test.
( cd "$probe" && env -i PATH="$PATH" HOME="$probe/home" \
    CLAUDE_PLUGIN_DATA="$pdata" DASH0_OTLP_URL=http://127.0.0.1:1 \
    bash "$REPO/claude/claude-on-event.sh" >/dev/null 2>&1 \
    <<<'{"hook_event_name":"SessionStart","session_id":"contract"}' ) || true
# `|| true`: the directory does not exist if the download never ran, and a
# failing find would end the whole suite under `set -e`.
cached=$(find "$pdata/bin" -name 'on-event-*' -type f 2>/dev/null | head -1) || true
# A genuinely foreign binary, built for the other OS. A hand-made fake header
# does not work: the kernel returns ENOEXEC for it and bash falls back to
# interpreting the file as a shell script, which is a different failure.
other=linux; [ "$(uname -s)" = Linux ] && other=darwin
if [ -z "$cached" ]; then
  echo "SKIP: could not prime a cached binary"
elif ! GOOS="$other" GOARCH=amd64 go build -o "$cached" "$REPO/cmd/claude-on-event" 2>/dev/null; then
  echo "SKIP: could not cross-build a $other binary"
else
  chmod +x "$cached"
  status=0
  ( cd "$probe" && env -i PATH="$PATH" HOME="$probe/home" CLAUDE_PLUGIN_DATA="$pdata" \
      bash "$REPO/claude/claude-on-event.sh" \
      <<<'{"hook_event_name":"SessionStart"}' >/dev/null 2>&1 ) || status=$?
  [ "$status" -eq 0 ] \
    || { echo "ERROR: an unrunnable cached binary exited $status" >&2; exit 1; }
  [ -f "$cached" ] \
    || { echo "ERROR: the bad binary was deleted — the next hook re-downloads it" >&2; exit 1; }
  echo "PASS: exits 0 and leaves the cache alone"
fi
rm -rf "$probe" "$pdata"

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
# Hermetic HOME, as above: an `enabled: false` config exits before any download
# and every assertion below would measure a directory nothing created.
export HOME="$DATA/home"; mkdir -p "$HOME"
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

# Exit codes no longer signal anything: every failure exits 0 by design now, so
# the original race — 48 of 48 invocations failing, each on a different checksum
# — would reach fail_open and pass this check. Measured against the pre-atomic
# script: 6 non-zero exits AND 6 stderr lines. A successful run is silent, so
# that is the signal that survived. Both are asserted.
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
