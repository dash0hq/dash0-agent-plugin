#!/usr/bin/env bash
# SPDX-FileCopyrightText: Copyright 2026 Dash0 Inc.
# SPDX-License-Identifier: Apache-2.0
#
# Drive one real Cursor session and record everything needed to verify it.
#
# This runtime is a hybrid of the other three, because Cursor allows exactly one
# of the two things they each rely on and refuses the other.
#
# What it refuses: a throwaway home. Cursor's stored login is not in the config
# directory — a copied cli-config.json does not authenticate — and CURSOR_CONFIG_DIR
# moves the configuration without moving where hooks are read from, measured
# 2026-09-01: a session started fine against a redirected config directory and
# fired not one hook from it. So the REGISTRATION has to be the machine's own,
# under the real HOME, exactly as on the claude runtime.
#
# What it allows: configuration. Cursor passes the launcher's environment to hook
# processes, so CURSOR_PLUGIN_OPTION_* reaches the plugin, and internal/harness
# ranks that above the configuration file. So unlike claude — where a managed
# remote-settings.json beats every override and QA has to read whatever the
# install writes to — a cursor run points the installed registration at the QA
# target for the duration of the session and nothing else changes. That is what
# makes the run verifiable at all: this machine's install exports to production
# while qa/config.local.json reads development, and without the override the run
# would be a healthy session with no spans to find.
#
# DASH0_PLUGIN_DATA is redirected into the run directory for the same reason
# QA_SWAP_BINARY is opt-in on claude: the binary cache under ~/.local/state is
# shared with the developer's own live Cursor sessions, and a run must not touch
# it. The bootstrap honours that variable for both the cache and the session
# state, so the run gets its own of each.
#
# What it produces:
#
#   record/            every hook payload, byte for byte — the pipeline's input
#   transcript.jsonl   Cursor's own agent transcript — the second channel
#   plugin-debug.log   every span the plugin emitted, as it emitted it
#   tty.log            the terminal, with the escape sequences stripped
#
# There is no harness-figures channel. The TUI has no --output-format, and print
# mode — which does — fires no afterAgentResponse and so produces no turn at all.
# qa/tools/qa-cursor-drive.py explains that measurement.
#
# Usage:
#   qa/tools/qa-session-cursor.sh "<prompt>" [run-id]
#   QA_CURSOR_BINARY=working-tree qa/tools/qa-session-cursor.sh "..."   # unreleased code
#   QA_CURSOR_RESUME="<second prompt>" qa/tools/qa-session-cursor.sh "..."  # two turns
#   QA_CURSOR_MCP=1 qa/tools/qa-session-cursor.sh "..."                 # stub MCP server
#   QA_CURSOR_ALLOW_STALE=1 qa/tools/qa-session-cursor.sh "..."         # see step 1b
#   QA_MODEL=... qa/tools/qa-session-cursor.sh "..."
#
# Auth: the machine's own `cursor-agent login`. Nothing is passed in and nothing
# is written, so a run cannot rotate or invalidate that login.

set -euo pipefail

ROOT=$(git rev-parse --show-toplevel)
cd "$ROOT"

PROMPT=${1:?usage: qa-session-cursor.sh "<prompt>" [run-id]}
RUN_ID=${2:-$(date -u +%Y%m%dT%H%M%SZ)}
BINARY_MODE=${QA_CURSOR_BINARY:-release}

RUN="$ROOT/qa/runs/$RUN_ID"
PROJECT="$RUN/project"
RECORD="$RUN/record"
# The recorder appends, so a reused run id leaves two sessions in one record/.
# qa-compare.py filters the index by the manifest's session_id, so that is
# survivable; deleting record/ would throw away evidence a spec asked to keep.
# Everything this script reads back is scoped to the rows appended after
# INDEX_BASELINE, which is taken just before the launch.
mkdir -p "$PROJECT/.cursor" "$RECORD"

for tool in cursor-agent go python3 git; do
  command -v "$tool" >/dev/null || { echo "qa: MISSING: $tool" >&2; exit 2; }
