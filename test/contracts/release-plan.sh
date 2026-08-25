#!/usr/bin/env bash
# Release planning contracts (runnable locally and in CI).
#
# scripts/release-plan.sh decides whether a Release run tags, what it tags, and
# whether the result claims GitHub's "Latest" pointer. It is the only branching
# in the release path and the workflow cannot be dispatched from a PR, so every
# branch is exercised here instead.
#
# Requires: jq, git, bash. No network.
set -euo pipefail
# shellcheck source=test/contracts/lib.sh
source "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/lib.sh"

PINNED=$(jq -r '.version' "$REPO/.claude-plugin/plugin.json")
fail=0

# plan_is <name> <expected key=value,…> -- VAR=value…
# Runs the planner with the given environment and compares the emitted lines.
plan_is() {
  local name="$1" want="$2"; shift 3
  local got
  if ! got=$(env -i PATH="$PATH" HOME="$HOME" "$@" "$REPO/scripts/release-plan.sh" 2>&1); then
    echo "  FAIL $name: planner exited non-zero"
    printf '    %s\n' "$got"
    fail=1
    return
  fi
  got=$(printf '%s' "$got" | tr '\n' ',')
  if [ "$got" != "$want" ]; then
    echo "  FAIL $name"
    echo "    want: $want"
    echo "    got:  ${got%,}"
    fail=1
    return
  fi
  echo "  ok   $name"
}

# plan_rejects <name> <substring of the expected error> -- VAR=value…
plan_rejects() {
  local name="$1" want="$2"; shift 3
  local got status=0
  got=$(env -i PATH="$PATH" HOME="$HOME" "$@" "$REPO/scripts/release-plan.sh" 2>&1) || status=$?
  if [ "$status" -eq 0 ]; then
    echo "  FAIL $name: planner accepted it"
    printf '    %s\n' "$got"
    fail=1
    return
  fi
  case "$got" in
    *"$want"*) echo "  ok   $name" ;;
    *) echo "  FAIL $name: wrong reason"; echo "    want ~ $want"; echo "    got:   $got"; fail=1 ;;
  esac
}

echo "== What each dispatch plans =="

plan_is "dry run builds nothing releasable" \
  "mode=dry-run,version=$PINNED,tag=,create_tag=false,latest=true" -- \
  EVENT=workflow_dispatch CHANNEL=stable DRY_RUN=true REF_NAME=some-branch

plan_is "dry run works off a branch too" \
  "mode=dry-run,version=$PINNED,tag=,create_tag=false,latest=true" -- \
  EVENT=workflow_dispatch CHANNEL=dev DRY_RUN=true REF_NAME=some-branch

# PINNED is supplied rather than read, so this also pins that a stable release
# takes its version from the manifest and not from anywhere else.
plan_is "stable from main releases the version main pins" \
  "mode=release,version=9.9.9,tag=v9.9.9,create_tag=true,latest=true" -- \
  EVENT=workflow_dispatch CHANNEL=stable DRY_RUN=false REF_NAME=main PINNED=9.9.9

plan_is "dev tags a prerelease off the branch and declines Latest" \
  "mode=release,version=$PINNED-dev.41,tag=v$PINNED-dev.41,create_tag=true,latest=false" -- \
  EVENT=workflow_dispatch CHANNEL=dev DRY_RUN=false REF_NAME=some-branch RUN_NUMBER=41

# Merging the bump PR IS the release trigger — `release: vX` on main has one
# meaning, and a second button to press only widens the window in which main
# advertises a version that has no release.
plan_is "a bump merged to main releases itself" \
  "mode=release,version=0.1.26,tag=v0.1.26,create_tag=true,latest=true" -- \
  EVENT=push REF_TYPE=branch REF_NAME=main PINNED=0.1.26 \
  EXISTING_RELEASES="v0.1.24 v0.1.25" TAG_STATE=absent

# Every other push to main reaches the planner too — `paths` and `tags` do not
# combine predictably on one trigger — so it must resolve to a no-op rather than
# re-releasing the current version.
plan_is "an ordinary push to main releases nothing" \
  "mode=none,version=0.1.25,tag=,create_tag=false,latest=true" -- \
  EVENT=push REF_TYPE=branch REF_NAME=main PINNED=0.1.25 \
  EXISTING_RELEASES="v0.1.24 v0.1.25"

plan_is "a pushed tag releases itself without re-tagging" \
  "mode=release,version=0.9.9,tag=v0.9.9,create_tag=false,latest=true" -- \
  EVENT=push REF_TYPE=tag REF_NAME=v0.9.9

plan_is "a pushed prerelease tag declines Latest" \
  "mode=release,version=0.9.9-rc.1,tag=v0.9.9-rc.1,create_tag=false,latest=false" -- \
  EVENT=push REF_TYPE=tag REF_NAME=v0.9.9-rc.1

echo "== What it refuses =="

# The window between merging the bump and publishing is the one hazard this flow
# accepts. Releasing a version main does not pin would make it permanent: main
# would keep advertising the old version while a newer one sat published.
plan_rejects "stable that main does not pin" "run Release prepare" -- \
  EVENT=workflow_dispatch CHANNEL=stable DRY_RUN=false REF_NAME=main \
  IN_VERSION=0.99.0 PINNED="$PINNED"

plan_rejects "stable from a branch" "must be dispatched from main" -- \
  EVENT=workflow_dispatch CHANNEL=stable DRY_RUN=false REF_NAME=some-branch

