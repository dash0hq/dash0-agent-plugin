#!/usr/bin/env bash
# SPDX-FileCopyrightText: Copyright 2026 Dash0 Inc.
# SPDX-License-Identifier: Apache-2.0
#
# This script makes sure that the `go` version in go.mod is the same as the
# golang:<version> base image in scripts/docker/Dockerfile.

set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
GOMOD="$ROOT/go.mod"
DOCKERFILE="$ROOT/scripts/docker/Dockerfile"

# go.mod: `go 1.27.3` -> 1.27.3
gomod_ver=$(grep -E '^go [0-9]+\.[0-9]+' "$GOMOD" | head -1 | awk '{print $2}')
# Dockerfile: `FROM golang:1.27.3-bookworm ...` -> 1.27.3
docker_ver=$(grep -oE 'golang:[0-9]+\.[0-9]+(\.[0-9]+)?' "$DOCKERFILE" | head -1 | cut -d: -f2)

if [ -z "$gomod_ver" ]; then
  echo "go-version-check: could not read the go directive from $GOMOD" >&2
  exit 1
fi
if [ -z "$docker_ver" ]; then
  echo "go-version-check: could not read golang:<version> from $DOCKERFILE" >&2
  exit 1
fi

if [ "$gomod_ver" != "$docker_ver" ]; then
  echo "go-version-check: version mismatch — go.mod is $gomod_ver but $DOCKERFILE pins golang:$docker_ver" >&2
  echo "                  bring the two into sync (same full version)." >&2
  exit 1
fi

echo "go-version-check: OK (Go $gomod_ver in go.mod and Dockerfile)"
