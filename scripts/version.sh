#!/usr/bin/env bash
# SPDX-FileCopyrightText: Copyright 2026 Dash0 Inc.
# SPDX-License-Identifier: Apache-2.0
#
# Everything about the release version, in one place.
#
#   scripts/version.sh check                  every pin agrees; exit 1 if not
#   scripts/version.sh set <version>          write it everywhere, then check
#   scripts/version.sh latest                 the newest published release
#   scripts/version.sh next patch|minor|major print what comes after it
#
# Ten files pin the version. They must agree: a bootstrap left behind asks
# GitHub for a release that was never tagged, and since the Claude marketplace
# lists this repo with no ref, that reaches users on their next `plugin install`.
# This is the only list of them, so the bump and the check cannot disagree about
# what needs bumping. Used by .github/workflows/release-prepare.yml, CI's
# consistency-checks job, and `make version-check`.
#
# Test hooks, used by test/contracts/release-plan.sh:
#   EXISTING_RELEASES  a version list, instead of querying GitHub
#   PINNED             the manifest version, instead of reading plugin.json

set -euo pipefail

cd "$(dirname "${BASH_SOURCE[0]}")/.."

die() { echo "::error::$1" >&2; exit 1; }

# The Copilot marketplace pins the marketplace and its plugin entry separately;
# both move together. Every other manifest carries one "version" key.
MANIFESTS=(
  .claude-plugin/plugin.json
  .cursor-plugin/plugin.json
  .codex-plugin/plugin.json
  copilot/plugin.json
  .github/plugin/marketplace.json
)
BOOTSTRAPS=(
  claude/claude-on-event.sh
  cursor/cursor-on-event.sh
  codex/codex-on-event.sh
  copilot/copilot-on-event.sh
)

# Tag names of published, non-draft, non-prerelease releases.
released() {
  if [ -n "${EXISTING_RELEASES:-}" ]; then
    printf '%s\n' "$EXISTING_RELEASES" | tr ' ' '\n'
    return
  fi
  gh api "repos/dash0hq/dash0-agent-plugin/releases" --paginate \
    --jq '.[] | select(.draft == false and .prerelease == false) | .tag_name'
}

pins() {
  for f in "${MANIFESTS[@]}"; do
    if [ "$f" = ".github/plugin/marketplace.json" ]; then
      printf '%s (metadata)\t%s\n' "$f" "$(jq -r '.metadata.version' "$f")"
      printf '%s (plugin)\t%s\n' "$f" "$(jq -r '.plugins[0].version' "$f")"
    else
      printf '%s\t%s\n' "$f" "$(jq -r '.version' "$f")"
    fi
  done
  for f in "${BOOTSTRAPS[@]}"; do
    # The pinned default only. DASH0_VERSION overrides it at runtime from a
    # separate line, which this deliberately does not match.
    printf '%s\t%s\n' "$f" "$(sed -n 's/^VERSION="\(.*\)"/\1/p' "$f")"
  done
}

check() {
  local out distinct
  out=$(pins)
  printf '%s\n' "$out" | column -t -s $'\t'
  distinct=$(printf '%s\n' "$out" | cut -f2 | sort -u)
  if [ "$(printf '%s\n' "$distinct" | wc -l)" -ne 1 ] || [ -z "$distinct" ]; then
    die "version pins disagree: $(printf '%s' "$distinct" | tr '\n' ' ')"
  fi
  echo "PASS: all $(printf '%s\n' "$out" | wc -l | tr -d ' ') pins agree on $distinct"
}

set_version() {
  local version="$1" before got
  # Prereleases are allowed so a dev build can be cut from a branch.
  [[ "$version" =~ ^[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.]+)?$ ]] \
    || die "'$version' is not a version (expected 0.2.0, or 0.2.0-dev.1)"
  # Reachable when a previous release failed after its bump merged: the tag and
  # branch guards both pass, every sed no-ops, and release-prepare then dies at
  # `git commit -a` with an opaque "nothing to commit".
  before=$(pins | cut -f2 | sort -u)
  [ "$before" != "$version" ] \
    || die "every file already pins $version — there is nothing to prepare. If v$version was never published, finish that release instead."
  # `sed -i` takes its backup suffix differently on BSD and GNU; passing one and
  # removing it after is the form both accept.
  for f in "${MANIFESTS[@]}"; do
    sed -i.bak "s/\"version\": \"[^\"]*\"/\"version\": \"${version}\"/" "$f"
    rm -f "$f.bak"
  done
  for f in "${BOOTSTRAPS[@]}"; do
    sed -i.bak "s/^VERSION=\"[^\"]*\"/VERSION=\"${version}\"/" "$f"
    rm -f "$f.bak"
  done
  check
  # check only proves the pins AGREE. This proves they agree on what was asked
  # for, so a sed that stops matching fails here rather than shipping.
  got=$(pins | cut -f2 | sort -u)
  [ "$got" = "$version" ] || die "asked for $version but the pins now read $got — the rewrite did not take"
}

# The newest published release. PUBLISHED, not tagged and not pinned: the tag is
# pushed before the build, so a run that fails after tagging leaves a tag with no
# release behind — and counting from tags would then agree with the manifests and
# propose the version after it, skipping the unreleased one silently and forever.
# Drafts and prereleases are excluded for the same reason a dev cut must not
# become the base for the next stable.
#
# `|| true` because pipefail is on and a grep matching nothing would exit before
# the diagnostic. Numeric sort per component, not `sort -V` (BSD and GNU
# disagree) and not lexical, which ranks 0.1.9 above 0.1.25.
latest() {
  local list v status=0
  # Captured separately so a failed query is not reported as an empty one. Rolled
  # into the pipeline, a missing GH_TOKEN or a 5xx surfaced as "no published
  # stable release found" against a repo with 25 of them.
  list=$(released) || status=$?
  [ "$status" -eq 0 ] \
    || die "could not list published releases (gh exited $status) — in Actions this usually means GH_TOKEN is not set on the step"
  v=$(printf '%s\n' "$list" | grep -E '^v?[0-9]+\.[0-9]+\.[0-9]+$' | sed 's/^v//' \
    | sort -t. -k1,1n -k2,2n -k3,3n | tail -n1) || true
  [ -n "$v" ] || die "no published stable release found"
  printf '%s\n' "$v"
}

next() {
  local part="$1" latest pinned major minor patch
  case "$part" in patch|minor|major) ;; *) die "expected patch, minor or major" ;; esac

  latest=$(latest)
  pinned="${PINNED:-$(jq -r '.version' .claude-plugin/plugin.json)}"
  [ "$pinned" = "$latest" ] || die "the manifests pin $pinned but the newest published release is v$latest — they must match before preparing a new version. If v$pinned was merged or tagged but never published, finish that release rather than bumping past it."

  IFS=. read -r major minor patch <<<"$latest"
  case "$part" in
    major) major=$((major + 1)); minor=0; patch=0 ;;
    minor) minor=$((minor + 1)); patch=0 ;;
    patch) patch=$((patch + 1)) ;;
  esac
  printf '%s.%s.%s\n' "$major" "$minor" "$patch"
}

case "${1:-}" in
  check)  check ;;
  latest) latest ;;
  set)    [ $# -eq 2 ] || die "usage: $0 set <version>"; set_version "$2" ;;
  next)   [ $# -eq 2 ] || die "usage: $0 next patch|minor|major"; next "$2" ;;
  *)      echo "usage: $0 check | latest | set <version> | next patch|minor|major" >&2; exit 2 ;;
esac
