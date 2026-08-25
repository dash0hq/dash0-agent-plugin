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
#   EVENT       github.event_name     — "push" (a tag) or "workflow_dispatch"
#   CHANNEL     inputs.channel        — stable | dev
#   DRY_RUN     inputs.dry_run        — "true" | "false"
#   IN_VERSION  inputs.version        — optional override, may be empty
#   REF_NAME    github.ref_name       — branch, or tag name on a tag push
#   RUN_NUMBER  github.run_number     — the dev build counter
#   PINNED      the version in .claude-plugin/plugin.json (defaults to reading it)

set -euo pipefail

cd "$(dirname "${BASH_SOURCE[0]}")/.."

EVENT="${EVENT:-workflow_dispatch}"
CHANNEL="${CHANNEL:-stable}"
DRY_RUN="${DRY_RUN:-false}"
IN_VERSION="${IN_VERSION:-}"
REF_NAME="${REF_NAME:-}"
RUN_NUMBER="${RUN_NUMBER:-0}"
PINNED="${PINNED:-$(jq -r '.version' .claude-plugin/plugin.json)}"

die() { echo "::error::$1" >&2; exit 1; }

# Only as complete as the checkout, hence fetch-depth: 0 in the workflow.
# EXISTING_TAGS stands in for git so the contract test does not depend on which
# tags a clone happens to hold.
tag_exists() {
  if [ -n "${EXISTING_TAGS:-}" ]; then
    case " $EXISTING_TAGS " in *" $1 "*) return 0 ;; esac
    return 1
  fi
  git rev-parse -q --verify "refs/tags/$1" >/dev/null 2>&1
}

if [ "$EVENT" = "push" ]; then
  # A hand-pushed tag. Release what it names, whatever the checkout pins.
  TAG="$REF_NAME"; VERSION="${TAG#v}"
  MODE=release; CREATE_TAG=false
else
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

if [ "$CREATE_TAG" = "true" ] && tag_exists "$TAG"; then
  die "tag $TAG already exists"
fi

# "Latest" stays on the newest stable, so a prerelease suffix declines it.
LATEST=true
case "$VERSION" in *-*) LATEST=false ;; esac

printf 'mode=%s\nversion=%s\ntag=%s\ncreate_tag=%s\nlatest=%s\n' \
  "$MODE" "$VERSION" "$TAG" "$CREATE_TAG" "$LATEST"
