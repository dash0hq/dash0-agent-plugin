#!/usr/bin/env bash
# SPDX-FileCopyrightText: Copyright 2026 Dash0 Inc.
# SPDX-License-Identifier: Apache-2.0
#
# Drive one real Codex session and record everything needed to verify it.
#
# The Claude driver next door runs against the plugin as the machine has it
# installed, because a Claude Code install is managed and QA cannot reconfigure
# it. Codex has no managed configuration, so this driver does the opposite: it
# PROVISIONS a complete install into a throwaway home and runs the session
# against that. What is under test is therefore the shipped install path —
# install-codex.sh, the bootstrap, the binary, the hook registration and its
# reproduced trust — exporting to the real Dash0 target from
# qa/config.local.json.
#
# Two things follow, and both are why this is worth having:
#
#   - Nothing on the machine is mutated. The developer's ~/.codex, their hooks,
#     their credentials and their binary cache are untouched, so there is no
#     equivalent of the Claude driver's binary swap and nothing to restore.
#   - QA owns the configuration, so the plugin's debug log can be turned on. That
#     is a view of what the plugin SENT, which the Claude runtime cannot have.
#
# What it costs: this cannot answer "is the install on this machine configured
# correctly", because it does not use it. See `## Runtimes` in qa/setup.md.
#
# Alongside the plugin's own hooks it registers the QA recorder, for every event
# the plugin acts on, pre-trusted the same way:
#
#   record/events/*.json       every hook payload, byte for byte
#   record/transcripts/*.jsonl the rollout as it stood at each hook
#   record/index.jsonl         the two joined, in wall-clock order
#   rollout.jsonl              the final rollout, which no snapshot is
#   codex-events.jsonl         Codex's own `--json` event stream
#   plugin-debug.log           every span the plugin emitted, as it emitted it
#
# Verify with qa-compare.py, which reads the spans back out of Dash0 and lines
# them up against an expectation computed from the recording.
#
# Usage:
#   qa/tools/qa-session-codex.sh "<prompt>" [run-id]
#   QA_MODEL=gpt-5.1-codex-mini qa/tools/qa-session-codex.sh "..."   # cheap probe
#   QA_CODEX_BINARY=working-tree qa/tools/qa-session-codex.sh "..."  # test unreleased code
#   QA_KEEP_SCRATCH=1 qa/tools/qa-session-codex.sh "..."             # keep the throwaway home
#   QA_CODEX_RESUME="<second prompt>" qa/tools/qa-session-codex.sh "..."  # two turns, one session
#   QA_CODEX_MULTI_AGENT=1 qa/tools/qa-session-codex.sh "..."        # let the model delegate
#   QA_CODEX_SKILL=1 qa/tools/qa-session-codex.sh "..."              # install the qa-echo skill
#
# Auth: OPENAI_API_KEY is used when set. Otherwise pass QA_CODEX_REUSE_LOGIN=1 to
# reuse the machine's `codex login` — read the caution in `## Configure` in
# qa/setup.md first, because Codex refreshes that credential.

set -euo pipefail

ROOT=$(git rev-parse --show-toplevel)
cd "$ROOT"

PROMPT=${1:?usage: qa-session-codex.sh "<prompt>" [run-id]}
RUN_ID=${2:-$(date -u +%Y%m%dT%H%M%SZ)}
BINARY_MODE=${QA_CODEX_BINARY:-release}
SANDBOX=${QA_CODEX_SANDBOX:-workspace-write}
KEEP_SCRATCH=${QA_KEEP_SCRATCH:-0}
BYPASS_TRUST=${QA_CODEX_BYPASS_TRUST:-0}

RUN="$ROOT/qa/runs/$RUN_ID"
PROJECT="$RUN/project"
RECORD="$RUN/record"
# The recorder appends, so a reused run id leaves two sessions in one record/.
# qa-compare.py filters the index by the manifest's session_id, so that is
# survivable; deleting record/ would throw away evidence a spec asked to keep,
# and a delete built from an unvalidated run id can reach outside the run tree.
mkdir -p "$PROJECT" "$RECORD"

for tool in codex go python3 git; do
  command -v "$tool" >/dev/null || { echo "qa: MISSING: $tool" >&2; exit 2; }
done

