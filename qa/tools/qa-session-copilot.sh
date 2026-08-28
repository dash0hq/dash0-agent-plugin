#!/usr/bin/env bash
# SPDX-FileCopyrightText: Copyright 2026 Dash0 Inc.
# SPDX-License-Identifier: Apache-2.0
#
# Drive one real GitHub Copilot CLI session and record everything needed to
# verify it.
#
# This is the Codex driver's shape, for the same reason: Copilot has no managed
# configuration to defer to, and mutating the developer's ~/.copilot would
# register the QA recorder for their own live sessions. So the run PROVISIONS a
# complete install into a throwaway home — the real marketplace path, the shipped
# copilot/ package, the real bootstrap — and exports to the Dash0 target from
# qa/config.local.json. Nothing outside the throwaway home is written.
#
# What is different from every other runtime, and the reason this file exists at
# all: Copilot's hooks do not carry the numbers. Tokens, model and the tool
# executions come from Copilot's OWN OpenTelemetry, written to a per-session
# file that the launch environment enables. The plugin reads that file at each
# agentStop. So a Copilot run has two independent inputs rather than one, and the
# recording alone cannot predict a tool span:
#
#   record/            every hook payload, byte for byte — the lifecycle input
#   otel.jsonl         Copilot's native OTel file — the quantitative input
#   copilot-events.jsonl  `--output-format json`, the harness's own figures
#   plugin-debug.log   every span the plugin emitted, as it emitted it
#   install.log        what the marketplace install did
#
# Verify with qa-compare.py, which reads the spans back out of Dash0 and lines
# them up against those records.
#
# Usage:
#   qa/tools/qa-session-copilot.sh "<prompt>" [run-id]
#   QA_MODEL=gpt-5-mini qa/tools/qa-session-copilot.sh "..."          # cheap probe
#   QA_COPILOT_BINARY=working-tree qa/tools/qa-session-copilot.sh "..."  # unreleased code
#   QA_KEEP_SCRATCH=1 qa/tools/qa-session-copilot.sh "..."            # keep the throwaway home
#   QA_COPILOT_RESUME="<second prompt>" qa/tools/qa-session-copilot.sh "..."  # two turns
#   QA_COPILOT_SKILL=1 qa/tools/qa-session-copilot.sh "..."           # install the qa-echo skill
#   QA_COPILOT_NO_OTEL=1 qa/tools/qa-session-copilot.sh "..."         # native OTel off
#
# Auth: COPILOT_GITHUB_TOKEN, GH_TOKEN or GITHUB_TOKEN, in Copilot's own order of
# precedence. `gh auth token` supplies one on a machine with the GitHub CLI
# logged in. The throwaway home carries no stored login, so without one of these
# the session fails at auth before it reaches the model.

set -euo pipefail

ROOT=$(git rev-parse --show-toplevel)
cd "$ROOT"

PROMPT=${1:?usage: qa-session-copilot.sh "<prompt>" [run-id]}
RUN_ID=${2:-$(date -u +%Y%m%dT%H%M%SZ)}
BINARY_MODE=${QA_COPILOT_BINARY:-release}
KEEP_SCRATCH=${QA_KEEP_SCRATCH:-0}

RUN="$ROOT/qa/runs/$RUN_ID"
PROJECT="$RUN/project"
RECORD="$RUN/record"
# The recorder appends, so a reused run id leaves two sessions in one record/.
# qa-compare.py filters the index by the manifest's session_id, so that is
# survivable; deleting record/ would throw away evidence a spec asked to keep.
mkdir -p "$PROJECT" "$RECORD"

for tool in copilot go python3 git uuidgen; do
  command -v "$tool" >/dev/null || { echo "qa: MISSING: $tool" >&2; exit 2; }
done

# The read side and the write side both come from the QA config, as on Codex:
# this run's target IS what the config says, so a mismatch between what the
# plugin writes and what qa-compare.py reads is impossible by construction.
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
    sys.exit(f"{path} has no usable authToken. This runtime provisions the install, so the"
             " same token both ingests and reads back; a token that cannot ingest 401s on"
             " every export and the run reads as total telemetry loss. The preflight check"
             " ingest-token-reaches-the-ingress proves it before a session is paid for.")
missing = [k for k in ("ingestUrl", "dataset") if not cfg.get(k)]
if missing:
    sys.exit(f"{path} is missing: {', '.join(missing)}")
