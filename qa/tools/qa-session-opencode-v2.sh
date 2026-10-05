#!/usr/bin/env bash
# SPDX-FileCopyrightText: Copyright 2026 Dash0 Inc.
# SPDX-License-Identifier: Apache-2.0
#
# Drive one real OpenCode V2 session and record everything needed to verify it.
#
# OpenCode has no hooks: the plugin subscribes to the server's V2 event stream
# and pipes it to the Go exporter. So this driver does not register a recorder.
# What it provisions instead is a private OpenCode server whose configuration
# QA owns:
#
#   - XDG_CONFIG_HOME points at a throwaway directory holding an opencode.json
#     that loads this repository's opencode-v2/ package, with the working-tree
#     exporter as its `executable`. The developer's own opencode.json, their
#     installed plugin and their background service are not used.
#   - Every plugin option is set explicitly. Options beat the user file at
#     ~/.opencode-v2/dash0-agent-plugin.local.md key by key, and any key left out
#     would silently come from it — including the developer's endpoint and token.
#   - The server is a long-lived `opencode serve` rather than `opencode run
#     --standalone`: a resumed turn and the session export both need the same
#     server the turns ran on. Standalone runs are covered by test/e2e.
#
# What stays the machine's: the provider login and the session database under
# ~/.local/share/opencode, so QA sessions appear in `opencode session list`.
#
#   opencode-events.jsonl   OpenCode's own `run --format json` stream, per turn
#   session-export.json     `opencode session export`: OpenCode's own record of
#                           every message, tool call and per-step token count
#   plugin-debug.log        every span the plugin emitted, as it emitted it
#   serve.log               the private server's log, including plugin errors
#
# Verify with qa-compare.py, which hands an opencode run to qa-compare-opencode-v2.py.
#
# Usage:
#   qa/tools/qa-session-opencode-v2.sh "<prompt>" [run-id]
#   QA_MODEL=openai/gpt-6-luna qa/tools/qa-session-opencode-v2.sh "..."
#   QA_OPENCODE_V2_RESUME="<second prompt>" qa/tools/qa-session-opencode-v2.sh "..."  # two turns
#   QA_OMIT_IO=false qa/tools/qa-session-opencode-v2.sh "..."    # export prompts and tool IO
#   QA_OPENCODE_V2_RELOAD_ON='sleep 10' QA_OPENCODE_V2_RELOAD_AFTER=2 \
#     qa/tools/qa-session-opencode-v2.sh "..."  # `opencode reload` 2s after the tool spawns `sleep 10`
#   QA_KEEP_SCRATCH=1 qa/tools/qa-session-opencode-v2.sh "..."   # keep the config (holds the token)

set -euo pipefail

ROOT=$(git rev-parse --show-toplevel)
cd "$ROOT"

PROMPT=${1:?usage: qa-session-opencode-v2.sh "<prompt>" [run-id]}
RUN_ID=${2:-$(date -u +%Y%m%dT%H%M%SZ)}
MODEL=${QA_MODEL:-opencode/muse-spark-1.3-contributor-free}
OMIT_IO=${QA_OMIT_IO:-true}
KEEP_SCRATCH=${QA_KEEP_SCRATCH:-0}
[[ -z ${QA_OPENCODE_V2_RELOAD_AFTER:-} || -n ${QA_OPENCODE_V2_RELOAD_ON:-} ]] ||
  { echo "qa: QA_OPENCODE_V2_RELOAD_AFTER needs QA_OPENCODE_V2_RELOAD_ON, a string in the tool command line" >&2; exit 2; }

RUN="$ROOT/qa/runs/$RUN_ID"
PROJECT="$RUN/project"
mkdir -p "$PROJECT"
# Setup checks reuse a fixed run id. These files are appended to, and the
# session id is read from the first event, so a stale one would win.
rm -f "$RUN/opencode-events.jsonl" "$RUN/opencode-stderr.log" "$RUN/plugin-debug.log" \
  "$RUN"/session-export*.json "$RUN/manifest.json" "$RUN/reload.log" "$RUN/reload-pids"

for tool in opencode go python3 git node; do
  command -v "$tool" >/dev/null || { echo "qa: MISSING: $tool" >&2; exit 2; }
