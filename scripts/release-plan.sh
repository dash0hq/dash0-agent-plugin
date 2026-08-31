#!/usr/bin/env bash
# SPDX-FileCopyrightText: Copyright 2026 Dash0 Inc.
# SPDX-License-Identifier: Apache-2.0
#
# Decide what a Release run does, and refuse the combinations that would publish
# something wrong. Prints `key=value` for $GITHUB_OUTPUT; ::error:: and exit 1
# when a guard trips.
#
# A script, not an inline `run:` block, so it can be shellchecked and contract
# tested — the workflow cannot be dispatched from a PR, so this is the only
# coverage it gets before merge. See test/contracts/release.sh.
#
# Reads from the environment, as GitHub sets them:
#   DRY_RUN     inputs.dry_run     — "true" | "false"
#   BUMP        inputs.bump        — patch | minor | major
#   IN_VERSION  inputs.version     — optional exact version
#   REF_NAME    github.ref_name    — the branch dispatched from
#   RUN_NUMBER  github.run_number  — the dev build counter
#   PINNED      the version in .claude-plugin/plugin.json (defaults to reading it)

set -euo pipefail

cd "$(dirname "${BASH_SOURCE[0]}")/.."

DRY_RUN="${DRY_RUN:-false}"
BUMP="${BUMP:-patch}"
IN_VERSION="${IN_VERSION:-}"
REF_NAME="${REF_NAME:-}"
RUN_NUMBER="${RUN_NUMBER:-0}"
PINNED="${PINNED:-$(jq -r '.version' .claude-plugin/plugin.json)}"

die() { echo "::error::$1" >&2; exit 1; }

# absent | here | elsewhere. TAG_STATE stands in for git so the contract test
# does not depend on which tags a clone happens to hold.
tag_state() {
  if [ -n "${TAG_STATE:-}" ]; then printf '%s\n' "$TAG_STATE"; return; fi
  local at
  at=$(git rev-parse -q --verify "refs/tags/$1^{commit}" 2>/dev/null) || { echo absent; return; }
  if [ "$at" = "$(git rev-parse HEAD)" ]; then echo here; else echo elsewhere; fi
}

# Higher of two versions, comparing each component numerically. Not `sort -V`
# (BSD and GNU disagree) and not lexically, which ranks 0.1.9 above 0.1.25.
higher() {
  printf '%s\n%s\n' "$1" "$2" | sort -t. -k1,1n -k2,2n -k3,3n | tail -n1
}

if [ -n "$IN_VERSION" ] && ! [[ "$IN_VERSION" =~ ^[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.]+)?$ ]]; then
  die "'$IN_VERSION' is not a version (expected 0.2.0; no leading v)"
fi

if [ "$DRY_RUN" = "true" ]; then
  MODE=dry-run; VERSION="${IN_VERSION:-$PINNED}"; TAG=""; BUMP_NEEDED=false

else
  MODE=release
  [ "$REF_NAME" = "main" ] \
    || die "releases must be dispatched from main (got '$REF_NAME')"

  # A release writes its version into main, and the Claude marketplace lists this
  # repo with no ref — so every install would then pin whatever this says.
  # Prereleases have no route here at all: the dev channel that produced them is
  # not in this workflow, because gating the App credential by branch and cutting
  # from a feature branch are mutually exclusive without splitting the job graph.
  case "$IN_VERSION" in
    *-*) die "'$IN_VERSION' is a prerelease — this workflow only cuts stable releases from main" ;;
  esac
  case "$PINNED" in
    *-*) die "main pins the prerelease $PINNED — a stable release cannot be cut from it" ;;
  esac

  # Both sides are plain X.Y.Z from here: PUBLISHED comes from version.sh latest,
  # which excludes prereleases, and the two cases above rule them out on the
  # other. `higher` compares numerically per component and would otherwise rank
  # 0.2.0-dev.1 above 0.2.0.
  PUBLISHED=$(./scripts/version.sh latest)
  if [ "$PINNED" != "$PUBLISHED" ] && [ "$(higher "$PINNED" "$PUBLISHED")" = "$PINNED" ]; then
    # main already carries a bump that was never released — an earlier run pushed
    # it and then failed. Finish that release rather than starting another, which
    # would skip a version permanently.
    if [ -n "$IN_VERSION" ] && [ "$IN_VERSION" != "$PINNED" ]; then
      die "main already carries the unreleased v$PINNED — finish that release, or revert the bump on main before asking for $IN_VERSION"
    fi
    VERSION="$PINNED"; BUMP_NEEDED=false
  else
    VERSION="${IN_VERSION:-$(./scripts/version.sh next "$BUMP")}"
    BUMP_NEEDED=true
    if [ "$VERSION" = "$PUBLISHED" ] || [ "$(higher "$VERSION" "$PUBLISHED")" != "$VERSION" ]; then
      die "v$PUBLISHED is already published — $VERSION would not move the release forward"
    fi
  fi
  TAG="v$VERSION"
fi

if [ -n "$TAG" ]; then
  case "$(tag_state "$TAG")" in
    elsewhere) die "tag $TAG already exists on another commit" ;;
    # An earlier run of this same release tagged and then failed. Nothing to
    # create; GoReleaser's `mode: replace` makes the rebuild safe.
    here)      ;;
  esac
fi

# "Latest" stays on the newest stable, so a prerelease suffix declines it.
LATEST=true
case "$VERSION" in *-*) LATEST=false ;; esac

printf 'mode=%s\nversion=%s\ntag=%s\nbump_needed=%s\nlatest=%s\n' \
  "$MODE" "$VERSION" "$TAG" "$BUMP_NEEDED" "$LATEST"
