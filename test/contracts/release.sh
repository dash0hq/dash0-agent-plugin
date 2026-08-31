#!/usr/bin/env bash
# Release planning contracts (runnable locally and in CI).
#
# scripts/release-plan.sh decides what a Release run tags, whether it rewrites
# the version on main, and whether the result claims GitHub's "Latest" pointer.
# It is the only branching in the release path, and the workflow cannot be
# dispatched from a PR, so every branch is exercised here instead.
#
# Requires: jq, git, bash. No network — EXISTING_RELEASES and TAG_STATE stand in
# for the two things the planner would otherwise ask GitHub and git.
set -euo pipefail
# shellcheck source=test/contracts/lib.sh
source "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/lib.sh"

PLAN="$REPO/scripts/release-plan.sh"
VER="$REPO/scripts/version.sh"
fail=0

# expect <name> <expected key=value,…> -- VAR=value…
expect() {
  local name="$1" want="$2"; shift 3
  local got
  if ! got=$(env -i PATH="$PATH" HOME="$HOME" "$@" 2>&1); then
    echo "  FAIL $name: exited non-zero"; printf '    %s\n' "$got"; fail=1; return
  fi
  got=$(printf '%s' "$got" | tr '\n' ',')
  if [ "$got" != "$want" ]; then
    echo "  FAIL $name"; echo "    want: $want"; echo "    got:  $got"; fail=1; return
  fi
  echo "  ok   $name"
}

# refuse <name> <substring of the expected error> -- VAR=value…
refuse() {
  local name="$1" want="$2"; shift 3
  local got status=0
  got=$(env -i PATH="$PATH" HOME="$HOME" "$@" 2>&1) || status=$?
  if [ "$status" -eq 0 ]; then
    echo "  FAIL $name: accepted it"; printf '    %s\n' "$got"; fail=1; return
  fi
  case "$got" in
    *"$want"*) echo "  ok   $name" ;;
    *) echo "  FAIL $name: wrong reason"; echo "    want ~ $want"; echo "    got:   $got"; fail=1 ;;
  esac
}

echo "== What each dispatch plans =="

expect "dry run builds nothing releasable" \
  "mode=dry-run,version=0.1.25,tag=,bump_needed=false,latest=true" -- \
  DRY_RUN=true REF_NAME=some-branch PINNED=0.1.25 "$PLAN"

expect "a release bumps main and tags the result" \
  "mode=release,version=0.1.26,tag=v0.1.26,bump_needed=true,latest=true" -- \
  REF_NAME=main PINNED=0.1.25 BUMP=patch \
  EXISTING_RELEASES="v0.1.24 v0.1.25" TAG_STATE=absent "$PLAN"

expect "an explicit version overrides the bump" \
  "mode=release,version=0.2.0,tag=v0.2.0,bump_needed=true,latest=true" -- \
  REF_NAME=main PINNED=0.1.25 IN_VERSION=0.2.0 \
  EXISTING_RELEASES="v0.1.25" TAG_STATE=absent "$PLAN"

# The recovery path. A previous run pushed the bump and then failed, so main
# already names a version that was never released. Bumping again would skip it
# permanently; this finishes it instead.
expect "a bump already on main is finished, not repeated" \
  "mode=release,version=0.1.26,tag=v0.1.26,bump_needed=false,latest=true" -- \
  REF_NAME=main PINNED=0.1.26 \
  EXISTING_RELEASES="v0.1.24 v0.1.25" TAG_STATE=absent "$PLAN"

# The tag is created before the build, so a run that fails after it leaves one
# behind. Re-running must continue from it rather than refuse its own tag.
expect "a re-run continues from the tag it already made" \
  "mode=release,version=0.1.26,tag=v0.1.26,bump_needed=false,latest=true" -- \
  REF_NAME=main PINNED=0.1.26 \
  EXISTING_RELEASES="v0.1.25" TAG_STATE=here "$PLAN"

echo "== What it refuses =="

# main is what `claude plugin install` reads, so a stable release cut anywhere
# else would publish a version no install asks for.
refuse "a release from a branch" "must be dispatched from main" -- \
  REF_NAME=some-branch PINNED=0.1.25 "$PLAN"

refuse "a version that would not move forward" "already published" -- \
  REF_NAME=main PINNED=0.1.25 IN_VERSION=0.1.20 \
  EXISTING_RELEASES="v0.1.25" TAG_STATE=absent "$PLAN"

# A stable release writes its version into main, and the marketplace reads main
# with no ref — so a prerelease there would be pinned by every install, while
# GitHub's Latest pointer stayed on the last stable and said nothing.
refuse "a malformed explicit version" "is not a version" -- \
  REF_NAME=main IN_VERSION=v0.2.0 PINNED=0.1.25 "$PLAN"

refuse "a prerelease version" "only cuts stable releases" -- \
  REF_NAME=main PINNED=0.1.25 IN_VERSION=0.2.0-dev.9 \
  EXISTING_RELEASES="v0.1.25" TAG_STATE=absent "$PLAN"

