#!/usr/bin/env bash
# SPDX-FileCopyrightText: Copyright 2026 Dash0 Inc.
# SPDX-License-Identifier: Apache-2.0
#
# Assert that a published release carries a binary for every platform each
# bootstrap can ask for, at the exact public URL the bootstrap builds.
#
# Asset names are read out of the scripts, not hardcoded: hardcoding them once
# let a rename through, where the bootstrap asked for claude-on-event-<os>-<arch>
# while this probed on-event-<os>-<arch> and passed. A script may list several
# candidates and try them in order, so the requirement is that at least ONE
# resolves per platform.
#
# Usage:
#   scripts/verify-release-assets.sh <version>            # missing release => warn
#   scripts/verify-release-assets.sh --strict <version>   # missing release => fail
#
# Non-strict is for CI on a bump PR, where the tag comes only after the merge.
# Strict is for the release workflow, where a missing release IS the failure.

set -euo pipefail

STRICT=0
if [ "${1:-}" = "--strict" ]; then STRICT=1; shift; fi
VERSION="${1:-}"
[ -n "$VERSION" ] || { echo "usage: $0 [--strict] <version>" >&2; exit 2; }

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"

die() { echo "::error::$1" >&2; exit 1; }

# The fork's own releases when this runs in a fork's CI, so --strict does not
# pass by validating what upstream published.
BASE="https://github.com/${GITHUB_REPOSITORY:-dash0hq/dash0-agent-plugin}/releases/download/v${VERSION}"
PLATFORMS=(linux-amd64 linux-arm64 darwin-amd64 darwin-arm64
           windows-amd64 windows-arm64)
BOOTSTRAPS=(claude/claude-on-event.sh cursor/cursor-on-event.sh
            codex/codex-on-event.sh copilot/copilot-on-event.sh)
# The Windows bootstraps ask for windows assets only, and always with .exe.
PS_BOOTSTRAPS=(cursor/cursor-on-event.ps1 codex/codex-on-event.ps1
               copilot/copilot-on-event.ps1)

# `|| true` because a DNS or connection failure exits curl 6/7, which under
# `set -e` would abort at the assignment below — skipping the classification that
# distinguishes "not published" from "could not ask". curl still writes 000.
status() { curl -sI -o /dev/null -w '%{http_code}' -L "$1" || true; }

# 404 is a real miss and lets the next candidate name be tried. Anything else is
# a failure to ask, which must not be reported as a missing asset — a strict
# release run would otherwise fail claiming a binary is absent when it is there.
resolves() {
  local s
  s=$(status "$1")
  case "$s" in
    200) return 0 ;;
    404) return 1 ;;
    *)   die "could not reach $1 (HTTP $s)" ;;
  esac
}

CHECKSUMS_STATUS=$(status "${BASE}/checksums.txt")
if [ "$CHECKSUMS_STATUS" != "200" ]; then
  [ "$STRICT" -eq 0 ] \
    || die "release v${VERSION} has no checksums.txt at ${BASE} (HTTP $CHECKSUMS_STATUS)"
  # Only a 404 means "not published yet". A 5xx, a rate limit or a connection
  # failure (000) must not read as a clean skip — that is how a genuinely broken
  # asset set passes CI behind one flaky request.
  [ "$CHECKSUMS_STATUS" = "404" ] \
    || die "could not reach ${BASE}/checksums.txt (HTTP $CHECKSUMS_STATUS)"
  # Expected on a version-bump PR. Validated for real by the release workflow.
  echo "::warning::release v${VERSION} is not published yet — asset check skipped"
  exit 0
fi

# probe <script> <platform> <candidate names…> — at least one must resolve. More
# than one is how a published asset can be renamed without coordinating the
# release: the bootstrap lists the new name first and falls back to the old.
# Windows binaries arrived after v0.1.25, so on a PR pinning an older release
# they are genuinely absent and every Windows probe would fail — with nothing to
# fix. Asking the release itself, rather than hardcoding a cutover version, means
# this retires on its own: the first release carrying them checks them for real.
# Never skipped under --strict, where the release was just built and must be
# complete.
WINDOWS=1
if [ "$STRICT" -eq 0 ] && ! curl -sS -L "${BASE}/checksums.txt" | grep -q -- '-windows-'; then
  echo "::warning::v${VERSION} predates the Windows binaries — Windows assets not checked"
  WINDOWS=0
fi

fail=0
probe() {
  local script="$1" platform="$2" name found=""
  shift 2
  for name in "$@"; do
    # ${EXE} is empty off Windows and ".exe" on it. Stripped by the parser and
    # re-applied here, so one candidate list covers every platform.
    [ "${platform#windows-}" = "$platform" ] || name="${name}.exe"
    if resolves "${BASE}/${name}-${platform}"; then found="${name}-${platform}"; break; fi
  done
  if [ -n "$found" ]; then
    echo "  ok  $script -> $found"
  else
    echo "::error::$script can obtain no binary for $platform from v${VERSION} — tried: $*" >&2
    fail=1
  fi
}

for script in "${BOOTSTRAPS[@]}"; do
  # Every "<name>-${OS}-${ARCH}${EXE}" literal, i.e. the names it will try.
  # Checked non-empty: a parser matching nothing would report success having
  # checked nothing — which is how a rename once shipped, the script asking for
  # claude-on-event-<os>-<arch> while the check probed on-event-<os>-<arch>.
  # shellcheck disable=SC2016  # matching literal ${OS}/${ARCH} in the script text
  candidates=$(grep -oE '"[a-zA-Z${}_-]+-\$\{OS\}-\$\{ARCH\}(\$\{EXE\})?"' "$script" \
    | tr -d '"' | sed 's/-\${OS}-\${ARCH}\(\${EXE}\)\{0,1\}$//' | sort -u)
  # ${AGENT} resolved with bash, not sed: escaping a literal $ on the left while
  # expanding a variable on the right silently produces a pattern matching
  # nothing, and every candidate stays unexpanded.
  agent=$(sed -n 's/^AGENT="\(.*\)"/\1/p' "$script")
  # shellcheck disable=SC2016  # '${AGENT}' is the literal being replaced
  candidates=${candidates//'${AGENT}'/$agent}
  # shellcheck disable=SC2016  # '${' is the literal an unexpanded name leaves
  case "$candidates" in
    ''|*'${'*)
      echo "::error::could not read the asset names from $script — update this parser" >&2
      fail=1; continue ;;
  esac
  for platform in "${PLATFORMS[@]}"; do
    [ "$WINDOWS" -eq 1 ] || [ "${platform#windows-}" = "$platform" ] || continue
    # shellcheck disable=SC2086  # one candidate per word, deliberately split
    probe "$script" "$platform" $candidates
  done
done

for script in "${PS_BOOTSTRAPS[@]}"; do
  [ "$WINDOWS" -eq 1 ] || continue
  agent=$(sed -n "s/^\$Agent = '\(.*\)'/\1/p" "$script")
  [ -n "$agent" ] || { echo "::error::could not read \$Agent from $script" >&2; fail=1; continue; }
  # These name windows explicitly, so only the arch varies. .exe comes from
  # probe, which appends it for every windows platform.
  probe "$script" windows-amd64 "${agent}-on-event"
  probe "$script" windows-arm64 "${agent}-on-event"
done

[ "$fail" -eq 0 ] || exit 1
N=${#PLATFORMS[@]}
[ "$WINDOWS" -eq 1 ] || N=$((N - 2))
echo "PASS: every bootstrap resolves a binary for all $N platforms in v${VERSION}"