print(cfg["ingestUrl"], cfg["dataset"], token)
PY
) || { echo "qa: $CONFIG_VALUES" >&2; exit 2; }
read -r OTLP_URL DATASET INGEST_TOKEN <<<"$CONFIG_VALUES"
echo "qa: exporting to $OTLP_URL / $DATASET"

# Auth. Copilot's own precedence, so a machine that already exports one of these
# needs nothing else. The throwaway home has no stored login to fall back on.
COPILOT_TOKEN=${COPILOT_GITHUB_TOKEN:-${GH_TOKEN:-${GITHUB_TOKEN:-}}}
AUTH_SOURCE="COPILOT_GITHUB_TOKEN/GH_TOKEN/GITHUB_TOKEN"
if [[ -z "$COPILOT_TOKEN" ]] && command -v gh >/dev/null; then
  COPILOT_TOKEN=$(gh auth token 2>/dev/null || true)
  AUTH_SOURCE="gh auth token"
fi
if [[ -z "$COPILOT_TOKEN" ]]; then
  echo "qa: no GitHub token for Copilot. Export COPILOT_GITHUB_TOKEN (or GH_TOKEN," >&2
  echo "    or GITHUB_TOKEN), or log in with \`gh auth login\`. The throwaway home" >&2
  echo "    carries no stored login, so the session would fail at auth." >&2
  exit 2
fi
echo "qa: auth — $AUTH_SOURCE"

# The throwaway home. Outside qa/runs on purpose: it holds the ingest token and
# the GitHub token, and run directories get attached to bug reports. Only the
# artifacts that carry neither are copied into the run.
SCRATCH=$(mktemp -d "${TMPDIR:-/tmp}/qa-copilot-XXXXXX")
COPILOT_HOME_DIR="$SCRATCH/.copilot"
# The convention path internal/source/copilot.OtelDir() resolves to under this
# HOME. It is deliberately NOT communicated through an env var: Copilot does not
# pass the launch environment to hook processes, so the launcher and the reader
# can only agree on a baked-in path, and a QA run must exercise that agreement
# rather than paper over it with DASH0_COPILOT_OTEL_DIR.
OTEL_DIR="$SCRATCH/.local/state/dash0-agent-plugin/copilot/otel"
OTEL_FILE="$OTEL_DIR/otel-$RUN_ID.jsonl"
mkdir -p "$COPILOT_HOME_DIR/hooks" "$OTEL_DIR"
cleanup() {
  if [[ "$KEEP_SCRATCH" == "1" ]]; then
    echo "qa: kept the scratch home at $SCRATCH (holds two live tokens — delete it when done)"
  else
    rm -rf "$SCRATCH"
  fi
}
trap cleanup EXIT

# git config does not travel with a throwaway HOME. Linked, not copied: read-only
# use, and internal/vcs reads the repository's own config anyway.
[[ -f "$HOME/.gitconfig" ]] && ln -s "$HOME/.gitconfig" "$SCRATCH/.gitconfig"

# 1. The recorder, and the hook command that carries its output directory. The
#    event name is forwarded as an argv, because Copilot's camelCase payloads
#    carry no event-name field — the same contract the plugin's own bootstrap
#    honours.
go build -o "$RUN/recorder" ./qa/recorder
cat >"$RUN/record-hook.sh" <<EOF
#!/usr/bin/env bash
# Generated by qa/tools/qa-session-copilot.sh for run $RUN_ID.
export QA_RECORD_DIR="$RECORD"
exec "$RUN/recorder" "\$@"
EOF
chmod +x "$RUN/record-hook.sh"

# 2. Register the recorder at user scope in the throwaway home. Copilot has no
#    hook-trust mechanism, so unlike Codex there is no index to get right and no
#    silent skip to guard against — a user hook file and the plugin's hooks are
#    simply additive.
#
#    The lifecycle events come from copilot/hooks.json, so the recorder cannot
#    miss an event the plugin acts on. The tool and sub-agent events are added on
#    top: the plugin deliberately ignores them (tool spans come from the native
#    OTel file, and sub-agent hook sessions are dropped), which makes them a
#    second opinion QA gets for free. preToolUse is never registered — it is
#    Copilot's only fail-closed event, and a hook that stumbles there blocks the
#    session's tools.
python3 - "$ROOT/copilot/hooks.json" "$RUN/record-hook.sh" \
    >"$COPILOT_HOME_DIR/hooks/qa-recorder.json" <<'PY'
