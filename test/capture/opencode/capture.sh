#!/usr/bin/env bash
# SPDX-FileCopyrightText: Copyright 2026 Dash0 Inc.
# SPDX-License-Identifier: Apache-2.0
# Records one scripted OpenCode session's full bus-event and hook stream to a
# JSONL file. Runs against the mock provider in mock-llm.mjs, so it needs no
# model credentials and produces the same turn every time.
#
# Usage: test/capture/opencode/capture.sh [output.jsonl]

set -euo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
OUT="${1:-$HERE/captured/captured_events.jsonl}"
PORT="${MOCK_LLM_PORT:-8817}"
# Reusing the real cache avoids a models.dev fetch on every run; a cold cache
# makes OpenCode's first start hang well past any sane timeout.
CACHE_DIR="${OPENCODE_CAPTURE_CACHE:-$HOME/.cache}"
LOG_DIR="$(dirname "$OUT")"
REQUEST_LOG="$LOG_DIR/llm-requests.jsonl"
STORAGE_DUMP="$LOG_DIR/storage.txt"

SANDBOX="$(mktemp -d)"
HOME_DIR="$SANDBOX/home"
PROJECT="$SANDBOX/project"
mkdir -p "$HOME_DIR/.config/opencode/plugin" "$HOME_DIR/.local/share" "$PROJECT"
mkdir -p "$LOG_DIR"
: > "$OUT"
: > "$REQUEST_LOG"

cleanup() {
  [ -n "${SERVER_PID:-}" ] && kill "$SERVER_PID" 2>/dev/null || true
  rm -rf "$SANDBOX"
}
trap cleanup EXIT

printf 'capture fixture project\n' > "$PROJECT/README.md"

cp "$HERE/plugin/capture.ts" "$HOME_DIR/.config/opencode/plugin/capture.ts"
jq --arg base "http://127.0.0.1:$PORT/v1" \
   --arg mcp "$HERE/mock-mcp.mjs" \
   --arg plugin "$HOME_DIR/.config/opencode/plugin/capture.ts" \
   '.provider.mock.options.baseURL = $base
    | .mcp.capture.command = ["node", $mcp]
    | .plugin = [$plugin]' \
   "$HERE/opencode.json" > "$HOME_DIR/.config/opencode/opencode.json"

MOCK_LLM_PORT="$PORT" MOCK_LLM_PROJECT_DIR="$PROJECT" \
  MOCK_LLM_REQUEST_LOG="$REQUEST_LOG" \
  node "$HERE/mock-llm.mjs" &
SERVER_PID=$!

for _ in $(seq 1 40); do
  # ?probe=1 keeps this readiness poll out of REQUEST_LOG, so every logged
  # request is unambiguously one OpenCode made.
  curl -sf -m 1 "http://127.0.0.1:$PORT/v1/models?probe=1" >/dev/null 2>&1 && break
  sleep 0.25
done

(
  cd "$PROJECT"
  HOME="$HOME_DIR" \
  XDG_CONFIG_HOME="$HOME_DIR/.config" \
  XDG_DATA_HOME="$HOME_DIR/.local/share" \
  XDG_STATE_HOME="$HOME_DIR/.local/state" \
  XDG_CACHE_HOME="$CACHE_DIR" \
  DASH0_OPENCODE_CAPTURE_FILE="$OUT" \
    opencode run --print-logs --model mock/mock-model \
      "Read the readme, then read a missing file, then call the capture echo tool, then delegate a sub-task." \
      < /dev/null > "$LOG_DIR/opencode.log" 2>&1
) || echo "opencode exited non-zero; see $LOG_DIR/opencode.log" >&2

# The sandbox is wiped on exit, so the on-disk layout OpenCode actually wrote
# has to be recorded here or it is unobservable after the run.
dump_storage() {
  local db table
  while IFS= read -r db; do
    printf '== %s\n' "${db#"$HOME_DIR/"}"
    sqlite3 "$db" .schema
    while IFS= read -r table; do
      printf -- '-- %s: %s rows\n' "$table" "$(sqlite3 "$db" "select count(*) from \"$table\"")"
    done < <(sqlite3 "$db" "select name from sqlite_master where type='table' order by name")

    printf '== %s session rows\n' "${db#"$HOME_DIR/"}"
    sqlite3 -json "$db" \
      'select id, parent_id, cost, tokens_input, tokens_output, tokens_reasoning,
              tokens_cache_read, tokens_cache_write from session'
    printf '\n== %s newest assistant message row\n' "${db#"$HOME_DIR/"}"
    sqlite3 "$db" \
      "select data from message where json_extract(data, '\$.role') = 'assistant'
       order by time_created desc limit 1"
    printf '\n'
  done < <(find "$HOME_DIR/.local/share/opencode" -name '*.db')

  printf '== file tree\n'
  (cd "$HOME_DIR" && find .local/share/opencode -not -name '*.db-*' | sort)
}
dump_storage > "$STORAGE_DUMP"

printf 'captured %s lines to %s\n' "$(wc -l < "$OUT" | tr -d ' ')" "$OUT"
printf 'storage layout dumped to %s\n' "$STORAGE_DUMP"
