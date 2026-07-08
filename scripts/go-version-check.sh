#!/usr/bin/env bash
# SPDX-FileCopyrightText: Copyright 2026 Dash0 Inc.
# SPDX-License-Identifier: Apache-2.0
#
# Verifies that the Go version in go.mod matches the golang:<version> base image
# in scripts/docker/Dockerfile. Only the major.minor is compared, so a patch-level
# bump in go.mod (e.g. 1.25.0 -> 1.25.1) does not spuriously fail.

set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
GOMOD="$ROOT/go.mod"
DOCKERFILE="$ROOT/scripts/docker/Dockerfile"

# go.mod: `go 1.25.0` -> 1.25
gomod_ver=$(grep -E '^go [0-9]+\.[0-9]+' "$GOMOD" | head -1 | awk '{print $2}' | cut -d. -f1,2)
# Dockerfile: `FROM golang:1.25-bookworm ...` -> 1.25
docker_ver=$(grep -oE 'golang:[0-9]+\.[0-9]+' "$DOCKERFILE" | head -1 | cut -d: -f2)

if [ -z "$gomod_ver" ]; then
  echo "go-version-check: could not read Go version from $GOMOD" >&2
  exit 1
fi
if [ -z "$docker_ver" ]; then
  echo "go-version-check: could not read golang:<version> from $DOCKERFILE" >&2
  exit 1
fi

if [ "$gomod_ver" != "$docker_ver" ]; then
  echo "go-version-check: version mismatch — go.mod is $gomod_ver but $DOCKERFILE pins golang:$docker_ver" >&2
  echo "                  bring the two into sync (same major.minor)." >&2
  exit 1
fi

echo "go-version-check: OK (Go $gomod_ver in go.mod and Dockerfile)"