done
[[ -d "$ROOT/opencode-v2/node_modules/@opencode/plugin" ]] || {
  echo "qa: opencode-v2/node_modules is missing. Run: (cd opencode-v2 && npm ci)" >&2
  exit 2
}

CONFIG="$ROOT/qa/config.local.json"
CONFIG_VALUES=$(python3 - "$CONFIG" 2>&1 <<'PY'
import json, sys
path = sys.argv[1]
try:
    cfg = json.load(open(path))
except FileNotFoundError:
    sys.exit(f"{path} does not exist. Copy qa/config.local.json.example and fill it in.")
except json.JSONDecodeError as err:
    sys.exit(f"{path} is not valid JSON: {err}")
token = cfg.get("authToken") or ""
if not token or "REPLACE_ME" in token:
    sys.exit(f"{path} has no usable authToken. This runtime hands the same token to the"
             " plugin to ingest and to qa-compare.py to read back.")
missing = [k for k in ("ingestUrl", "dataset") if not cfg.get(k)]
if missing:
    sys.exit(f"{path} is missing: {', '.join(missing)}")
print(cfg["ingestUrl"], cfg["dataset"], token)
PY
) || { echo "qa: $CONFIG_VALUES" >&2; exit 2; }
read -r OTLP_URL DATASET INGEST_TOKEN <<<"$CONFIG_VALUES"
echo "qa: exporting to $OTLP_URL / $DATASET"

# Outside qa/runs on purpose: the config holds the ingest token, and run
# directories get attached to bug reports.
SCRATCH=$(mktemp -d "${TMPDIR:-/tmp}/qa-opencode-XXXXXX")
SERVER_PID=""
cleanup() {
  [[ -n "$SERVER_PID" ]] && kill "$SERVER_PID" 2>/dev/null || true
  if [[ "$KEEP_SCRATCH" == "1" ]]; then
    echo "qa: kept the scratch config at $SCRATCH (holds an ingest token — delete it when done)"
  else
    rm -rf "$SCRATCH"
  fi
}
trap cleanup EXIT

# There is no OpenCode release asset yet, so the working tree is always what is
# under test. The executable option bypasses the bootstrap's download.
go build -o "$SCRATCH/opencode-v2-on-event" ./cmd/opencode-v2-on-event
VERSION=$(grep '^VERSION=' opencode-v2/opencode-v2-on-event.sh | cut -d'"' -f2)
mkdir -p "$SCRATCH/config/opencode"
python3 - "$SCRATCH/config/opencode/opencode.json" <<PY
import json, sys
json.dump({"plugins": [{"package": "$ROOT/opencode-v2", "options": {
    "executable": "$SCRATCH/opencode-v2-on-event",
    "otlp_url": "$OTLP_URL",
    "auth_token": "$INGEST_TOKEN",
    # A keychain token outranks auth_token, and an empty option falls back to
    # the developer's own file or DASH0_*. A service that never exists fails
    # the lookup, so the QA token is the one used.
    "auth_token_keychain_service": "dash0-qa-no-keychain",
    "dataset": "$DATASET",
    "agent_name": "opencode",
    "team_name": "dash0-qa",
    "omit_io": $([[ "$OMIT_IO" == "false" ]] && echo False || echo True),
    "omit_user_info": False,
    "omit_identity_fallback": False,
    "debug": True,
    "debug_file": "$RUN/plugin-debug.log",
}}]}, open(sys.argv[1], "w"), indent=2)
PY
chmod 600 "$SCRATCH/config/opencode/opencode.json"

# A project the session can touch freely, recreated on every run so a reused
# run id starts from the same files. git so VCS attributes are exercised.
rm -rf "$PROJECT" && mkdir -p "$PROJECT"
git -C "$PROJECT" init -q
printf '# QA fixture\n\nA scratch project for an OpenCode QA run.\n' >"$PROJECT/README.md"
# A commit, so HEAD resolves and the vcs.ref.* attributes are exported. Signing
# is off for it: a global commit.gpgsign can prompt, and nothing can answer.
git -C "$PROJECT" add README.md
git -C "$PROJECT" \
  -c user.email=qa@dash0.com -c user.name="Dash0 QA" \
  -c commit.gpgsign=false -c tag.gpgsign=false \
  commit -q -m "qa run $RUN_ID"