# TAG_STATE rather than the clone's own tags: actions/checkout and a local
# `git fetch` disagree about which tags are present, and a guard that quietly
# passes because the tag was simply not fetched is worse than no guard.
plan_rejects "a tag already on another commit" "another commit" -- \
  EVENT=workflow_dispatch CHANNEL=stable DRY_RUN=false REF_NAME=main \
  IN_VERSION=0.9.9 PINNED=0.9.9 TAG_STATE=elsewhere

plan_is "an untagged version tags normally" \
  "mode=release,version=0.9.9,tag=v0.9.9,create_tag=true,latest=true" -- \
  EVENT=workflow_dispatch CHANNEL=stable DRY_RUN=false REF_NAME=main \
  IN_VERSION=0.9.9 PINNED=0.9.9 TAG_STATE=absent

# The tag is pushed before the build, so a run that fails after tagging leaves it
# behind. Re-running must continue from it rather than refuse its own tag —
# otherwise that version can never be released without deleting the tag by hand.
plan_is "a re-run continues from the tag it already pushed" \
  "mode=release,version=0.9.9,tag=v0.9.9,create_tag=false,latest=true" -- \
  EVENT=workflow_dispatch CHANNEL=stable DRY_RUN=false REF_NAME=main \
  IN_VERSION=0.9.9 PINNED=0.9.9 TAG_STATE=here

# A reverted bump would otherwise try to re-release an older version and fail
# later, at the tag, with a message about the wrong thing.
plan_rejects "main having gone backwards" "gone backwards" -- \
  EVENT=push REF_TYPE=branch REF_NAME=main PINNED=0.1.24 \
  EXISTING_RELEASES="v0.1.24 v0.1.25"

# The post-publish steps run after Publish, so re-dispatching stable is the
# documented remedy for their failure — but `mode: replace` would delete and
# re-upload a live release's assets, reopening the window the draft removed.
plan_rejects "re-dispatching a version already published" "already published" -- \
  EVENT=workflow_dispatch CHANNEL=stable DRY_RUN=false REF_NAME=main \
  PINNED=0.1.25 EXISTING_RELEASES="v0.1.24 v0.1.25"

plan_is "re-dispatching a version tagged but not yet published" \
  "mode=release,version=0.1.26,tag=v0.1.26,create_tag=false,latest=true" -- \
  EVENT=workflow_dispatch CHANNEL=stable DRY_RUN=false REF_NAME=main \
  PINNED=0.1.26 EXISTING_RELEASES="v0.1.24 v0.1.25" TAG_STATE=here

plan_rejects "a malformed explicit version" "is not a version" -- \
  EVENT=workflow_dispatch CHANNEL=dev DRY_RUN=false REF_NAME=branch \
  IN_VERSION=v0.2.0 TAG_STATE=absent

echo "== What version comes next =="

# next_is <name> <expected> -- VAR=value…
next_is() {
  local name="$1" want="$2"; shift 3
  local got
  if ! got=$(env -i PATH="$PATH" HOME="$HOME" "$@" 2>&1); then
    echo "  FAIL $name: exited non-zero"; printf '    %s\n' "$got"; fail=1; return
  fi
  if [ "$got" != "$want" ]; then
    echo "  FAIL $name"; echo "    want: $want"; echo "    got:  $got"; fail=1; return
  fi
  echo "  ok   $name"
}

# next_rejects <name> <substring of the expected error> -- VAR=value…
next_rejects() {
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

VER="$REPO/scripts/version.sh"

next_is "patch"  "0.1.26" -- EXISTING_RELEASES="v0.1.24 v0.1.25" PINNED=0.1.25 "$VER" next patch
next_is "minor"  "0.2.0"  -- EXISTING_RELEASES="v0.1.24 v0.1.25" PINNED=0.1.25 "$VER" next minor
next_is "major"  "1.0.0"  -- EXISTING_RELEASES="v0.1.24 v0.1.25" PINNED=0.1.25 "$VER" next major

# Lexical order would pick v0.1.9 as newest and propose 0.1.10 — a version
# already published.
next_is "counts numerically, not lexically" "0.1.26" -- \
  EXISTING_RELEASES="v0.1.9 v0.1.10 v0.1.25" PINNED=0.1.25 "$VER" next patch

# A dev build cut from a branch must not become the base for the next stable.
next_is "ignores prereleases" "0.1.26" -- \
  EXISTING_RELEASES="v0.1.25 v0.2.0-dev.7 v0.9.0-rc.1" PINNED=0.1.25 "$VER" next patch

# Counting from PUBLISHED releases covers what a tag-based count cannot: v0.1.26
# tagged by a run that then failed. A tag count would agree with the manifests
# and propose 0.1.27, skipping it forever.
next_rejects "a version merged or tagged but never published" "must match" -- \
  EXISTING_RELEASES="v0.1.25" PINNED=0.1.26 "$VER" next patch

next_rejects "no stable release to count from" "no published stable release" -- \
  EXISTING_RELEASES="v0.1.0-dev.1" PINNED=0.1.25 "$VER" next patch

next_rejects "an unknown part" "expected patch, minor or major" -- \
  EXISTING_RELEASES="v0.1.25" PINNED=0.1.25 "$VER" next sideways

# $PINNED, never a literal: `set` reads and writes the real files, so a stale
# literal would stop being a no-op after the next release — and then this case
# would rewrite all ten pins in the working tree instead of being refused.
next_rejects "a bump to the version already pinned" "nothing to prepare" -- \
  "$VER" set "$PINNED"

[ "$fail" -eq 0 ] || exit 1
echo "PASS: the planner tags, names and flags every dispatch as documented, and picks the next version"
