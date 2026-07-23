#!/usr/bin/env bash
# SPDX-FileCopyrightText: Copyright 2026 Dash0 Inc.
# SPDX-License-Identifier: Apache-2.0

set -euo pipefail

if [ $# -ne 1 ]; then
  echo "Usage: ./scripts/release.sh <version>"
  echo "Example: ./scripts/release.sh 0.2.0"
  exit 1
fi

VERSION="$1"
TAG="v${VERSION}"

if git rev-parse "$TAG" >/dev/null 2>&1; then
  echo "Error: tag $TAG already exists"
  exit 1
fi

if [ -n "$(git status --porcelain)" ]; then
  echo "Error: working directory is not clean"
  exit 1
fi

echo "Releasing $TAG..."

sed -i '' "s/\"version\": \"[^\"]*\"/\"version\": \"${VERSION}\"/" .claude-plugin/plugin.json
sed -i '' "s/\"version\": \"[^\"]*\"/\"version\": \"${VERSION}\"/" .cursor-plugin/plugin.json
sed -i '' "s/\"version\": \"[^\"]*\"/\"version\": \"${VERSION}\"/" .codex-plugin/plugin.json
sed -i '' "s/\"version\": \"[^\"]*\"/\"version\": \"${VERSION}\"/" copilot/plugin.json
sed -i '' "s/\"version\": \"[^\"]*\"/\"version\": \"${VERSION}\"/" .github/plugin/marketplace.json
sed -i '' "s/VERSION=\"[^\"]*\"/VERSION=\"${VERSION}\"/" scripts/on-event.sh
sed -i '' "s/VERSION=\"[^\"]*\"/VERSION=\"${VERSION}\"/" scripts/cursor-on-event.sh
sed -i '' "s/VERSION=\"[^\"]*\"/VERSION=\"${VERSION}\"/" scripts/codex-on-event.sh
sed -i '' "s/VERSION=\"[^\"]*\"/VERSION=\"${VERSION}\"/" copilot/copilot-on-event.sh

echo "Updated versions:"
grep '"version"' .claude-plugin/plugin.json
grep '"version"' .cursor-plugin/plugin.json
grep '"version"' .codex-plugin/plugin.json
grep '"version"' copilot/plugin.json
grep '"version"' .github/plugin/marketplace.json
grep 'VERSION=' scripts/on-event.sh
grep 'VERSION=' scripts/cursor-on-event.sh
grep 'VERSION=' scripts/codex-on-event.sh
grep 'VERSION=' copilot/copilot-on-event.sh

git add .claude-plugin/plugin.json .cursor-plugin/plugin.json .codex-plugin/plugin.json copilot/plugin.json .github/plugin/marketplace.json scripts/on-event.sh scripts/cursor-on-event.sh scripts/codex-on-event.sh copilot/copilot-on-event.sh
git commit -m "release: ${TAG}"
git tag "$TAG"
git push
git push --tags

echo "Done. GoReleaser will build and publish binaries."