PORT=$(python3 -c 'import socket; s=socket.socket(); s.bind(("127.0.0.1", 0)); print(s.getsockname()[1])')
export OPENCODE_SERVER_PASSWORD
OPENCODE_SERVER_PASSWORD=$(python3 -c 'import secrets; print(secrets.token_urlsafe(24))')
SERVER="http://127.0.0.1:$PORT"
(cd "$PROJECT" && exec env XDG_CONFIG_HOME="$SCRATCH/config" OPENCODE_V2_PLUGIN_DATA="$SCRATCH/state" \
  opencode serve --hostname 127.0.0.1 --port "$PORT" --print-logs --log-level info) \
  >"$RUN/serve.log" 2>&1 &
SERVER_PID=$!
for _ in $(seq 1 50); do
  grep -q 'server listening' "$RUN/serve.log" 2>/dev/null && break
  sleep 0.2
done
grep -q 'server listening' "$RUN/serve.log" || { echo "qa: opencode serve did not start; see $RUN/serve.log" >&2; exit 2; }

STARTED_AT=$(date -u +%Y-%m-%dT%H:%M:%S.000Z)
turn() {
  # A turn that hangs must fail the run, not block it. macOS has no timeout(1).
  (cd "$PROJECT" && perl -e 'alarm shift; exec @ARGV' "${QA_TURN_TIMEOUT:-300}" \
    opencode run --server "$SERVER" --format json --auto -m "$MODEL" "$@") \
    >>"$RUN/opencode-events.jsonl" 2>>"$RUN/opencode-stderr.log"
}
# A reload replaces the exporter process; its PIDs either side are the evidence
# that the reload happened, rather than being absorbed as a no-op.
exporters() { { pgrep -f "$SCRATCH/opencode-v2-on-event" || true; } | sort | paste -sd, -; }
now_ms() { perl -MTime::HiRes=time -e 'printf "%d\n", time * 1000'; }
RELOAD_PID=""
if [[ -n ${QA_OPENCODE_V2_RELOAD_AFTER:-} ]]; then
  # A timer started with the turn can fire before the tool runs, so the reload
  # waits for OpenCode to log spawning the tool's process. The comparer then
  # checks the export's tool timings to prove the reload landed mid-tool.
  (
    for _ in $(seq 1 $((${QA_TURN_TIMEOUT:-300} * 5))); do
      grep 'spawning process' "$RUN/serve.log" | grep -qF -- "$QA_OPENCODE_V2_RELOAD_ON" && break
      sleep 0.2
    done
    sleep "$QA_OPENCODE_V2_RELOAD_AFTER"
    before=$(exporters)
    started=$(now_ms)
    (cd "$PROJECT" && opencode reload --server "$SERVER") >"$RUN/reload.log" 2>&1 || echo "reload exited $?" >>"$RUN/reload.log"
    ended=$(now_ms)
    sleep 2
    printf '%s %s %s %s\n' "${before:--}" "$(exporters)" "$started" "$ended" >"$RUN/reload-pids"
  ) &
  RELOAD_PID=$!
