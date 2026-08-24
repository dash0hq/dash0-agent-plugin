#!/usr/bin/env bash
# SPDX-FileCopyrightText: Copyright 2026 Dash0 Inc.
# SPDX-License-Identifier: Apache-2.0
#
# Bundles the plugin into the single file the release publishes and the npm
# package ships: opencode/dist/dash0-opencode-plugin.js.
#
# OpenCode loads a plugin from a path or an npm package with no install step of
# its own, so the delivered file has to be self-contained. The check at the end
# enforces that: anything the bundler left unresolved would be a module the
# user's OpenCode is expected to provide.

set -euo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
OUT="$HERE/dist/dash0-opencode-plugin.js"

mkdir -p "$HERE/dist"

if command -v bun &>/dev/null; then
  bun build "$HERE/src/index.ts" --target=node --format=esm --outfile="$OUT"
elif command -v npx &>/dev/null; then
  npx --yes esbuild@0.25.0 "$HERE/src/index.ts" \
    --bundle --platform=node --format=esm --packages=bundle --outfile="$OUT"
else
  echo "build.sh: neither bun nor npx found; install one to bundle the plugin" >&2
  exit 1
fi

# Every remaining specifier must be a Node builtin. `@opencode-ai/plugin` is a
# type-only import and is erased, so a hit on it means the bundler kept a value
# import that OpenCode would have to resolve at load time.
LEAKED="$(grep -oE '(from[[:space:]]*|require\()["'"'"'][^"'"'"']+["'"'"']' "$OUT" \
  | grep -oE '["'"'"'][^"'"'"']+["'"'"']' \
  | tr -d '"'"'"'"' \
  | grep -v '^node:' || true)"

if [ -n "$LEAKED" ]; then
  echo "build.sh: bundle imports modules outside the Node builtins:" >&2
  printf '%s\n' "$LEAKED" >&2
  exit 1
fi

printf 'built %s (%s bytes)\n' "$OUT" "$(wc -c <"$OUT" | tr -d ' ')"
