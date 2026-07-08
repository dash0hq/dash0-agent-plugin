#!/usr/bin/env bash
# SPDX-FileCopyrightText: Copyright 2026 Dash0 Inc.
# SPDX-License-Identifier: Apache-2.0
# Logs the JSON payload of every Cursor hook invocation to a file under
# $CAPTURE_DIR for inspection. Always exits 0 so the captured-from session
# is never blocked by capture failures.

set -u

CAPTURE_DIR="${DASH0_CURSOR_CAPTURE_DIR:-$HOME/source/dash0-agent-plugin/cursor/captured}"
mkdir -p "$CAPTURE_DIR" 2>/dev/null || exit 0

# Read all stdin into a variable so we can parse hook_event_name without
# losing the payload.
payload="$(cat)"

# Best-effort extract of hook_event_name and a timestamp to make file names
# searchable. If anything fails, fall back to a generic name.
event_name="$(printf '%s' "$payload" \
  | grep -o '"hook_event_name"[[:space:]]*:[[:space:]]*"[^"]*"' \
  | head -1 \
  | sed -E 's/.*"hook_event_name"[[:space:]]*:[[:space:]]*"([^"]+)".*/\1/' \
  )"
[ -z "$event_name" ] && event_name="unknown"

# nanosecond timestamp to keep events ordered and unique
ts="$(date +%s)-$$-$RANDOM"

out="$CAPTURE_DIR/${ts}_${event_name}.json"
printf '%s' "$payload" > "$out"

exit 0
