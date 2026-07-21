#!/usr/bin/env bash
# SPDX-FileCopyrightText: Copyright 2026 Dash0 Inc.
# SPDX-License-Identifier: Apache-2.0
#
# Phase 0 capture harness for GitHub Copilot CLI hook payloads. Records the raw
# payload of every hook invocation to a file under $capture_dir so we can build
# the internal/source/copilot adapter against ground-truth shapes rather than
# docs prose.
#
# ALWAYS exits 0. preToolUse is fail-closed (a non-zero exit denies the tool),
# so this must never fail the captured-from session.
#
# Arg $1 is a label ("camel"/"pascal") identifying which registration fired, so
# we can compare the payload shape produced by camelCase vs PascalCase event
# names in a single session (the D2 question).

set -u

label="${1:-unknown}"
capture_dir="${DASH0_COPILOT_CAPTURE_DIR:-$(dirname "${BASH_SOURCE[0]}")/captured}"
mkdir -p "$capture_dir" 2>/dev/null || exit 0

payload="$(cat)"

# Best-effort event name from whichever shape Copilot uses (snake_case
# hook_event_name, camelCase hookEventName, or a bare event/eventName field).
event="$(printf '%s' "$payload" \
  | grep -oiE '"(hook_event_name|hookeventname|event|eventname)"[[:space:]]*:[[:space:]]*"[^"]*"' \
  | head -1 \
  | sed -E 's/.*:[[:space:]]*"([^"]+)".*/\1/')"
[ -z "$event" ] && event="unknown"

ts="$(date +%s)-$$-$RANDOM"
out="$capture_dir/${ts}_${label}_${event}.json"

{
  printf '// label=%s argv=[%s]\n' "$label" "$*"
  printf '%s\n' "$payload"
} >"$out" 2>/dev/null

exit 0