fi
RC=0
turn "$PROMPT" || RC=$?
[[ -z $RELOAD_PID ]] || wait "$RELOAD_PID" || true
SESSION_ID=$(python3 -c '
import json, sys
for line in open(sys.argv[1]):
    try: print(json.loads(line)["sessionID"]); break
    except (ValueError, KeyError): pass' "$RUN/opencode-events.jsonl" 2>/dev/null || true)
[[ -n "$SESSION_ID" ]] || { echo "qa: no session id in opencode-events.jsonl (exit $RC); see $RUN/opencode-stderr.log" >&2; exit 2; }
TURNS=1
if [[ -n ${QA_OPENCODE_V2_RESUME:-} && $RC -eq 0 ]]; then
  turn -s "$SESSION_ID" "$QA_OPENCODE_V2_RESUME" || RC=$?
  TURNS=2
fi

# The exporter receives events asynchronously, so a turn's chat span can land a
# moment after `run` returns. Wait for one per turn rather than a fixed sleep.
# No log or no match counts 0; a failed run still needs its manifest.
chats() { { grep -o '"name":"chat [^"]*"' "$RUN/plugin-debug.log" 2>/dev/null || true; } | wc -l | tr -d ' '; }
for _ in $(seq 1 75); do
  [[ $(chats) -ge $TURNS ]] && break
  sleep 0.2
done
ENDED_AT=$(date -u +%Y-%m-%dT%H:%M:%S.000Z)

# Failures here are recorded, not fatal: a failed run still needs its manifest,
# and the comparer refuses to judge a run whose export failed.
EXPORT_RC=0
opencode session export --server "$SERVER" "$SESSION_ID" >"$RUN/session-export.json" || EXPORT_RC=$?
# A sub-agent runs as a child session, exported separately. Its id is on the
# parent's tool part, which is the same link the plugin follows.
CHILDREN=""
[[ $EXPORT_RC -ne 0 ]] || CHILDREN=$(python3 -c '
import json, sys
for m in json.load(open(sys.argv[1])).get("messages") or []:
    for p in m.get("content") or []:
        sid = ((p.get("state") or {}).get("metadata") or {}).get("sessionID")
        if p.get("type") == "tool" and isinstance(sid, str): print(sid)' "$RUN/session-export.json") || EXPORT_RC=$?
for child in $CHILDREN; do
  opencode session export --server "$SERVER" "$child" >"$RUN/session-export-$child.json" || EXPORT_RC=$?
done

# Stop the server before editing its log: an in-place edit replaces the file,
# and a running server would keep writing to the deleted one. The password it
# printed is single-use, but strip it anyway.
kill "$SERVER_PID" 2>/dev/null || true; wait "$SERVER_PID" 2>/dev/null || true; SERVER_PID=""
sed -i.bak '/server password/d' "$RUN/serve.log" && rm -f "$RUN/serve.log.bak"

SPANS_LOGGED=0
[[ -f "$RUN/plugin-debug.log" ]] &&
  SPANS_LOGGED=$(grep -c '\[dash0:trace\]' "$RUN/plugin-debug.log" || true)

RELOAD_ON_JSON=$(python3 -c 'import json, sys; print(json.dumps(sys.argv[1]))' "${QA_OPENCODE_V2_RELOAD_ON:-}")
cat >"$RUN/manifest.json" <<EOF
{
  "runtime": "opencode-v2",
  "run_id": "$RUN_ID",
  "session_id": "$SESSION_ID",
  "prompt": $(printf '%s' "$PROMPT" | python3 -c 'import json,sys; print(json.dumps(sys.stdin.read()))'),
  "started_at": "$STARTED_AT",
  "ended_at": "$ENDED_AT",
  "opencode_exit_code": $RC,
  "export_exit_code": $EXPORT_RC,
  "turns": $TURNS,
  "model": "$MODEL",
  "omit_io": $OMIT_IO,
  "opencode_version": "$(opencode --version | awk '{print $NF}')",
  "binary_under_test": "working tree $(git rev-parse --short HEAD)",
  "otlp_url": "$OTLP_URL",
  "dataset": "$DATASET",
  "plugin_version": "$VERSION",
  "plugin_commit": "$(git rev-parse HEAD)",
  "plugin_dirty": $(git diff --quiet && echo false || echo true),
  "chat_spans_logged": $(chats),
  "subagent_sessions": "$(printf '%s' "$CHILDREN" | paste -sd, -)",
  "spans_logged": $SPANS_LOGGED,
  "reload_after": "${QA_OPENCODE_V2_RELOAD_AFTER:-}",
  "exporter_pids_before_reload": "$( [[ -f "$RUN/reload-pids" ]] && cut -d' ' -f1 "$RUN/reload-pids" )",
  "exporter_pids_after_reload": "$( [[ -f "$RUN/reload-pids" ]] && cut -d' ' -f2 "$RUN/reload-pids" )",
  "reload_on": $RELOAD_ON_JSON,
  "reload_started_ms": "$( [[ -f "$RUN/reload-pids" ]] && cut -d' ' -f3 "$RUN/reload-pids" )",
  "reload_ended_ms": "$( [[ -f "$RUN/reload-pids" ]] && cut -d' ' -f4 "$RUN/reload-pids" )"
}
EOF

echo "qa: session $SESSION_ID, $TURNS turn(s), $SPANS_LOGGED span(s) in the debug log"
echo "qa: run written to $RUN"
echo "qa: verify with  qa/tools/qa-compare.py $RUN"
