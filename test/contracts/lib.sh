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

# force_https — clone github over HTTPS (CI runners have no SSH key). Writes to
# the CURRENT $HOME's gitconfig, so call it AFTER switching HOME (keeps it
# hermetic — it never touches your real ~/.gitconfig).
force_https() { git config --global url."https://github.com/".insteadOf "git@github.com:"; }

# skip_or_fail MESSAGE — bail out of a contract whose preconditions can't be met.
# Locally that's a skip (a dev may be offline); in CI it's a failure, because a
# silently skipped contract reports green while testing nothing. Exits the script,
# so only call it when the remaining contracts depend on the missing precondition.
skip_or_fail() {
  if [ -n "${CI:-}" ]; then
    echo "ERROR: $1 — refusing to skip in CI" >&2
    exit 1
  fi
  echo "SKIP: $1"
  exit 0
}

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
