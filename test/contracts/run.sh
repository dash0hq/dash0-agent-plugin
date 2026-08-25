#!/usr/bin/env bash
# Run install/config contracts locally or in CI.
# Usage: ./test/contracts/run.sh [claude|cursor|codex|bootstrap|release-plan|all]  (default: all)
set -euo pipefail
DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
target="${1:-all}"
case "$target" in
  claude|cursor|codex|bootstrap|release-plan) "$DIR/$target.sh" ;;
  all) for a in claude cursor codex bootstrap release-plan; do echo "### $a ###"; "$DIR/$a.sh"; done ;;
  *) echo "usage: $0 [claude|cursor|codex|bootstrap|release-plan|all]"; exit 2 ;;
esac