done

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
    sys.exit(f"{path} has no usable authToken. This runtime points the installed"
             " registration at the QA target, so the same token both ingests and reads"
             " back; a token that cannot ingest 401s on every export and the run reads"
             " as total telemetry loss. The preflight check"
             " ingest-token-reaches-the-ingress proves it before a session is paid for.")
missing = [k for k in ("ingestUrl", "dataset") if not cfg.get(k)]
if missing:
    sys.exit(f"{path} is missing: {', '.join(missing)}")
print(cfg["ingestUrl"], cfg["dataset"], token)
PY
) || { echo "qa: $CONFIG_VALUES" >&2; exit 2; }
read -r OTLP_URL DATASET INGEST_TOKEN <<<"$CONFIG_VALUES"
echo "qa: exporting to $OTLP_URL / $DATASET"

# 1. The machine's registration, which is the thing under test. Read rather than
#    written: this runtime does not provision, and a run that quietly registered
#    its own hooks alongside the installed ones would double every span.
#
#    The wrapper's VERSION decides which binary the bootstrap looks for, so it is
#    read here and reported in the manifest. An install several releases behind
#    the working tree is normal on a developer machine and is exactly why
#    QA_CURSOR_BINARY=working-tree exists.
REGISTRATION=$(python3 - "$HOME" <<'PY'
import json, os, sys
home = sys.argv[1]
path = os.path.join(home, ".cursor", "hooks.json")
try:
    hooks = (json.load(open(path)).get("hooks") or {})
except FileNotFoundError:
    sys.exit(f"{path} does not exist, so no Cursor hook is registered on this machine."
             " Install the plugin with install-cursor.sh before running QA.")
except json.JSONDecodeError as err:
    sys.exit(f"{path} is not valid JSON: {err}")
commands = {entry.get("command", "") for entries in hooks.values() for entry in entries}
ours = sorted(c for c in commands if "cursor-on-event.sh" in c)
if not ours:
    sys.exit(f"{path} registers no cursor-on-event.sh hook, so the plugin will not fire."
             " Install it with install-cursor.sh before running QA.")
if len(ours) > 1:
    sys.exit(f"{path} registers {len(ours)} different cursor-on-event.sh commands"
             f" ({', '.join(ours)}). Two registrations would emit every span twice;"
             " clean the file up before running QA.")
script = ours[0].replace("$HOME", home)
events = sorted(e for e, entries in hooks.items()
                if any("cursor-on-event.sh" in entry.get("command", "") for entry in entries))
version = ""
try:
    for line in open(script):
        if line.startswith("VERSION="):
            version = line.split('"')[1]
            break
except OSError as err:
    sys.exit(f"{ours[0]} is registered but not readable: {err}")
if not version:
    sys.exit(f"{script} declares no VERSION=, so the binary the bootstrap looks for"
             " cannot be predicted and working-tree mode cannot place one.")
print(version, len(events), script)
PY
) || { echo "qa: $REGISTRATION" >&2; exit 2; }
read -r VERSION REGISTERED_EVENTS PLUGIN_SCRIPT <<<"$REGISTRATION"
echo "qa: installed registration — v$VERSION over $REGISTERED_EVENTS event(s)"

# 1b. The registered wrapper must be the one this checkout ships, and the run
#     stops when it is not.
#
#     This is not tidiness. The pre-0.1.25 wrapper read the configuration file
#     itself and exported each value, including
#     `export CURSOR_PLUGIN_OPTION_AUTH_TOKEN="$val"` — the same high-precedence
#     form this driver uses. So a stale wrapper OVERWRITES the QA token with
#     whatever the developer's own config file holds, and the session exports to
#     the QA endpoint with a production credential. Measured 2026-09-01 against
#     the v0.1.19 wrapper on this machine: 6 hooks recorded, both spans built and
#     logged, every export 401, zero spans in Dash0. The report read as total
#     telemetry loss and the plugin was blameless.
#
#     Configuration moved into Go after 0.1.24, so the shipped wrapper exports
#     nothing and cannot do this. Comparing the file rather than the version
#     string is what makes the check exact: a hand-edited wrapper at the right
#     version is caught too.
if ! cmp -s "$PLUGIN_SCRIPT" "$ROOT/cursor/cursor-on-event.sh"; then
  if [[ "${QA_CURSOR_ALLOW_STALE:-0}" != "1" ]]; then
    cat >&2 <<EOF
