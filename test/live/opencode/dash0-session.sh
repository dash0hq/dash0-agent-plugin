#!/usr/bin/env bash
# SPDX-FileCopyrightText: Copyright 2026 Dash0 Inc.
# SPDX-License-Identifier: Apache-2.0
#
# Sends the same scripted session run.sh asserts against a mock collector to a
# real Dash0 ingress, and prints the identifiers needed to query it back.
#
# Golden and consistency tests compare our output against our own expectations,
# so they cannot catch a mapping that is wrong in both places. This is the step
# that proves Dash0 received what we think we sent — see the checklist in
# opencode/README.md.
#
# Usage: test/live/opencode/dash0-session.sh [extra config lines]
#   test/live/opencode/dash0-session.sh
#   test/live/opencode/dash0-session.sh "$(printf 'omit_io: false\nomit_user_info: true')"
#
# Credentials default to the developer's own OpenCode config and can be
# overridden with DASH0_LIVE_OTLP_URL, DASH0_LIVE_AUTH_TOKEN, DASH0_LIVE_DATASET.
# The token is never printed.
set -euo pipefail
# shellcheck source=test/contracts/lib.sh
source "$(cd "$(dirname "${BASH_SOURCE[0]}")/../../contracts" && pwd)/lib.sh"

CACHE_DIR="${OPENCODE_LIVE_CACHE:-$HOME/.cache}"
CAPTURE="$REPO/test/capture/opencode"
LLM_PORT="${MOCK_LLM_PORT:-8817}"

# shellcheck source=test/live/opencode/session.sh
source "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/session.sh"

for t in opencode node jq curl; do
  command -v "$t" >/dev/null 2>&1 || skip_or_fail "$t is not installed"
done

# frontmatter KEY — one value from the developer's own OpenCode config, so a
# working local install is all this needs.
CONFIG="$HOME/.config/opencode/dash0-agent-plugin.local.md"
frontmatter() {
  [ -f "$CONFIG" ] || return 0
  sed -n '/^---$/,/^---$/p' "$CONFIG" | grep "^$1:" | sed "s/^$1: *//; s/^\"\(.*\)\"$/\1/" | head -1
}

OTLP_URL="${DASH0_LIVE_OTLP_URL:-$(frontmatter otlp_url)}"
AUTH_TOKEN="${DASH0_LIVE_AUTH_TOKEN:-$(frontmatter auth_token)}"
DATASET="${DASH0_LIVE_DATASET:-$(frontmatter dataset)}"
[ -n "$OTLP_URL" ] || skip_or_fail "no otlp_url — set DASH0_LIVE_OTLP_URL or configure $CONFIG"
[ -n "$AUTH_TOKEN" ] || skip_or_fail "no auth_token — set DASH0_LIVE_AUTH_TOKEN or configure $CONFIG"
[ -n "$DATASET" ] || DATASET=default

echo "== sending one scripted session to $OTLP_URL (dataset: $DATASET) =="
build_session_inputs
DEBUG_FILE="$(mktemp -d)/payloads.jsonl"
FROM=$(date -u +%Y-%m-%dT%H:%M:%SZ)
# The payloads are captured locally as well as sent, so the identifiers to query
# by are knowable without first finding the session in Dash0.
SANDBOX=$(session "$AUTH_TOKEN" "$OTLP_URL" "$DATASET" \
  "$(printf 'debug: true\ndebug_file: "%s"\n%s' "$DEBUG_FILE" "${1:-}")")
TO=$(date -u -v+1M +%Y-%m-%dT%H:%M:%SZ 2>/dev/null || date -u -d '+1 minute' +%Y-%m-%dT%H:%M:%SZ)

[ "$SESSION_EXIT" -eq 0 ] || { echo "ERROR: opencode run exited $SESSION_EXIT"; tail -30 "$SANDBOX/opencode.log"; exit 1; }
[ -s "$DEBUG_FILE" ] || { echo "ERROR: nothing was exported; see $SANDBOX/opencode.log"; exit 1; }

# The debug lines are "[dash0:<prefix>] <payload>"; only the export payloads
# parse as an OTLP request.
SPANS=$(sed 's/^\[dash0:[^]]*\] //' "$DEBUG_FILE" \
  | jq -s '[.[] | select(type == "object" and has("resourceSpans"))
           | .resourceSpans[].scopeSpans[].spans[]]')

echo
echo "$SPANS" | jq -r '.[] | "\(.name)\t\(.spanId)\t\(.parentSpanId)"' | column -t
echo
printf 'dataset      %s\n' "$DATASET"
printf 'session id   %s\n' "$(echo "$SPANS" | jq -r 'first(.[] | .attributes[] | select(.key == "gen_ai.conversation.id") | .value.stringValue)')"
printf 'trace id     %s\n' "$(echo "$SPANS" | jq -r '.[0].traceId')"
printf 'team name    %s\n' "$(echo "$SPANS" | jq -r 'first(.[] | .attributes[] | select(.key == "dash0.team.name") | .value.stringValue) // "(unset)"')"
printf 'span count   %s\n' "$(echo "$SPANS" | jq 'length')"
printf 'time range   %s .. %s\n' "$FROM" "$TO"
printf 'payloads     %s\n' "$DEBUG_FILE"
echo
echo "Query it back with the checklist in opencode/README.md."