# The read side and the write side both come from the QA config. Unlike the
# Claude runtime there is no installed target to agree with: this run's target IS
# what the config says, so a mismatch between what the plugin writes and what
# qa-compare.py reads is impossible by construction.
CONFIG="$ROOT/qa/config.local.json"
# Read through a variable rather than straight into `read`: a process
# substitution that fails still leaves `read` successful, so the failure would
# arrive later as an empty endpoint instead of the explanation printed here.
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
             " codex-auth-token-can-ingest proves it before a session is paid for.")
missing = [k for k in ("ingestUrl", "dataset") if not cfg.get(k)]
if missing:
    sys.exit(f"{path} is missing: {', '.join(missing)}")
print(cfg["ingestUrl"], cfg["dataset"], token)
PY
) || { echo "qa: $CONFIG_VALUES" >&2; exit 2; }
read -r OTLP_URL DATASET INGEST_TOKEN <<<"$CONFIG_VALUES"
echo "qa: exporting to $OTLP_URL / $DATASET"

# The throwaway home. Outside qa/runs on purpose: it holds the ingest token and
# a link to a live Codex credential, and run directories get attached to bug
# reports. Only the artifacts that carry neither are copied into the run.
SCRATCH=$(mktemp -d "${TMPDIR:-/tmp}/qa-codex-XXXXXX")
CODEX_HOME_DIR="$SCRATCH/.codex"
mkdir -p "$CODEX_HOME_DIR" "$SCRATCH/state"
cleanup() {
  if [[ "$KEEP_SCRATCH" == "1" ]]; then
    echo "qa: kept the scratch home at $SCRATCH (holds an ingest token — delete it when done)"
  else
    rm -rf "$SCRATCH"
  fi
}
trap cleanup EXIT

# Auth. An API key is the clean path: it is not refreshed, so nothing about the
# machine's own login can be invalidated by this run.
if [[ -n ${OPENAI_API_KEY:-} ]]; then
  printf '%s' "$OPENAI_API_KEY" | CODEX_HOME="$CODEX_HOME_DIR" codex login --with-api-key >/dev/null
  AUTH_SOURCE="OPENAI_API_KEY"
elif [[ "${QA_CODEX_REUSE_LOGIN:-0}" == "1" && -f "$HOME/.codex/auth.json" ]]; then
  # Symlinked rather than copied, so no live credential is duplicated onto disk.
  # Codex refreshes this file; see the caution in setup.md.
  ln -s "$HOME/.codex/auth.json" "$CODEX_HOME_DIR/auth.json"
  AUTH_SOURCE="reused ~/.codex/auth.json"
else
  echo "qa: no Codex auth. Set OPENAI_API_KEY, or pass QA_CODEX_REUSE_LOGIN=1 to reuse" >&2
  echo "    this machine's \`codex login\` — read the auth caution in qa/setup.md first." >&2
  exit 2
fi
echo "qa: auth — $AUTH_SOURCE"

# git config does not travel with a throwaway HOME, and Codex commits nothing,
# but its git calls are quieter with one. Linked, not copied: read-only use.
[[ -f "$HOME/.gitconfig" ]] && ln -s "$HOME/.gitconfig" "$SCRATCH/.gitconfig"

# 1. The recorder, and the hook command that carries its output directory.
go build -o "$RUN/recorder" ./qa/recorder
cat >"$RUN/record-hook.sh" <<EOF
#!/usr/bin/env bash
# Generated by qa/tools/qa-session-codex.sh for run $RUN_ID.
export QA_RECORD_DIR="$RECORD"
exec "$RUN/recorder"
EOF
chmod +x "$RUN/record-hook.sh"

# 2. Register the recorder FIRST, into a config.toml that declares no hooks yet.
#    The trust key embeds the group index, so this block takes 0 and the
#    installer's block, appended next, correctly takes 1. The other way round
#    they would both claim 0 and Codex would silently skip one of them.
go build -o "$RUN/qa-codex-hooks" ./qa/tools/qa-codex-hooks
#    Written through a temporary file: redirecting straight into --config would
#    truncate the file before the tool reads it, and the tool reads it to refuse
#    a config that already declares hooks.
"$RUN/qa-codex-hooks" --command "$RUN/record-hook.sh" --config "$CODEX_HOME_DIR/config.toml" \
  >"$SCRATCH/recorder-hooks.toml"