qa: the registered wrapper is not the one this checkout ships.

  registered: $PLUGIN_SCRIPT (v$VERSION)
  shipped   : $ROOT/cursor/cursor-on-event.sh

QA cannot substitute its own registration: Cursor honours the user-scope and the
project-scope hook files together, so a second one would emit every span twice.
Re-install so the machine runs the shipped bootstrap:

  DASH0_VERSION=$(python3 -c "import json;print(json.load(open('$ROOT/.cursor-plugin/plugin.json'))['version'])") bash $ROOT/install-cursor.sh

A wrapper from before 0.1.25 does more than lag. It reads the configuration file
and re-exports CURSOR_PLUGIN_OPTION_AUTH_TOKEN from it, which overwrites the QA
token with the developer's own, so every export 401s while the session looks
perfectly healthy.

QA_CURSOR_ALLOW_STALE=1 runs anyway, for the one case where testing the old
install IS the question. The manifest records it and the report must say so.
EOF
    exit 2
  fi
  echo "qa: WARNING — running against a wrapper this checkout does not ship." >&2
  WRAPPER_MATCHES_SHIPPED=false
else
  WRAPPER_MATCHES_SHIPPED=true
fi

# 2. Two ways the user's own configuration would beat the QA overrides, both of
#    which produce a healthy session whose spans go somewhere else or nowhere.
#    Neither is a product defect and neither is fixable from inside the run, so
#    the run stops rather than reporting a telemetry loss it caused itself.
python3 - "$HOME" <<'PY' || exit 2
import os, sys
path = os.path.join(sys.argv[1], ".cursor", "dash0-agent-plugin.local.md")
if not os.path.exists(path):
    sys.exit(0)
text = open(path).read()
if "auth_token_keychain_service" in text:
    sys.exit(f"{path} declares auth_token_keychain_service. internal/harness ranks a"
             " successful keychain lookup ABOVE CURSOR_PLUGIN_OPTION_AUTH_TOKEN, so the"
             " session would export with that token instead of the QA one and every"
             " span would 401 or land in someone else's tenant. Comment the key out for"
             " the run.")
for line in text.splitlines():
    if line.replace(" ", "").startswith("enabled:") and "false" in line:
        sys.exit(f"{path} sets enabled: false, so the plugin returns before it reads"
                 " anything and the session emits nothing at all.")
PY

# 3. The recorder, and the hook command that carries its output directory. Cursor
#    puts hook_event_name in the payload, as Claude Code and Codex do, so no
#    event-name argv is needed.
go build -o "$RUN/recorder" ./qa/recorder
cat >"$RUN/record-hook.sh" <<EOF
#!/usr/bin/env bash
# Generated by qa/tools/qa-session-cursor.sh for run $RUN_ID.
export QA_RECORD_DIR="$RECORD"
exec "$RUN/recorder"
EOF
chmod +x "$RUN/record-hook.sh"

# 4. Register the recorder at PROJECT scope, which is the whole reason this run
#    leaves the machine alone. Cursor honours <project>/.cursor/hooks.json
#    alongside ~/.cursor/hooks.json — measured 2026-09-01, a project file's hooks
#    fired and the user's kept firing — so the recorder is additive and the
#    developer's own sessions never see it.
#
#    The event list is generated from cursor/hooks.json, so the recorder cannot
#    miss an event the plugin acts on. That includes preToolUse: unlike Copilot,
#    where that event is fail-closed and QA stays off it, the Cursor plugin
#    already registers it, and the recorder writes nothing to stdout and exits 0.
python3 - "$ROOT/cursor/hooks.json" "$RUN/record-hook.sh" \
    >"$PROJECT/.cursor/hooks.json" <<'PY'
