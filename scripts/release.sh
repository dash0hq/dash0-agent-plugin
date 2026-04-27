#!/usr/bin/env bash
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
sed -i '' "s/\"version\": \"[^\"]*\"/\"version\": \"${VERSION}\"/" .claude-plugin/marketplace.json
sed -i '' "s/VERSION=\"[^\"]*\"/VERSION=\"${VERSION}\"/" scripts/on-event.sh

echo "Updated versions:"
grep '"version"' .claude-plugin/plugin.json .claude-plugin/marketplace.json
grep 'VERSION=' scripts/on-event.sh

git add .claude-plugin/plugin.json .claude-plugin/marketplace.json scripts/on-event.sh
git commit -m "release: ${TAG}"
git tag "$TAG"
git push
git push --tags

echo "Done. GoReleaser will build and publish binaries."