mv "$SCRATCH/recorder-hooks.toml" "$CODEX_HOME_DIR/config.toml"
RECORDER_EVENTS=$(grep -c '^\[\[hooks\.[A-Za-z]*\]\]$' "$CODEX_HOME_DIR/config.toml")
echo "qa: recording $RECORDER_EVENTS hook events"

# 3. Install the plugin into the throwaway home, exactly as a user would. In
#    working-tree mode the binary and bootstrap are pre-placed at the paths the
#    installer resolves, which it then reuses rather than downloading — the same
#    trick test/e2e/codex_e2e_test.go uses. Unlike the Claude driver's binary
#    swap this touches no shared cache, so it needs no restore and is safe by
#    default rather than opt-in.
VERSION=$(grep '^VERSION=' codex/codex-on-event.sh | cut -d'"' -f2)
OS=$(uname -s | tr '[:upper:]' '[:lower:]')
ARCH=$(uname -m | sed 's/x86_64/amd64/;s/aarch64/arm64/')
INSTALL_ENV=(
  "HOME=$SCRATCH"
  "XDG_STATE_HOME=$SCRATCH/state"
  "DASH0_OTLP_URL=$OTLP_URL"
  "DASH0_AUTH_TOKEN=$INGEST_TOKEN"
  "DASH0_DATASET=$DATASET"
  # Every optional value the installer would otherwise prompt for on /dev/tty
  # must be set, or a non-interactive run blocks forever on a hidden prompt.
  # The name doubles as the way to tell QA spans apart in a shared dataset.
  "DASH0_TEAM_NAME=dash0-qa"
)
if [[ "$BINARY_MODE" == "working-tree" ]]; then
  STATE_BASE="$SCRATCH/state/dash0-agent-plugin/codex"
  mkdir -p "$STATE_BASE/bin"
  go build -o "$STATE_BASE/bin/codex-on-event-${VERSION}-${OS}-${ARCH}" ./cmd/codex-on-event
  install -m 755 codex/codex-on-event.sh "$STATE_BASE/codex-on-event.sh"
  INSTALL_ENV+=("DASH0_VERSION=$VERSION")
  BINARY_UNDER_TEST="working tree $(git rev-parse --short HEAD)"
else
  # Empty rather than unset, so an exported DASH0_VERSION in the developer's
  # shell cannot quietly pin this run to some other release.
  INSTALL_ENV+=("DASH0_VERSION=")
  BINARY_UNDER_TEST="latest release, resolved by install-codex.sh"
fi
# DASH0_PLUGIN_DATA would move the binary cache back out of the throwaway home,
# which is the one variable that could make this run touch the machine.
env -u DASH0_PLUGIN_DATA -u CODEX_PLUGIN_DATA \
  "${INSTALL_ENV[@]}" bash install-codex.sh >"$RUN/install.log" 2>&1 \
  || { echo "qa: install-codex.sh failed; see $RUN/install.log" >&2; exit 2; }
INSTALLED_VERSION=$(grep -o 'using v[0-9.]*' "$RUN/install.log" | head -1 | sed 's/using v//')
echo "qa: installed v${INSTALLED_VERSION:-?} — $BINARY_UNDER_TEST"

# 4. Turn on the plugin's debug log. This is the one channel the Claude runtime
#    cannot have: it is what the plugin SENT, before the wire and before ingest.
#    It is the product's own output, so it is not an independent record — it
#    answers "did the plugin emit this", never "should it have".
python3 - "$SCRATCH/.codex/dash0-agent-plugin.local.md" "$RUN/plugin-debug.log" <<'PY'
import sys
path, log = sys.argv[1], sys.argv[2]
lines = open(path).read().rstrip("\n").split("\n")
assert lines[-1] == "---", f"unexpected config shape in {path}: {lines}"
lines[-1:] = ["debug: true", f'debug_file: "{log}"', "---"]
open(path, "w").write("\n".join(lines) + "\n")
PY

# 4b. The skill fixture, installed where Codex looks for a user's own skills.
#     Opt-in: a session with a skill available carries the whole catalogue in its
#     context, which is not what most runs are measuring, and the catalogue is
#     itself a trap the skill spec has to control for.
if [[ "${QA_CODEX_SKILL:-0}" == "1" ]]; then
  mkdir -p "$SCRATCH/.agents/skills"
  cp -R qa/skill-fixture/qa-echo "$SCRATCH/.agents/skills/"
  echo "qa: installed the qa-echo skill fixture into the throwaway home"
fi

