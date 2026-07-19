#!/usr/bin/env bash
# Shared helpers for the install/config contract scripts (test/contracts/*.sh).
# These pin the behaviour the README's Installation/Configuration sections
# depend on. Runnable locally and in CI — see test/contracts/README.md.
set -euo pipefail

# Repo root (override with REPO=… for out-of-tree runs).
REPO="${REPO:-$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)}"
export REPO

# Native OS/arch in the release-asset naming the bootstraps resolve via uname.
os_arch() { echo "$(uname -s | tr '[:upper:]' '[:lower:]')-$(uname -m | sed 's/x86_64/amd64/;s/aarch64/arm64/')"; }

_bg_pids=()
_cleanup() { for p in "${_bg_pids[@]:-}"; do kill "$p" 2>/dev/null || true; done; }
trap _cleanup EXIT

# start_mock_otlp — build + run the mock OTLP server on :4319 (killed on exit).
start_mock_otlp() {
  local bin; bin="$(mktemp -d)/mock-otlp"
  make -C "$REPO" build-binary PKG=./test/e2e/mock-otlp-server OUT="$bin" >/dev/null
  "$bin" & _bg_pids+=("$!")
  sleep 1
}