import json, sys
plugin_hooks, command = sys.argv[1], sys.argv[2]
events = list(json.load(open(plugin_hooks))["hooks"])
json.dump({"version": 1,
           "hooks": {e: [{"command": command}] for e in events}},
          sys.stdout, indent=2)
PY
RECORDER_EVENTS=$(python3 -c "
import json; print(len(json.load(open('$PROJECT/.cursor/hooks.json'))['hooks']))")
echo "qa: recording $RECORDER_EVENTS hook events"

# 4b. The stub MCP server, registered at project scope. Opt-in: a session with an
#     MCP server attached carries its tool catalogue in context, which is not what
#     most runs are measuring.
#
#     One server, not the two the claude driver registers. Cursor exposes an MCP
#     call through the generic preToolUse/postToolUse pair as MCP:<tool>, dropping
#     the specialized hooks that carry the server name, so
#     internal/source/cursor tags mcp_server with the placeholder "cursor". A
#     second server could not be told from the first, so registering one keeps the
#     fixture honest about what the run can prove.
#
#     Cursor has no --strict-mcp-config, so a user-scope ~/.cursor/mcp.json loads
#     alongside this file and a QA prompt could reach the developer's production
#     connectors. The run refuses rather than risk it.
if [[ "${QA_CURSOR_MCP:-0}" == "1" ]]; then
  python3 - "$HOME" <<'PY' || exit 2
import json, os, sys
path = os.path.join(sys.argv[1], ".cursor", "mcp.json")
if not os.path.exists(path):
    sys.exit(0)
servers = sorted((json.load(open(path)).get("mcpServers") or {}))
if servers:
    sys.exit(f"{path} registers {len(servers)} user-scope MCP server(s)"
             f" ({', '.join(servers)}). Cursor has no --strict-mcp-config, so they load"
             " alongside the QA fixture and a prompt could reach a production system."
             " Move them aside for the run.")
PY
  go build -o "$RUN/mcp-fixture" ./qa/mcp-fixture
  python3 - "$RUN/mcp-fixture" "$PROJECT/.cursor/mcp.json" <<'PY'
import json, sys
binary, out = sys.argv[1], sys.argv[2]
json.dump({"mcpServers": {"qa_fixture_alpha": {
    "command": binary, "args": [], "env": {"QA_MCP_SERVER_NAME": "alpha"}}}},
    open(out, "w"), indent=2)
PY
  echo "qa: registered the qa_fixture_alpha MCP server at project scope"
fi

# 5. The workspace. A real git repo, because that is what internal/vcs reads.
#    commit.gpgsign is turned off for this commit: a developer with it on globally
#    gets a passphrase prompt no non-interactive run can answer, and a warm
#    gpg-agent makes the hang intermittent, which is worse.
git -C "$PROJECT" init -q
git -C "$PROJECT" \
  -c user.email=qa@dash0.com -c user.name="Dash0 QA" \
  -c commit.gpgsign=false -c tag.gpgsign=false \
  commit -q --allow-empty -m "qa run $RUN_ID"

# 6. The binary. DASH0_PLUGIN_DATA points the installed bootstrap's cache into the
#    run directory, so the shared cache under ~/.local/state is never written and
#    the developer's live sessions keep the binary they had.
#
#    In release mode the bootstrap downloads the release matching the installed
#    wrapper's VERSION, which on a machine whose install is behind the working
#    tree means the run tests old code. Working-tree mode pre-places a freshly
#    built binary under that same name instead: the shipped wrapper still runs, so
#    the bootstrap's own resolution is exercised, and the Go code under test is
#    this checkout's.
PLUGIN_DATA="$RUN/plugin-data"
OS=$(uname -s | tr '[:upper:]' '[:lower:]')
ARCH=$(uname -m | sed 's/x86_64/amd64/;s/aarch64/arm64/')
mkdir -p "$PLUGIN_DATA/bin"
if [[ "$BINARY_MODE" == "working-tree" ]]; then
  go build -o "$PLUGIN_DATA/bin/cursor-on-event-${VERSION}-${OS}-${ARCH}" ./cmd/cursor-on-event
  BINARY_UNDER_TEST="working tree $(git rev-parse --short HEAD), as v$VERSION"
else
  BINARY_UNDER_TEST="release v$VERSION, downloaded by the installed bootstrap"
fi
echo "qa: under test — $BINARY_UNDER_TEST"

PROMPTS=(--prompt "$PROMPT")
TURNS=1
if [[ -n ${QA_CURSOR_RESUME:-} ]]; then
  # A second turn in the SAME session, which is the only way to exercise anything
  # per-turn: with one turn, "this turn's usage" and "the session's usage" are the
  # same number and a double-count is invisible. The TUI stays open between
  # turns, so this needs no resume flag — it is a second prompt into the session
  # that is already running.
  PROMPTS+=(--prompt "$QA_CURSOR_RESUME")
  TURNS=2
fi

MODEL_ARGS=()
[[ -n ${QA_MODEL:-} ]] && MODEL_ARGS=(--model "$QA_MODEL")

# DASH0_* is stripped from the environment the hooks inherit. The plugin's
# options fall back to DASH0_<key>, so an inherited value from the developer's
# shell could retarget the export or change what the spans carry. DASH0_PLUGIN_DATA
# is the one that is set on purpose, so it is passed after the stripping.
STARTED_AT=$(date -u +%Y-%m-%dT%H:%M:%S.000Z)
# Every row already in the index belongs to an earlier run under this run id, and
# nothing below may read one as evidence about this one. The driver waits on
# sessionStart and counts afterAgentResponse, so a leftover row would make it skip
# waiting, type into a TUI that is not up, and exit 0 on a session it never drove.
INDEX_BASELINE=0
if [[ -f "$RECORD/index.jsonl" ]]; then
  INDEX_BASELINE=$(wc -l <"$RECORD/index.jsonl" | tr -d ' ')
  [[ "$INDEX_BASELINE" == "0" ]] ||
    echo "qa: the run id was used before — ignoring the $INDEX_BASELINE row(s) already recorded"
fi
set +e
env -u DASH0_OTLP_URL -u DASH0_AUTH_TOKEN -u DASH0_DATASET \
    -u DASH0_TEAM_NAME -u DASH0_AGENT_NAME -u DASH0_DEBUG -u DASH0_DEBUG_FILE \
    -u DASH0_OMIT_IO -u DASH0_OMIT_USER_INFO -u DASH0_OMIT_IDENTITY_FALLBACK \
    -u CURSOR_PLUGIN_DATA \
  DASH0_PLUGIN_DATA="$PLUGIN_DATA" \
  CURSOR_PLUGIN_OPTION_OTLP_URL="$OTLP_URL" \
  CURSOR_PLUGIN_OPTION_AUTH_TOKEN="$INGEST_TOKEN" \
  CURSOR_PLUGIN_OPTION_DATASET="$DATASET" \
  CURSOR_PLUGIN_OPTION_TEAM_NAME="dash0-qa" \
  CURSOR_PLUGIN_OPTION_DEBUG="true" \
  CURSOR_PLUGIN_OPTION_DEBUG_FILE="$RUN/plugin-debug.log" \
  python3 qa/tools/qa-cursor-drive.py \
    --project "$PROJECT" \
    --index "$RECORD/index.jsonl" \
    --index-baseline "$INDEX_BASELINE" \
    --tty-log "$RUN/tty.log" \
    "${PROMPTS[@]}" \
    ${MODEL_ARGS[@]+"${MODEL_ARGS[@]}"}
DRIVE_RC=$?
set -e
ENDED_AT=$(date -u +%Y-%m-%dT%H:%M:%S.000Z)
echo "qa: the driver exited $DRIVE_RC"

# 7. The session id, discovered rather than pinned: cursor-agent has no
#    --session-id, so the id Cursor minted is the id gen_ai.conversation.id
#    carries, and the recording is where it is written down. A run whose recording
#    is empty therefore has no session id at all, and stopping here beats writing
#    a manifest nothing can verify.
#
#    A delegating session records MORE than one id, and that is normal rather
#    than a reused run id. Measured 2026-09-01 on qa/runs/probe-cursor-subagent: a
#    Cursor sub-agent runs under its own freshly minted UUID with no field linking
#    it to the parent, and it fires no sessionStart and no sessionEnd — only its
#    own preToolUse and postToolUse. So the main session is identified by
#    sessionStart, not by being the first id seen, and the rest are reported
#    separately. Copilot has the same shape behind a recognisable call_ prefix;
#    Cursor's is indistinguishable from a real session by its id alone.
SESSIONS=$(python3 - "$RECORD/index.jsonl" "$INDEX_BASELINE" <<'PY'
import collections, json, os, sys
path, skip = sys.argv[1], int(sys.argv[2])
events = collections.defaultdict(set)
order = []
if os.path.exists(path):
    for lineno, line in enumerate(open(path)):
        # Rows from an earlier run under the same run id. Reading one would scope
        # this run to a session it never drove.
        if lineno < skip:
            continue
        try:
            row = json.loads(line)
        except json.JSONDecodeError:
            continue
        sid = row.get("session_id")
        if not sid:
            continue
        if sid not in order:
            order.append(sid)
        events[sid].add(row.get("hook_event_name"))
main = [s for s in order if "sessionStart" in events[s]]
others = [s for s in order if s not in main]
# One line, and a separator that is there even when a side is empty. Two
# whitespace-separated fields collapse into one when main is empty, and bash then
# reads a sub-agent id as the main session.
print(f"{','.join(main)}|{','.join(others)}")
PY
)
MAIN_SESSIONS=${SESSIONS%%|*}
SUBAGENT_SESSIONS=${SESSIONS#*|}
if [[ -z "$MAIN_SESSIONS" && -z "$SUBAGENT_SESSIONS" ]]; then
  cat >&2 <<EOF
qa: the recorder captured no session. The run produced no verifiable record.

The hook file
  $PROJECT/.cursor/hooks.json
is what Cursor reads for this workspace. Check that it is there, that
$RUN/record-hook.sh is executable, and that the session started at all — the
terminal is in $RUN/tty.log, and an unauthenticated cursor-agent fires no hook
of any kind.
EOF
  exit 1
fi
if [[ -z "$MAIN_SESSIONS" ]]; then
  echo "qa: the recording holds only sub-agent sessions [$SUBAGENT_SESSIONS] and none" >&2
  echo "    that fired sessionStart, so there is no session to scope the run to." >&2
  exit 1
fi
SESSION_ID=${MAIN_SESSIONS%%,*}
if [[ "$MAIN_SESSIONS" != "$SESSION_ID" ]]; then
  echo "qa: WARNING — $MAIN_SESSIONS each fired sessionStart, so this run's own rows" >&2
  echo "    hold several real sessions; scoping the run to the first. cursor-agent" >&2
  echo "    was started more than once, which the driver does exactly once." >&2
fi
echo "qa: session $SESSION_ID"
if [[ -n "$SUBAGENT_SESSIONS" ]]; then
  echo "qa: $SUBAGENT_SESSIONS ran as a sub-agent — its own id, no sessionStart"
fi

# 8. Cursor's own transcript, copied out of ~/.cursor/projects/<slug>/. It is the
#    run's second channel and the only independent record of what tools ran, so a
#    run that left it behind cannot be re-read. The paths come from the recorded
#    payloads rather than from a guess at the slug, which is Cursor's to change.
TRANSCRIPTS=$(python3 - "$RECORD/index.jsonl" "$RUN" "$SESSION_ID" <<'PY'
import json, os, shutil, sys
index, run, session = sys.argv[1], sys.argv[2], sys.argv[3]
paths = []
for line in open(index):
    try:
        row = json.loads(line)
    except json.JSONDecodeError:
        continue
    if row.get("session_id") != session:
        continue
    for key in ("transcript_path", "agent_transcript_path"):
        path = row.get(key)
        if path and path not in paths:
            paths.append(path)
# The main transcript first, so the copy named transcript.jsonl is the session's
# own and a sub-agent's lands beside it. Cursor names the main one after the
# conversation, which is what makes the two distinguishable.
paths.sort(key=lambda p: (session not in os.path.basename(p), p))
kept = 0
for path in paths:
    if not os.path.exists(path):
        continue
    dest = os.path.join(run, "transcript.jsonl" if kept == 0 else f"transcript-{kept}.jsonl")
    shutil.copy2(path, dest)
    kept += 1
print(kept)
PY
)
echo "qa: kept $TRANSCRIPTS transcript file(s)"

# This run's own rows, so a reused run id does not inflate the figure the report
# reads back.
HOOKS_RECORDED=$(( $(wc -l <"$RECORD/index.jsonl" | tr -d ' ') - INDEX_BASELINE ))
# grep exits 1 on no match after printing 0, and a bare `|| echo 0` would then
# put two numbers in the manifest and make it unparseable.
SPANS_LOGGED=0
[[ -f "$RUN/plugin-debug.log" ]] &&
  SPANS_LOGGED=$(grep -c '\[dash0:trace\]' "$RUN/plugin-debug.log" || true)

cat >"$RUN/manifest.json" <<EOF
{
  "runtime": "cursor",
  "run_id": "$RUN_ID",
  "session_id": "$SESSION_ID",
  "recorded_sessions": "$MAIN_SESSIONS",
  "subagent_sessions": "$SUBAGENT_SESSIONS",
  "prompt": $(printf '%s' "$PROMPT" | python3 -c 'import json,sys; print(json.dumps(sys.stdin.read()))'),
  "started_at": "$STARTED_AT",
  "ended_at": "$ENDED_AT",
  "drive_exit_code": $DRIVE_RC,
  "turns": $TURNS,
  "cursor_version": "$(cursor-agent --version 2>/dev/null | head -1 || echo unknown)",
  "install_mode": "machine registration",
  "registered_script": "$PLUGIN_SCRIPT",
  "registered_events": $REGISTERED_EVENTS,
  "wrapper_matches_shipped": $WRAPPER_MATCHES_SHIPPED,
  "binary_under_test": "$BINARY_UNDER_TEST",
  "model": "${QA_MODEL:-}",
  "otlp_url": "$OTLP_URL",
  "dataset": "$DATASET",
  "plugin_version": "$VERSION",
  "plugin_commit": "$(git rev-parse HEAD)",
  "plugin_dirty": $(git diff --quiet && echo false || echo true),
  "stub_mcp_servers": $([[ "${QA_CURSOR_MCP:-0}" == "1" ]] && echo true || echo false),
  "transcripts": $TRANSCRIPTS,
  "hooks_recorded": $HOOKS_RECORDED,
  "spans_logged": $SPANS_LOGGED
}
EOF

echo "qa: recorded $HOOKS_RECORDED hook invocations, $SPANS_LOGGED span(s) in the debug log"
echo "qa: run written to $RUN"
echo "qa: verify with  qa/tools/qa-compare.py $RUN"

# The manifest is written first, because a failed drive is still evidence and its
# record/, tty.log and plugin-debug.log are how it gets diagnosed. But the run is
# not the stimulus the spec asked for, and it must not read as one: a two-turn run
# that died after the first turn agrees with itself at one chat span and one
# prompt, so the per-turn spec would go green on a session that had one turn.
if [[ "$DRIVE_RC" != "0" ]]; then
  echo "qa: the driver exited $DRIVE_RC, so this run is not a valid stimulus." >&2
  echo "    The artifacts are kept for diagnosis; do not read the comparison as a" >&2
  echo "    result. qa-compare.py reports the same thing as a finding." >&2
  exit "$DRIVE_RC"
fi
