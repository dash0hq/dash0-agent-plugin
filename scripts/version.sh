#!/usr/bin/env bash
# SPDX-FileCopyrightText: Copyright 2026 Dash0 Inc.
# SPDX-License-Identifier: Apache-2.0
#
# Everything about the release version, in one place.
#
#   scripts/version.sh check                  every pin agrees; exit 1 if not
#   scripts/version.sh set <version>          write it everywhere, then check
#   scripts/version.sh next patch|minor|major print what comes after the newest
#                                             published release
#
# Ten files pin the version. They must agree: a bootstrap left behind asks
# GitHub for a release that was never tagged, and since the Claude marketplace
# lists this repo with no ref, that reaches users on their next `plugin install`.
# This is the only list of them, so the bump and the check cannot disagree about
# what needs bumping. Used by .github/workflows/release-prepare.yml, CI's
# consistency-checks job, and `make version-check`.
#
# Test hooks, used by test/contracts/release-plan.sh:
#   EXISTING_TAGS  a tag list, instead of `git tag`
#   PINNED         the manifest version, instead of reading plugin.json

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
  local version="$1"
  # Prereleases are allowed so a dev build can be cut from a branch.
  [[ "$version" =~ ^[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.]+)?$ ]] \
    || die "'$version' is not a version (expected 0.2.0, or 0.2.0-dev.1)"
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
}

next() {
  local part="$1" latest pinned major minor patch
  case "$part" in patch|minor|major) ;; *) die "expected patch, minor or major" ;; esac

  # Newest stable tag. `|| true` because pipefail is on and a grep that matches
  # nothing would exit before the diagnostic below. Numeric sort per component,
  # not `sort -V` (BSD and GNU disagree) and not lexical, which ranks 0.1.9 above
  # 0.1.25. Prereleases are excluded so a dev build cut from a branch cannot
  # become the base for the next stable.
  latest=$( { [ -n "${EXISTING_TAGS:-}" ] && printf '%s\n' "$EXISTING_TAGS" | tr ' ' '\n' || git tag; } \
    | grep -E '^v[0-9]+\.[0-9]+\.[0-9]+$' | sed 's/^v//' \
    | sort -t. -k1,1n -k2,2n -k3,3n | tail -n1) || true
  [ -n "$latest" ] || die "no published stable release to count from — pass an explicit version instead"

  # Counted from the newest tag, not the manifests. They agree after a successful
  # release and diverge in one case — a bump merged whose release never published
  # — where counting from the manifests would skip the unreleased version
  # silently and forever.
  pinned="${PINNED:-$(jq -r '.version' .claude-plugin/plugin.json)}"
  [ "$pinned" = "$latest" ] || die "the manifests pin $pinned but the newest release is v$latest — they must match before preparing a new version. If v$pinned was never published, run Release (channel: stable) from main rather than preparing another bump."

  IFS=. read -r major minor patch <<<"$latest"
  case "$part" in
    major) major=$((major + 1)); minor=0; patch=0 ;;
    minor) minor=$((minor + 1)); patch=0 ;;
    patch) patch=$((patch + 1)) ;;
  esac
  printf '%s.%s.%s\n' "$major" "$minor" "$patch"
}

case "${1:-}" in
  check) check ;;
  set)   [ $# -eq 2 ] || die "usage: $0 set <version>"; set_version "$2" ;;
  next)  [ $# -eq 2 ] || die "usage: $0 next patch|minor|major"; next "$2" ;;
  *)     echo "usage: $0 check | set <version> | next patch|minor|major" >&2; exit 2 ;;
esac