# 5. The workspace. A real git repo, because that is what Codex expects and what
#    internal/vcs reads. No .codex/dash0-agent-plugin.local.md is written here: a
#    project-level config outranks the global one, so it would silently retarget
#    the very install this run just provisioned.
#    commit.gpgsign is turned off for this commit, not because signing is wrong
#    but because it hangs: a developer with `commit.gpgsign = true` globally gets
#    a passphrase prompt no non-interactive run can answer, and the driver blocks
#    forever with no output. It is intermittent, which is worse — a warm
#    gpg-agent cache signs without asking, so the run works until the cache
#    expires. Observed 2026-08-26 after several successful runs the same day.
git -C "$PROJECT" init -q
git -C "$PROJECT" \
  -c user.email=qa@dash0.com -c user.name="Dash0 QA" \
  -c commit.gpgsign=false -c tag.gpgsign=false \
  commit -q --allow-empty -m "qa run $RUN_ID"

MODEL_ARGS=()
[[ -n ${QA_MODEL:-} ]] && MODEL_ARGS=(--model "$QA_MODEL")

# Sub-agents are off by default in Codex 0.149.1 and the model cannot delegate
# without this, so a prompt asking it to is simply answered directly. Opt-in
# rather than always-on: it changes the tool set the model sees, which is not
# what most runs are measuring.
FEATURE_ARGS=()
if [[ "${QA_CODEX_MULTI_AGENT:-0}" == "1" ]]; then
  FEATURE_ARGS=(--enable multi_agent_mode)
  echo "qa: multi-agent mode enabled; the model can spawn sub-agents"
fi
TRUST_ARGS=()
if [[ "$BYPASS_TRUST" == "1" ]]; then
  # Escape hatch for the day Codex changes its trust serialization: the run keeps
  # working, and the e2e canary is what fails. A run that needs this is telling
  # you the reproduced hashes are stale — say so in the report.
  TRUST_ARGS=(--dangerously-bypass-hook-trust)
  echo "qa: WARNING — running with hook trust bypassed, so this run proves nothing about trust"
fi

# One place that knows how to invoke Codex against the throwaway home, called
# once per turn. Everything after the flags is the turn's own argument list, so a
# resume turn passes `resume --last "<prompt>"`: Codex takes the exec flags
# before the subcommand and rejects them after it.
#
# DASH0_* is stripped from the environment Codex hands the hooks. The plugin's
# options fall back to DASH0_<key>, so an inherited value from the developer's
# shell could retarget the export or change what the spans carry.
codex_turn() {
  env -u DASH0_OTLP_URL -u DASH0_AUTH_TOKEN -u DASH0_DATASET -u DASH0_PLUGIN_DATA \
    -u DASH0_TEAM_NAME -u DASH0_AGENT_NAME -u DASH0_DEBUG -u DASH0_DEBUG_FILE \
    -u DASH0_OMIT_IO -u DASH0_OMIT_USER_INFO -u DASH0_OMIT_IDENTITY_FALLBACK \
    HOME="$SCRATCH" CODEX_HOME="$CODEX_HOME_DIR" XDG_STATE_HOME="$SCRATCH/state" \
    codex exec \
    --json \
    --cd "$PROJECT" \
    --sandbox "$SANDBOX" \
    -c 'approval_policy="never"' \
    "${MODEL_ARGS[@]}" \
    ${FEATURE_ARGS[@]+"${FEATURE_ARGS[@]}"} \
    ${TRUST_ARGS[@]+"${TRUST_ARGS[@]}"} \
    "$@" \
    >>"$RUN/codex-events.jsonl" 2>>"$RUN/codex-stderr.log" </dev/null
}

STARTED_AT=$(date -u +%Y-%m-%dT%H:%M:%S.000Z)
set +e
codex_turn "$PROMPT"
CODEX_RC=$?
set -e
echo "qa: codex exited $CODEX_RC"

# A second turn in the SAME session, which is the only way to exercise anything
# per-turn: one exec session is one turn, and a rollout spans both. Codex has no
# flag to pin a session id, so `resume --last` is how the second turn finds the
# first — the throwaway home holds exactly one session, so "last" is unambiguous.
TURNS=1
if [[ -n ${QA_CODEX_RESUME:-} ]]; then
  echo "qa: resuming the session for a second turn"
  set +e
  codex_turn resume --last "$QA_CODEX_RESUME"
  RESUME_RC=$?
  set -e
  TURNS=2
  echo "qa: resumed turn exited $RESUME_RC"
  [[ "$RESUME_RC" != 0 ]] && CODEX_RC=$RESUME_RC