refuse "a release cut from a prerelease pin" "main pins the prerelease" -- \
  REF_NAME=main PINNED=0.2.0-dev.9 \
  EXISTING_RELEASES="v0.1.25" TAG_STATE=absent "$PLAN"

# Silently publishing 0.1.26 when the operator asked for 0.2.0 is worse than
# refusing: the plan block is the only place the substitution would show.
refuse "an explicit version while main carries an unreleased bump" "finish that release" -- \
  REF_NAME=main PINNED=0.1.26 IN_VERSION=0.2.0 \
  EXISTING_RELEASES="v0.1.25" TAG_STATE=absent "$PLAN"

refuse "a tag already on another commit" "another commit" -- \
  REF_NAME=main PINNED=0.1.25 \
  EXISTING_RELEASES="v0.1.25" TAG_STATE=elsewhere "$PLAN"

echo "== What version comes next =="

expect "patch" "0.1.26" -- EXISTING_RELEASES="v0.1.24 v0.1.25" PINNED=0.1.25 "$VER" next patch
expect "minor" "0.2.0"  -- EXISTING_RELEASES="v0.1.24 v0.1.25" PINNED=0.1.25 "$VER" next minor
expect "major" "1.0.0"  -- EXISTING_RELEASES="v0.1.24 v0.1.25" PINNED=0.1.25 "$VER" next major

# Lexical order would pick v0.1.9 and propose 0.1.10 — already published.
expect "counts numerically, not lexically" "0.1.26" -- \
  EXISTING_RELEASES="v0.1.9 v0.1.10 v0.1.25" PINNED=0.1.25 "$VER" next patch

# A dev cut must not become the base for the next stable.
expect "ignores prereleases" "0.1.26" -- \
  EXISTING_RELEASES="v0.1.25 v0.2.0-dev.7" PINNED=0.1.25 "$VER" next patch

# Counted from published releases, so this covers what a tag-based count cannot:
# a version tagged by a run that then failed.
refuse "counting past a version that was never published" "must match" -- \
  EXISTING_RELEASES="v0.1.25" PINNED=0.1.26 "$VER" next patch

refuse "no published release to count from" "no published stable release" -- \
  EXISTING_RELEASES="v0.1.0-dev.1" PINNED=0.1.25 "$VER" next patch

refuse "an unknown part" "expected patch, minor or major" -- \
  EXISTING_RELEASES="v0.1.25" PINNED=0.1.25 "$VER" next sideways

# $PINNED, never a literal: `set` reads and writes the real files, so a stale
# literal would stop being a no-op after the next release and rewrite all ten
# pins in the working tree instead of being refused.
refuse "a bump to the version already pinned" "nothing to prepare" -- \
  "$VER" set "$(jq -r '.version' "$REPO/.claude-plugin/plugin.json")"

echo "== What a bump actually writes =="

# `set` is the one command that edits the repo, and every check above only
# proves it refuses things. This proves it succeeds: the release job runs it on
# main and commits whatever it produced, so a sed that quietly stops matching
# would ship a release whose bootstraps ask for a version that was never tagged.
# It runs against a copy — the script cds to its own parent, so the copy is the
# only way to exercise the real writes without dirtying the working tree.
sandbox=$(mktemp -d)
trap 'rm -rf "$sandbox"' EXIT
( cd "$REPO" && tar cf - scripts/version.sh \
    .claude-plugin/plugin.json .cursor-plugin/plugin.json .codex-plugin/plugin.json \
    copilot/plugin.json .github/plugin/marketplace.json \
    claude/claude-on-event.sh cursor/cursor-on-event.sh \
    codex/codex-on-event.sh copilot/copilot-on-event.sh ) | tar xf - -C "$sandbox"

if out=$("$sandbox/scripts/version.sh" set 9.9.9 2>&1); then
  case "$out" in
    *"all 10 pins agree on 9.9.9"*) echo "  ok   a bump rewrites all ten pins" ;;
    *) echo "  FAIL a bump rewrites all ten pins"; printf '    %s\n' "$out"; fail=1 ;;
  esac
  # Named individually, because `check` compares the pins to each other: were a
  # bootstrap's VERSION= line to stop matching, all ten would still agree — on
  # the old version — and check would pass.
  for f in claude/claude-on-event.sh cursor/cursor-on-event.sh \
           codex/codex-on-event.sh copilot/copilot-on-event.sh; do
    grep -q '^VERSION="9.9.9"$' "$sandbox/$f" \
      || { echo "  FAIL $f still pins $(grep -m1 '^VERSION=' "$sandbox/$f")"; fail=1; }
  done
  [ "$(jq -r '.metadata.version' "$sandbox/.github/plugin/marketplace.json")" = "9.9.9" ] \
    || { echo "  FAIL marketplace.json metadata.version was not rewritten"; fail=1; }
else
  echo "  FAIL a bump rewrites all ten pins: exited non-zero"; printf '    %s\n' "$out"; fail=1
fi

[ "$fail" -eq 0 ] || exit 1
echo "PASS: the planner tags, bumps and flags every dispatch as documented"
