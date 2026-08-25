#!/usr/bin/env bash
# SPDX-FileCopyrightText: Copyright 2026 Dash0 Inc.
# SPDX-License-Identifier: Apache-2.0
#
# Decide what a Release run does, and refuse the combinations that would publish
# something wrong. Prints `key=value` for $GITHUB_OUTPUT; ::error:: and exit 1
# when a guard trips.
#
# A script, not an inline `run:` block, so it can be shellchecked and contract
# tested — it is the only conditional logic in the release path, and the part
# that decides whether a tag gets pushed. See test/contracts/release-plan.sh.
#
# Reads from the environment, all as GitHub sets them:
#   EVENT       github.event_name     — "push" or "workflow_dispatch"
#   REF_TYPE    github.ref_type       — "tag" or "branch", on a push
#   CHANNEL     inputs.channel        — stable | dev
#   DRY_RUN     inputs.dry_run        — "true" | "false"
#   IN_VERSION  inputs.version        — optional override, may be empty
#   REF_NAME    github.ref_name       — branch, or tag name on a tag push
#   RUN_NUMBER  github.run_number     — the dev build counter
#   PINNED      the version in .claude-plugin/plugin.json (defaults to reading it)

set -euo pipefail

cd "$(dirname "${BASH_SOURCE[0]}")/.."

EVENT="${EVENT:-workflow_dispatch}"
REF_TYPE="${REF_TYPE:-branch}"
CHANNEL="${CHANNEL:-stable}"
DRY_RUN="${DRY_RUN:-false}"
IN_VERSION="${IN_VERSION:-}"
REF_NAME="${REF_NAME:-}"
RUN_NUMBER="${RUN_NUMBER:-0}"
PINNED="${PINNED:-$(jq -r '.version' .claude-plugin/plugin.json)}"

die() { echo "::error::$1" >&2; exit 1; }

# absent | here | elsewhere. "here" matters: the tag is pushed before the build,
# so a run that fails after tagging leaves it behind, and re-running must
# continue rather than refuse its own tag. Only a tag on a DIFFERENT commit is a
# real collision.
#
# Only as complete as the checkout, hence fetch-depth: 0 in the workflow.
# TAG_STATE stands in for git so the contract test does not depend on which tags
# a clone happens to hold.
tag_state() {
  if [ -n "${TAG_STATE:-}" ]; then printf '%s\n' "$TAG_STATE"; return; fi
  local at
  at=$(git rev-parse -q --verify "refs/tags/$1^{commit}" 2>/dev/null) || { echo absent; return; }
  if [ "$at" = "$(git rev-parse HEAD)" ]; then echo here; else echo elsewhere; fi
}

if [ "$EVENT" = "push" ] && [ "$REF_TYPE" = "tag" ]; then
  # A hand-pushed tag. Release what it names, whatever the checkout pins.
  TAG="$REF_NAME"; VERSION="${TAG#v}"
  MODE=release; CREATE_TAG=false
elif [ "$EVENT" = "push" ]; then
  # A merge to main. A commit that moves the pinned version past the newest
  # published release is a release bump and nothing else — that is the whole
  # meaning of `release: vX` — so it publishes itself. Every other push to main
  # lands here too and resolves to mode=none, which is why the plan runs before
  # anything is built.
  VERSION="$PINNED"; TAG="v$VERSION"; CREATE_TAG=true; MODE=release
  PUBLISHED=$(./scripts/version.sh latest)
  if [ "$VERSION" = "$PUBLISHED" ]; then
    MODE=none; TAG=""; CREATE_TAG=false
  else
    # Ordering, not inequality: a reverted bump would otherwise try to re-release
    # an older version and fail later, at the tag, with a confusing message.
    NEWER=$(printf '%s\n%s\n' "$VERSION" "$PUBLISHED" \
      | sort -t. -k1,1n -k2,2n -k3,3n | tail -n1)
    [ "$NEWER" = "$VERSION" ] \
      || die "main pins $VERSION but v$PUBLISHED is already published — main has gone backwards"
  fi
else
  # Same shape `version.sh set` enforces. Unchecked, `version: v0.2.0` on the dev
  # channel becomes the tag vv0.2.0-dev.N, and a stray space fails at `git tag` —
  # both after the operator has been shown a green plan.
  if [ -n "$IN_VERSION" ] && ! [[ "$IN_VERSION" =~ ^[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.]+)?$ ]]; then
    die "'$IN_VERSION' is not a version (expected 0.2.0, or 0.2.0-dev.1; no leading v)"
  fi
  BASE="${IN_VERSION:-$PINNED}"
  if [ "$DRY_RUN" = "true" ]; then
    MODE=dry-run; VERSION="$BASE"; TAG=""; CREATE_TAG=false
  elif [ "$CHANNEL" = "dev" ]; then
    # run_number never repeats and always increases, so successive dev cuts off
    # one branch neither collide nor sort backwards.
    MODE=release; VERSION="${BASE}-dev.${RUN_NUMBER}"
    TAG="v$VERSION"; CREATE_TAG=true
  else
    MODE=release; VERSION="$BASE"; TAG="v$VERSION"; CREATE_TAG=true
    # The default branch is what `claude plugin install` reads, so a stable tag
    # main does not pin publishes a release no install asks for.
    [ "$REF_NAME" = "main" ] \
      || die "stable releases must be dispatched from main (got '$REF_NAME'); use channel=dev to cut from a branch"
    [ "$VERSION" = "$PINNED" ] \
      || die "main pins $PINNED, not $VERSION — run Release prepare and merge the bump PR first"
  fi
fi

if [ "$CREATE_TAG" = "true" ]; then
  case "$(tag_state "$TAG")" in
    elsewhere) die "tag $TAG already exists on another commit" ;;
    # Left by an earlier run of this same release that failed after tagging.
    # Nothing to create, and GoReleaser's `mode: replace` makes the rebuild safe.
    here)      CREATE_TAG=false ;;
  esac
fi

# "Latest" stays on the newest stable, so a prerelease suffix declines it.
LATEST=true
case "$VERSION" in *-*) LATEST=false ;; esac

printf 'mode=%s\nversion=%s\ntag=%s\ncreate_tag=%s\nlatest=%s\n' \
  "$MODE" "$VERSION" "$TAG" "$CREATE_TAG" "$LATEST"