fi
ENDED_AT=$(date -u +%Y-%m-%dT%H:%M:%S.000Z)

# 6. The session id comes from the recording, because that is the id the plugin
#    was handed and therefore the one gen_ai.conversation.id carries. Codex has
#    no --session-id to pin it in advance. It is resolved before the rollout is
#    copied, because picking the right rollout needs it.
SESSION_ID=$(python3 - "$RECORD/index.jsonl" <<'PY'
import json, os, sys
# Always exits 0 and prints a line, empty or not: the caller explains the empty
# case at length, and a non-zero exit here would kill the run before it can.
path = sys.argv[1]
ids = set()
if os.path.exists(path):
    for line in open(path):
        try:
            session = json.loads(line).get("session_id")
        except json.JSONDecodeError:
            continue
        if session:
            ids.add(session)
print(sorted(ids)[0] if len(ids) == 1 else ("MULTIPLE:" + ",".join(sorted(ids)) if ids else ""))
PY
)
if [[ "$SESSION_ID" == MULTIPLE:* ]]; then
  echo "qa: the record holds more than one session (${SESSION_ID#MULTIPLE:})." >&2
  echo "    The run id was reused. Use a fresh one so the record holds one session." >&2
  exit 1
fi
if [[ -z "$SESSION_ID" ]]; then
  cat >&2 <<EOF
qa: the recorder captured no session. The run produced no verifiable record.

The usual cause is hook trust: Codex skips a hook whose trusted_hash does not
match, without a prompt or a log line. Check that
  $CODEX_HOME_DIR/config.toml
carried both blocks (it is deleted with the scratch home unless QA_KEEP_SCRATCH=1),
then re-run with QA_KEEP_SCRATCH=1 and inspect it. QA_CODEX_BYPASS_TRUST=1
isolates trust as the cause: if the recording appears with it, the reproduced
hashes in internal/source/codex/trust.go are stale.
EOF
  exit 1
fi
echo "qa: session $SESSION_ID"

# 7. The final rollout, separate from the per-hook snapshots: Stop fires before
#    Codex writes the last records, so no snapshot is the whole session.
#
#    Selected by session id, NOT by newest. A session that spawns a sub-agent
#    writes a rollout per thread, the sub-agent's is created later, and taking the
#    newest therefore copied the SUB-AGENT's rollout over the session's — which
#    made qa-rollout.py read one sub-agent turn as though it were the whole
#    session. The thread id is in the filename, and for the main thread it is the
#    session id.
#    Plain .jsonl wins over .jsonl.zst. Both can exist for one thread, `sort`
#    puts the compressed one last, and copying that to rollout.jsonl gave
#    qa-rollout.py a zstd blob under a name its compression guard cannot
#    recognise: it reported every line malformed and handed qa-compare.py zero
#    usage as a real difference.
ROLLOUT=$(find "$CODEX_HOME_DIR/sessions" -name "rollout-*-$SESSION_ID.jsonl" -type f 2>/dev/null |
  sort | tail -1)
if [[ -z "$ROLLOUT" ]]; then
  ROLLOUT=$(find "$CODEX_HOME_DIR/sessions" -name "rollout-*-$SESSION_ID.jsonl.zst" -type f 2>/dev/null |
    sort | tail -1)
  [[ -n "$ROLLOUT" ]] && echo "qa: the session's rollout is compressed; usage is unavailable from it"
fi
# The extension is carried over, so a compressed rollout keeps the .zst that
# qa-rollout.py's guard keys on rather than being renamed into a lie.
if [[ -n "$ROLLOUT" ]]; then
  case "$ROLLOUT" in
  *.jsonl.zst) cp "$ROLLOUT" "$RUN/rollout.jsonl.zst" ;;
  *) cp "$ROLLOUT" "$RUN/rollout.jsonl" ;;
  esac
