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
#   PINNED      the version in .claude-plugin/plugin.json (defaults to reading it)

set -euo pipefail

cd "$(dirname "${BASH_SOURCE[0]}")/.."

DRY_RUN="${DRY_RUN:-false}"
BUMP="${BUMP:-patch}"
IN_VERSION="${IN_VERSION:-}"
REF_NAME="${REF_NAME:-}"
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
  # main behind a published release. The publish now happens BEFORE the push to
  # main, so this is what a lost race leaves: v$PUBLISHED is public and Latest,
  # but the bump that names it never landed. Left alone, the next run counts from
  # the published release and cuts $PUBLISHED+1, skipping it permanently and
  # leaving main pointing at an older version than the one people install.
  # Refused loudly instead, because the fix is a bump PR and not another release.
  if [ "$PINNED" != "$PUBLISHED" ] && [ "$(higher "$PINNED" "$PUBLISHED")" = "$PUBLISHED" ]; then
    die "v$PUBLISHED is published but main still pins $PINNED — a previous run published and then could not move main. Open a PR running \`./scripts/version.sh set $PUBLISHED\` and merge it, then release again."
  fi
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
    # Normally unreachable: a run that pushes the tag and then fails deletes it
    # again along with its draft. Reachable if that cleanup did not run — a
    # hard-killed runner — and then it wedges every future release, so the
    # message has to name the way out.
    elsewhere) die "tag $TAG already exists on another commit, left by a run that failed after tagging. Nothing references it: delete it with \`git push origin :refs/tags/$TAG\` and dispatch again." ;;
    # An earlier run of this same release tagged and then failed. Nothing to
    # create; GoReleaser's `mode: replace` makes the rebuild safe.
    #
    # Only meaningful when bump_needed=false. This compares against the plan
    # job's HEAD, which is pre-bump, while the workflow compares against the
    # post-bump commit — so on a bump run a tag sitting on main's head passes
    # here and is then refused by the workflow's own guard. Loud and safe, but
    # the diagnostic blames the tag rather than the plan.
    here)      ;;
  esac
fi

# "Latest" stays on the newest stable, so a prerelease suffix declines it.
LATEST=true
case "$VERSION" in *-*) LATEST=false ;; esac

printf 'mode=%s\nversion=%s\ntag=%s\nbump_needed=%s\nlatest=%s\n' \
  "$MODE" "$VERSION" "$TAG" "$BUMP_NEEDED" "$LATEST"