import json, sys
plugin_hooks, command = sys.argv[1], sys.argv[2]
events = list(json.load(open(plugin_hooks))["hooks"])
# Observed-only, never consumed by the plugin. Kept apart from the list above so
# the two reasons for recording an event stay legible.
events += ["postToolUse", "postToolUseFailure", "subagentStart", "subagentStop"]
hooks = {e: [{"type": "command", "bash": f"{command} {e}", "timeoutSec": 10}]
         for e in dict.fromkeys(events)}
json.dump({"version": 1, "hooks": hooks}, sys.stdout, indent=2)
PY
RECORDER_EVENTS=$(python3 -c "
import json; print(len(json.load(open('$COPILOT_HOME_DIR/hooks/qa-recorder.json'))['hooks']))")
echo "qa: recording $RECORDER_EVENTS hook events"

# 3. Install the plugin into the throwaway home, exactly as a user would: the
#    repository is registered as a marketplace and the plugin installed from it,
#    so the manifest, the camelCase hooks.json, ${PLUGIN_ROOT} resolution and the
#    bootstrap are all the shipped ones. In working-tree mode the binary is
#    pre-placed where the bootstrap looks, which it then reuses rather than
#    downloading.
VERSION=$(grep '^VERSION=' copilot/copilot-on-event.sh | cut -d'"' -f2)
MARKETPLACE=$(python3 -c "
import json; print(json.load(open('.github/plugin/marketplace.json'))['name'])")
{
  echo "# marketplace add $ROOT"
  env HOME="$SCRATCH" COPILOT_HOME="$COPILOT_HOME_DIR" \
    copilot plugin marketplace add "$ROOT" 2>&1
  echo "# plugin install dash0-agent-plugin@$MARKETPLACE"
  env HOME="$SCRATCH" COPILOT_HOME="$COPILOT_HOME_DIR" \
    copilot plugin install "dash0-agent-plugin@$MARKETPLACE" 2>&1
} >"$RUN/install.log" 2>&1 ||
  { echo "qa: the marketplace install failed; see $RUN/install.log" >&2; exit 2; }

# 3b. Materialize a live install, because a resumed turn does not get one.
#
#     Copilot CLI 1.0.81 installs a marketplace whose source is a local directory
#     BY REFERENCE: it serves the plugin from that directory and copies nothing,
#     recording only the marketplace path and the enablement in settings.json.
#     test/e2e already accepts that layout — see `test(copilot): accept a live
#     marketplace install`. What that did not reach is the consequence measured
#     here on 2026-08-31: a live-loaded plugin's hooks fire on a fresh session
#     and NOT on `copilot --resume`. Every hook of the resumed turn is skipped,
#     so a two-turn run reported turn 1 only and read as a per-turn scoping
#     defect in the plugin.
#
#     A marketplace whose source is a GitHub repo is copied into
#     installed-plugins and does fire on resume, which is what a user gets and
#     why this is a harness problem rather than a product one. So the copied
#     layout is reproduced here: the package goes where a copied install puts
#     it, config.json gets the installedPlugins record that names it, and the
#     marketplace source is rewritten to the repo the manifest belongs to. The
#     `plugin install` above still ran first, so a manifest this CLI refuses
#     still fails the run rather than being papered over.
#
#     Nothing is fetched: auto-update is off and the plugin is already at
#     cache_path, so the GitHub source is a label the CLI never dereferences.
PLUGIN_ROOT="$COPILOT_HOME_DIR/installed-plugins/$MARKETPLACE/dash0-agent-plugin"
if [[ ! -d "$PLUGIN_ROOT" ]]; then
  mkdir -p "$PLUGIN_ROOT"
  cp -R "$ROOT/copilot/." "$PLUGIN_ROOT/"
  python3 - "$COPILOT_HOME_DIR" "$PLUGIN_ROOT" "$VERSION" "$MARKETPLACE" <<'PY'
import datetime, json, sys
home, plugin_root, version, marketplace = sys.argv[1:5]
repo = json.load(open("copilot/plugin.json"))["repository"].removeprefix("https://github.com/")

settings_path = f"{home}/settings.json"
try:
    settings = json.load(open(settings_path))
except FileNotFoundError:
    settings = {}
settings.setdefault("extraKnownMarketplaces", {})[marketplace] = {
    "source": {"source": "github", "repo": repo}
}
settings.setdefault("enabledPlugins", {})[f"dash0-agent-plugin@{marketplace}"] = True
json.dump(settings, open(settings_path, "w"), indent=2)

# config.json carries a leading `// User settings` comment when the CLI writes
# it, so it is rebuilt rather than parsed.
json.dump({
    "installedPlugins": [{
        "name": "dash0-agent-plugin",
        "marketplace": marketplace,
        "version": version,
        "installed_at": datetime.datetime.now(datetime.UTC).isoformat().replace("+00:00", "Z"),
        "cache_path": plugin_root,
        "enabled": True,
    }],
    "firstLaunchAt": "2026-03-11T00:00:00.000Z",
}, open(f"{home}/config.json", "w"), indent=2)
PY
  echo "qa: the CLI installed the plugin live; materialized it so a resumed turn runs its hooks"
fi

PLUGIN_DATA="$COPILOT_HOME_DIR/plugin-data/$MARKETPLACE/dash0-agent-plugin"
OS=$(uname -s | tr '[:upper:]' '[:lower:]')
ARCH=$(uname -m | sed 's/x86_64/amd64/;s/aarch64/arm64/')
if [[ "$BINARY_MODE" == "working-tree" ]]; then
  mkdir -p "$PLUGIN_DATA/bin"
  go build -o "$PLUGIN_DATA/bin/copilot-on-event-${VERSION}-${OS}-${ARCH}" ./cmd/copilot-on-event
  BINARY_UNDER_TEST="working tree $(git rev-parse --short HEAD)"
else
  BINARY_UNDER_TEST="release v$VERSION, downloaded by the bootstrap"
fi
echo "qa: installed from the $MARKETPLACE marketplace — $BINARY_UNDER_TEST"

# 4. Configure the install, and turn on its debug log. This is the same channel
#    the Codex runtime has and the Claude one cannot: what the plugin SENT,
#    before the wire and before ingest. It is the product's own output, so it
#    never supplies an expectation — it answers "did the plugin emit this",
#    never "should it have".
#
#    Written GLOBALLY, into the throwaway home. A .copilot/dash0-agent-plugin.local.md
#    under $PROJECT would outrank it and silently retarget the install this run
#    just provisioned; the bootstrap prefers the project file for every
#    registration in the session.
cat >"$COPILOT_HOME_DIR/dash0-agent-plugin.local.md" <<EOF
---
otlp_url: "$OTLP_URL"
auth_token: "$INGEST_TOKEN"
dataset: "$DATASET"
team_name: "dash0-qa"
debug: "true"
debug_file: "$RUN/plugin-debug.log"
---
EOF
chmod 600 "$COPILOT_HOME_DIR/dash0-agent-plugin.local.md"

# 4b. The skill fixture, where Copilot looks for a personal skill. Opt-in: a
#     session with a skill available carries the catalogue in its context, which
#     is not what most runs are measuring.
if [[ "${QA_COPILOT_SKILL:-0}" == "1" ]]; then
  mkdir -p "$COPILOT_HOME_DIR/skills"
  cp -R qa/skill-fixture/qa-echo "$COPILOT_HOME_DIR/skills/"
  echo "qa: installed the qa-echo skill fixture into the throwaway home"
fi

# 5. The workspace. A real git repo, because that is what internal/vcs reads.
#    commit.gpgsign is turned off for this commit: a developer with it on
#    globally gets a passphrase prompt no non-interactive run can answer, and a
#    warm gpg-agent makes the hang intermittent, which is worse.
git -C "$PROJECT" init -q
git -C "$PROJECT" \
  -c user.email=qa@dash0.com -c user.name="Dash0 QA" \
  -c commit.gpgsign=false -c tag.gpgsign=false \
  commit -q --allow-empty -m "qa run $RUN_ID"

# 6. The session id is pinned rather than discovered. Copilot accepts one for a
#    new session, which Codex does not, so the run knows what to query before it
#    has anything to query for. It is still cross-checked against the recording
#    and against Copilot's own `result` event afterwards: the id the plugin was
#    handed is what gen_ai.conversation.id carries, and a disagreement between
#    the three is a finding rather than a detail.
SESSION_ID=$(uuidgen | tr '[:upper:]' '[:lower:]')
echo "qa: session $SESSION_ID"

MODEL_ARGS=()
[[ -n ${QA_MODEL:-} ]] && MODEL_ARGS=(--model "$QA_MODEL")

# Native OTel, which is where every number in a Copilot span comes from. In
# production the launch function written by the dash0-configure skill exports
# these; here the driver is the launcher. QA_COPILOT_NO_OTEL=1 turns it off,
# which is the documented degraded mode: lifecycle chat spans, no usage, no tool
# spans. A spec asserting that degradation needs it; nothing else should use it.
OTEL_ENV=()
if [[ "${QA_COPILOT_NO_OTEL:-0}" == "1" ]]; then
  echo "qa: native OTel is OFF; expect chat spans without usage and no tool spans"
else
  OTEL_ENV=(
    "COPILOT_OTEL_ENABLED=true"
    "COPILOT_OTEL_FILE_EXPORTER_PATH=$OTEL_FILE"
    "OTEL_INSTRUMENTATION_GENAI_CAPTURE_MESSAGE_CONTENT=true"
  )
fi

# One place that knows how to invoke Copilot against the throwaway home, called
# once per turn.
#
# DASH0_* is stripped from the environment Copilot hands the hooks. The plugin's
# options fall back to DASH0_<key>, so an inherited value from the developer's
# shell could retarget the export or change what the spans carry.
# DASH0_COPILOT_OTEL_DIR is stripped for the same reason and one more: it would
# point the reader somewhere other than the convention path, which is the very
# agreement this run exists to exercise.
copilot_turn() {
  env -u DASH0_OTLP_URL -u DASH0_AUTH_TOKEN -u DASH0_DATASET -u DASH0_PLUGIN_DATA \
    -u DASH0_TEAM_NAME -u DASH0_AGENT_NAME -u DASH0_DEBUG -u DASH0_DEBUG_FILE \
    -u DASH0_OMIT_IO -u DASH0_OMIT_USER_INFO -u DASH0_OMIT_IDENTITY_FALLBACK \
    -u DASH0_COPILOT_OTEL_DIR -u COPILOT_PLUGIN_DATA \
    HOME="$SCRATCH" COPILOT_HOME="$COPILOT_HOME_DIR" \
    COPILOT_GITHUB_TOKEN="$COPILOT_TOKEN" \
    ${OTEL_ENV[@]+"${OTEL_ENV[@]}"} \
    copilot \
    --output-format json \
    --allow-all-tools \
    --no-color \
    --no-auto-update \
    -C "$PROJECT" \
    "${MODEL_ARGS[@]}" \
    "$@" \
    >>"$RUN/copilot-events.jsonl" 2>>"$RUN/copilot-stderr.log" </dev/null
}

STARTED_AT=$(date -u +%Y-%m-%dT%H:%M:%S.000Z)
set +e
copilot_turn --session-id "$SESSION_ID" -p "$PROMPT"
COPILOT_RC=$?
set -e
echo "qa: copilot exited $COPILOT_RC"

# A second turn in the SAME session, which is the only way to exercise anything
# per-turn: one prompt-mode invocation is one turn, and the plugin's OTel cursor
# is what has to keep two turns apart.
TURNS=1
if [[ -n ${QA_COPILOT_RESUME:-} ]]; then
  echo "qa: resuming the session for a second turn"
  set +e
  copilot_turn --resume="$SESSION_ID" -p "$QA_COPILOT_RESUME"
  RESUME_RC=$?
  set -e
  TURNS=2
  echo "qa: resumed turn exited $RESUME_RC"
  [[ "$RESUME_RC" != 0 ]] && COPILOT_RC=$RESUME_RC
fi
ENDED_AT=$(date -u +%Y-%m-%dT%H:%M:%S.000Z)

# 7. The native-OTel file, copied out before the scratch home goes. Every file in
#    the directory is taken, not just the one this run named: a resumed turn can
#    rotate the file, and the plugin reads the newest file carrying the
#    conversation rather than a fixed name.
OTEL_FILES=0
for path in "$OTEL_DIR"/*.jsonl; do
  [[ -e "$path" ]] || continue
  if [[ "$OTEL_FILES" == 0 ]]; then
    cp "$path" "$RUN/otel.jsonl"
  else
    cp "$path" "$RUN/otel-$OTEL_FILES.jsonl"
  fi
  OTEL_FILES=$((OTEL_FILES + 1))
done
echo "qa: kept $OTEL_FILES native-OTel file(s)"

# What the recorder and Copilot itself each say the session id was. Both are
# read defensively: a disagreement is reported, never silently resolved, because
# the id is what every read of this run is scoped to.
RECORDED_SESSIONS=$(python3 - "$RECORD/index.jsonl" <<'PY'
import json, os, sys
path = sys.argv[1]
ids = set()
if os.path.exists(path):
    for line in open(path):
        try:
            sid = json.loads(line).get("session_id")
        except json.JSONDecodeError:
            continue
        # A sub-agent runs under a synthetic call_<toolCallId> session that the
        # plugin drops wholesale. Counting it as a session of this run would make
        # every delegating run look like a reused run id.
        if sid and not sid.startswith("call_"):
            ids.add(sid)
print(",".join(sorted(ids)))
PY
)
REPORTED_SESSION=$(python3 - "$RUN/copilot-events.jsonl" <<'PY'
import json, os, sys
path = sys.argv[1]
found = ""
if os.path.exists(path):
    for line in open(path):
        try:
            event = json.loads(line)
        except json.JSONDecodeError:
            continue
        if event.get("type") == "result" and event.get("sessionId"):
            found = event["sessionId"]
print(found)
PY
)
if [[ -z "$RECORDED_SESSIONS" ]]; then
  cat >&2 <<EOF
qa: the recorder captured no session. The run produced no verifiable record.

The hook file
  $COPILOT_HOME_DIR/hooks/qa-recorder.json
is what Copilot reads at startup; it is deleted with the scratch home unless
QA_KEEP_SCRATCH=1. Re-run with QA_KEEP_SCRATCH=1 and check that the file is
there, that $RUN/record-hook.sh is executable, and that the session got past
auth at all — see $RUN/copilot-stderr.log.
EOF
  exit 1
fi
if [[ "$RECORDED_SESSIONS" != "$SESSION_ID" ]]; then
  echo "qa: WARNING — the recording holds session(s) [$RECORDED_SESSIONS], not the pinned" >&2
  echo "    $SESSION_ID. Either --session-id was not honoured or the run id was reused." >&2
fi
if [[ -n "$REPORTED_SESSION" && "$REPORTED_SESSION" != "$SESSION_ID" ]]; then
  echo "qa: WARNING — Copilot reported session $REPORTED_SESSION, not the pinned one." >&2
fi

HOOKS_RECORDED=$(wc -l <"$RECORD/index.jsonl" | tr -d ' ')
# grep exits 1 on no match after printing 0, and a bare `|| echo 0` would then
# put two numbers in the manifest and make it unparseable.
SPANS_LOGGED=0
[[ -f "$RUN/plugin-debug.log" ]] &&
  SPANS_LOGGED=$(grep -c '\[dash0:trace\]' "$RUN/plugin-debug.log" || true)

cat >"$RUN/manifest.json" <<EOF
{
  "runtime": "copilot",
  "run_id": "$RUN_ID",
  "session_id": "$SESSION_ID",
  "recorded_sessions": "$RECORDED_SESSIONS",
  "copilot_reported_session": "$REPORTED_SESSION",
  "prompt": $(printf '%s' "$PROMPT" | python3 -c 'import json,sys; print(json.dumps(sys.stdin.read()))'),
  "started_at": "$STARTED_AT",
  "ended_at": "$ENDED_AT",
  "copilot_exit_code": $COPILOT_RC,
  "turns": $TURNS,
  "copilot_version": "$(copilot --version 2>/dev/null | head -1 | grep -oE '[0-9]+\.[0-9]+\.[0-9]+' || echo unknown)",
  "install_mode": "provisioned",
  "marketplace": "$MARKETPLACE",
  "binary_under_test": "$BINARY_UNDER_TEST",
  "auth_source": "$AUTH_SOURCE",
  "native_otel": $([[ "${QA_COPILOT_NO_OTEL:-0}" == "1" ]] && echo false || echo true),
  "otel_files": $OTEL_FILES,
  "skill_fixture": $([[ "${QA_COPILOT_SKILL:-0}" == "1" ]] && echo true || echo false),
  "model": "${QA_MODEL:-}",
  "otlp_url": "$OTLP_URL",
  "dataset": "$DATASET",
  "plugin_version": "$VERSION",
  "plugin_commit": "$(git rev-parse HEAD)",
  "plugin_dirty": $(git diff --quiet && echo false || echo true),
  "hooks_recorded": $HOOKS_RECORDED,
  "spans_logged": $SPANS_LOGGED
}
EOF

echo "qa: recorded $HOOKS_RECORDED hook invocations, $SPANS_LOGGED span(s) in the debug log"
echo "qa: run written to $RUN"
echo "qa: verify with  qa/tools/qa-compare.py $RUN"