fi
# Every rollout for another thread belongs to a sub-agent this session spawned.
# Kept alongside, named by thread id, because a sub-agent's usage lives only here
# and a spec about delegation needs it.
#
# Excluded by SESSION ID rather than by path: when the main rollout is the
# compressed one, a path comparison against it does not match the plain file, so
# the session's own rollout was copied a second time as a sub-agent's and its
# tokens counted twice.
#    The thread id is matched by its UUID shape. A greedy `.*-` in a sed pattern
#    captures only the last dash-separated group, which never equals the session
#    id, so the guard silently passed the session's own rollout through and its
#    tokens were counted twice.
SUBAGENT_ROLLOUTS=0
while IFS= read -r path; do
  [[ -z "$path" || "$path" == "$ROLLOUT" ]] && continue
  thread=$(basename "$path" | grep -oE '[0-9a-f]{8}(-[0-9a-f]{4}){3}-[0-9a-f]{12}' | tail -1)
  [[ -z "$thread" || "$thread" == "$SESSION_ID" ]] && continue
  cp "$path" "$RUN/rollout-subagent-$thread.jsonl"
  SUBAGENT_ROLLOUTS=$((SUBAGENT_ROLLOUTS + 1))
done < <(find "$CODEX_HOME_DIR/sessions" -name 'rollout-*.jsonl' -type f 2>/dev/null | sort)
[[ "$SUBAGENT_ROLLOUTS" -gt 0 ]] && echo "qa: kept $SUBAGENT_ROLLOUTS sub-agent rollout(s)"

# Codex's own id for the thread, kept for cross-checking only. The event stream's
# shape is Codex's to change, so this is read defensively and never asserted on.
THREAD_ID=$(python3 - "$RUN/codex-events.jsonl" <<'PY'
import json, sys
def walk(node):
    if isinstance(node, dict):
        for key, value in node.items():
            if key in ("thread_id", "session_id", "conversation_id") and isinstance(value, str):
                return value
            found = walk(value)
            if found:
                return found
    elif isinstance(node, list):
        for item in node:
            found = walk(item)
            if found:
                return found
    return None
for line in open(sys.argv[1]):
    try:
        found = walk(json.loads(line))
    except json.JSONDecodeError:
        continue
    if found:
        print(found)
        break
PY
)

HOOKS_RECORDED=$(wc -l <"$RECORD/index.jsonl" | tr -d ' ')
# grep exits 1 on no match after printing 0, and a bare `|| echo 0` would then
# put two numbers in the manifest and make it unparseable.
SPANS_LOGGED=0
[[ -f "$RUN/plugin-debug.log" ]] &&
  SPANS_LOGGED=$(grep -c '\[dash0:trace\]' "$RUN/plugin-debug.log" || true)

cat >"$RUN/manifest.json" <<EOF
{
  "runtime": "codex",
  "run_id": "$RUN_ID",
  "session_id": "$SESSION_ID",
  "codex_thread_id": "${THREAD_ID:-}",
  "prompt": $(printf '%s' "$PROMPT" | python3 -c 'import json,sys; print(json.dumps(sys.stdin.read()))'),
  "started_at": "$STARTED_AT",
  "ended_at": "$ENDED_AT",
  "codex_exit_code": $CODEX_RC,
  "turns": $TURNS,
  "codex_version": "$(codex --version | awk '{print $NF}')",
  "install_mode": "provisioned",
  "binary_under_test": "$BINARY_UNDER_TEST",
  "installed_version": "${INSTALLED_VERSION:-}",
  "auth_source": "$AUTH_SOURCE",
  "sandbox": "$SANDBOX",
  "multi_agent": $([[ "${QA_CODEX_MULTI_AGENT:-0}" == "1" ]] && echo true || echo false),
  "skill_fixture": $([[ "${QA_CODEX_SKILL:-0}" == "1" ]] && echo true || echo false),
  "trust_bypassed": $([[ "$BYPASS_TRUST" == "1" ]] && echo true || echo false),
  "otlp_url": "$OTLP_URL",
  "dataset": "$DATASET",
  "plugin_version": "$VERSION",
  "plugin_commit": "$(git rev-parse HEAD)",
  "plugin_dirty": $(git diff --quiet && echo false || echo true),
  "hooks_recorded": $HOOKS_RECORDED,
  "spans_logged": $SPANS_LOGGED,
  "rollout_source": "${ROLLOUT:-none}",
  "subagent_rollouts": $SUBAGENT_ROLLOUTS
}
EOF

echo "qa: recorded $HOOKS_RECORDED hook invocations, $SPANS_LOGGED span(s) in the debug log"
echo "qa: run written to $RUN"
echo "qa: verify with  qa/tools/qa-compare.py $RUN"
