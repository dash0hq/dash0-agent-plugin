#!/usr/bin/env bash
# SPDX-FileCopyrightText: Copyright 2026 Dash0 Inc.
# SPDX-License-Identifier: Apache-2.0
#
# Drive one real Pi session and record everything needed to verify it.
#
# Pi has no hooks. The plugin is an extension Pi loads in-process, so the
# extension and the helper under test are this working tree's, loaded with
# `-e pi/index.ts` and built into the run directory. Nothing on the machine is
# installed, read or changed except Pi's own login:
#
#   - Pi runs with --no-extensions, so the machine's own extensions, including
#     any installed copy of this plugin, do not load and cannot export twice.
#     QA_PI_SUBAGENTS=1 gets the same isolation from a Pi agent directory
#     (settings, extensions) inside the run instead, because pi-subagents'
#     background children read Pi's settings, not the parent's command line.
#   - An empty plugin configuration file in the run's project shadows the
#     developer's ~/.pi/dash0-agent-plugin.local.md.
#   - Pi runs with a session directory inside the run, so QA sessions do not
#     appear in the user's /resume list.
#   - The plugin's configuration comes from PI_PLUGIN_OPTION_*, which outranks
#     every config file. DASH0_* does not and must not be used here.
#
# The second channel is Pi's own `--mode json` event stream. The extension
# never sees it: it receives Pi's events in-process, and the stream is what Pi
# writes to stdout for the same session. A recorder extension, loaded before
# the plugin, writes every event the plugin is given, so a run can tell "the
# plugin was fed the wrong thing" from "the plugin did the wrong thing".
#
# Records into qa/runs/<run-id>/:
#   stream.jsonl      Pi's own --mode json event stream (the second channel)
#   record.jsonl      every extension event the plugin was given
#   plugin-debug.log  every span the plugin emitted, as it emitted it
#   session-id        the session id, which is also gen_ai.conversation.id
#   sessions/         Pi's session file for this run
#   meta.json, manifest.json
#
# Verify with qa-pi-compare.py.
#
# Usage:
#   qa/tools/qa-session-pi.sh "<prompt>" [run-id]
#   QA_PI_TURNS=<file> qa/tools/qa-session-pi.sh "..."  # one more prompt per line, one session
#   QA_PI_MODEL=<provider/model> ...                    # default: claude-code/claude-haiku-4-5-20251001
#   QA_PI_MCP=1 ...                                     # two stub MCP servers, isolated
#   QA_PI_SKILL=1 ...                                   # the qa-echo skill fixture
#   QA_PI_TOOLS=read,bash ...                           # Pi's --tools allowlist; default read,bash
#   QA_PI_PROVIDER_EXTENSION=<path> ...                 # a provider extension to load, see below
#   QA_PI_SUBAGENTS=1 ...                               # load pi-subagents, isolated; see below
#   QA_PI_SUBAGENTS_CHILD_EXPORT=1 ...                  # also load the plugin into foreground children
#   QA_PI_SUBAGENTS_PACKAGE=<dir> ...                   # default: npm/node_modules/pi-subagents in the agent dir
#
# Auth: Pi's own login for the chosen provider. QA does not provision model
# credentials. PI_CODING_AGENT_DIR is left as the caller has it, because that
# is where the login lives; the extensions and MCP servers in it are disabled.

set -euo pipefail

ROOT=$(git rev-parse --show-toplevel)
cd "$ROOT"

PROMPT=${1:?usage: qa-session-pi.sh "<prompt>" [run-id]}
RUN_ID=${2:-pi-$(date -u +%Y%m%dT%H%M%SZ)}
# The run id is a directory name under qa/runs. A path in it could point the
# run, and its cleanup, at a directory outside qa/runs.
case "$RUN_ID" in
  ''|.|*/*|*..*) echo "run-id must be a plain directory name, not '$RUN_ID'" >&2; exit 2 ;;
esac
RUN_DIR="$ROOT/qa/runs/$RUN_ID"
CONFIG=qa/config.local.json
MODEL=${QA_PI_MODEL:-claude-code/claude-haiku-4-5-20251001}
TOOLS=${QA_PI_TOOLS:-read,bash}

[ -f "$CONFIG" ] || { echo "missing $CONFIG; see '## Configure' in qa/setup.md" >&2; exit 2; }

# The dataset is piDataset, not dataset: the pi arm stays out of the shared
# one, as the amp arm does with ampDataset.
CONFIG_VARS=$(python3 - "$CONFIG" <<'PY'
import json, shlex, sys
c = json.load(open(sys.argv[1]))
for key, var in (("ingestUrl", "QA_OTLP_URL"), ("authToken", "QA_AUTH_TOKEN"),
                 ("piDataset", "QA_DATASET"), ("apiUrl", "QA_API_URL")):
    value = c.get(key)
    if not value:
        sys.exit(f"{sys.argv[1]}: {key} is missing")
    print(f"{var}={shlex.quote(value)}")
PY
)
eval "$CONFIG_VARS"

# A run directory is evidence. A new run never replaces an earlier one.
if [ -e "$RUN_DIR" ]; then
  echo "$RUN_DIR exists; use a new run-id, or move the old run away first" >&2
  exit 2
fi
mkdir -p "$RUN_DIR/sessions" "$RUN_DIR/project" "$RUN_DIR/plugin"

# The plugin under test is this tree's. The extension is copied next to a
# helper built from this tree, exactly as an install lays them out, so the
# extension resolves the helper by its own rule rather than by a QA override.
cp pi/index.ts "$RUN_DIR/plugin/index.ts"
ln -s "$ROOT/pi/node_modules" "$RUN_DIR/plugin/node_modules" 2>/dev/null || true
go build -o "$RUN_DIR/plugin/pi-on-event" ./cmd/pi-on-event
cp qa/recorder/pi/index.ts "$RUN_DIR/recorder.ts"

# The model works in an empty project directory inside the run, so a tool call
# cannot touch the repository and a project .pi/ cannot change the plugin's
# configuration. a.txt gives the read tool something to read.
printf 'qa-pi-fixture\n' > "$RUN_DIR/project/a.txt"
# The helper reads the project's .pi/dash0-agent-plugin.local.md, else
# ~/.pi/dash0-agent-plugin.local.md, and the first file that exists wins as a
# whole. An empty project file thus keeps the developer's own file, with keys
# such as enabled or team_name, out of the run. PI_PLUGIN_OPTION_* below sets
# everything the run needs.
mkdir -p "$RUN_DIR/project/.pi"
printf -- '---\n---\n' > "$RUN_DIR/project/.pi/dash0-agent-plugin.local.md"

PI_ARGS=(--mode json --no-skills --no-prompt-templates
         --no-context-files --no-themes --offline
         --session-dir "$RUN_DIR/sessions"
         --model "$MODEL")

# --no-extensions also drops a provider that is itself an extension, such as
# claude-code. QA_PI_PROVIDER_EXTENSION names one to load anyway; by default it
# is the claude-code extension in the caller's agent directory, when present.
AGENT_DIR=${PI_CODING_AGENT_DIR:-$HOME/.pi/agent}
PROVIDER_EXT=${QA_PI_PROVIDER_EXTENSION:-}
if [ -z "$PROVIDER_EXT" ] && [ -e "$AGENT_DIR/extensions/claude-code" ]; then
  # Python, not readlink -f: older BSD readlink has no -f.
  PROVIDER_EXT=$(python3 -c 'import os,sys; print(os.path.realpath(sys.argv[1]))' "$AGENT_DIR/extensions/claude-code")
fi

SUBAGENTS_VERSION=
if [ "${QA_PI_SUBAGENTS:-0}" = "1" ]; then
  # A background pi-subagents child runs in a detached runner process that
  # loads the extensions in Pi's settings, not the parent's -e paths. So this
  # mode gives Pi an agent directory of its own, inside the run, whose
  # settings name exactly the recorder, the plugin, the provider and
  # pi-subagents. The user's agents, settings and extensions are then not
  # read, by the parent or by any child.
  #
  # The agent directory is also where Pi keeps logins, and QA never copies
  # credentials. So only a provider that brings its own login works here:
  # claude-code, which starts the Claude Code CLI.
  SUBAGENTS_PKG=${QA_PI_SUBAGENTS_PACKAGE:-$AGENT_DIR/npm/node_modules/pi-subagents}
  [ -f "$SUBAGENTS_PKG/package.json" ] || { echo "QA_PI_SUBAGENTS: no pi-subagents at $SUBAGENTS_PKG" >&2; exit 2; }
  [ -n "$PROVIDER_EXT" ] || { echo "QA_PI_SUBAGENTS: needs the claude-code provider extension" >&2; exit 2; }
  SUBAGENTS_VERSION=$(python3 -c 'import json,sys; print(json.load(open(sys.argv[1]))["version"])' "$SUBAGENTS_PKG/package.json")
  mkdir -p "$RUN_DIR/agent"
  python3 - "$RUN_DIR" "$PROVIDER_EXT" "$SUBAGENTS_PKG" "${QA_PI_SUBAGENTS_CHILD_EXPORT:-0}" <<'PY'
import json, sys
run, provider, package, child_export = sys.argv[1:5]
plugin = f"{run}/plugin/index.ts"
settings = {
    "extensions": [f"{run}/recorder.ts", plugin, provider],
    "packages": [package],
    "quietStartup": True,
    "defaultProjectTrust": "no",
    "enableInstallTelemetry": False,
}
if child_export == "1":
    # The pi-subagents setting that loads an extension into foreground
    # children too. The recorder goes with it, so the child's events are
    # recorded as well.
    settings["subagents"] = {"defaultSubagentOnlyExtensions": [f"{run}/recorder.ts", plugin]}
json.dump(settings, open(f"{run}/agent/settings.json", "w"), indent=2)
PY
  printf '{}\n' > "$RUN_DIR/agent/auth.json"
  export PI_CODING_AGENT_DIR="$RUN_DIR/agent"
  # pi-subagents adds these tools, and --tools would filter them out.
  TOOLS="$TOOLS,subagent,bg_wait"
else
  # Pi runs with --no-extensions, so the machine's own extensions, including
  # any installed copy of this plugin, do not load and cannot export twice.
  PI_ARGS+=(--no-extensions -e "$RUN_DIR/recorder.ts" -e "$RUN_DIR/plugin/index.ts")
  [ -n "$PROVIDER_EXT" ] && PI_ARGS+=(-e "$PROVIDER_EXT")
fi

# QA_PI_MCP=1 registers qa/mcp-fixture twice, as qa_fixture_alpha and
# qa_fixture_beta, the same names the other runtimes' MCP specs use. They go in
# a project .pi/mcp.json, and --approve trusts this one throwaway project for
# the run only.
if [ "${QA_PI_MCP:-0}" = "1" ]; then
  go build -o "$RUN_DIR/mcp-fixture" ./qa/mcp-fixture
  mkdir -p "$RUN_DIR/project/.pi"
  python3 - "$RUN_DIR/mcp-fixture" "$RUN_DIR/project/.pi/mcp.json" <<'PY'
import json, sys
binary, out = sys.argv[1], sys.argv[2]
servers = {f"qa_fixture_{n}": {"command": binary, "args": [], "exposure": "direct",
                               "env": {"QA_MCP_SERVER_NAME": n}}
           for n in ("alpha", "beta")}
json.dump({"mcpServers": servers}, open(out, "w"), indent=2)
PY
  # --no-extensions also disables Pi's built-in MCP support, so it is loaded
  # back explicitly. It reads the user's mcp.json too, so the --tools filter
  # is the isolation: a list with an mcp__ entry keeps only the MCP tools it
  # names, and the user's own servers then reach no tool.
  PI_ARGS+=(--approve -e builtin:mcp)
  TOOLS="$TOOLS,mcp__qa_fixture_*"
else
  PI_ARGS+=(--no-mcp)
fi
PI_ARGS+=(--tools "$TOOLS")

if [ "${QA_PI_SKILL:-0}" = "1" ]; then
  mkdir -p "$RUN_DIR/skills"
  cp -R qa/skill-fixture/qa-echo "$RUN_DIR/skills/"
  PI_ARGS+=(--skill "$RUN_DIR/skills/qa-echo")
fi

PROMPTS=("$PROMPT")
if [ -n "${QA_PI_TURNS:-}" ]; then
  [ -f "$QA_PI_TURNS" ] || { echo "QA_PI_TURNS: no such file: $QA_PI_TURNS" >&2; exit 2; }
  while IFS= read -r line || [ -n "$line" ]; do
    [ -n "$line" ] && PROMPTS+=("$line")
  done < "$QA_PI_TURNS"
fi

export QA_PI_RECORD="$RUN_DIR/record.jsonl"
export PI_PLUGIN_OPTION_OTLP_URL="$QA_OTLP_URL"
export PI_PLUGIN_OPTION_AUTH_TOKEN="$QA_AUTH_TOKEN"
# internal/harness ranks a successful keychain lookup above the token. A name
# that resolves to nothing keeps a developer's own keychain token out of QA.
export PI_PLUGIN_OPTION_AUTH_TOKEN_KEYCHAIN_SERVICE=dash0-qa-no-keychain
export PI_PLUGIN_OPTION_DATASET="$QA_DATASET"
export PI_PLUGIN_OPTION_DEBUG=true
export PI_PLUGIN_OPTION_DEBUG_FILE="$RUN_DIR/plugin-debug.log"
export PI_PLUGIN_OPTION_OMIT_IO=true

STARTED=$(date -u +%Y-%m-%dT%H:%M:%SZ)
echo "run: $RUN_DIR" >&2
set +e
# One invocation with every prompt: Pi runs them in order on one session, and
# each settles before the next starts. stdin is closed so nothing waits on it.
(cd "$RUN_DIR/project" && pi "${PI_ARGS[@]}" -p "${PROMPTS[@]}" < /dev/null \
  > "$RUN_DIR/stream.jsonl" 2> "$RUN_DIR/pi-stderr.log")
PI_STATUS=$?
set -e

# The first record of a JSON-mode stream is the session header.
SESSION_ID=$(head -1 "$RUN_DIR/stream.jsonl" | python3 -c '
import json, sys
h = json.loads(sys.stdin.read() or "{}")
sys.exit(0 if h.get("type") == "session" and print(h["id"]) is None else 1)') \
  || { echo "no session header in $RUN_DIR/stream.jsonl" >&2; exit 3; }
printf '%s\n' "$SESSION_ID" > "$RUN_DIR/session-id"
echo "session: $SESSION_ID" >&2

python3 - "$RUN_DIR" "$RUN_ID" "$SESSION_ID" "$STARTED" "$PI_STATUS" \
         "$QA_DATASET" "$QA_API_URL" "$MODEL" "${#PROMPTS[@]}" "$SUBAGENTS_VERSION" \
         "${QA_PI_SUBAGENTS_CHILD_EXPORT:-0}" <<'PY'
import json, sys, datetime
run_dir, run_id, session, started, status, dataset, api_url, model, turns, subagents, child_export = sys.argv[1:12]
finished = datetime.datetime.now(datetime.timezone.utc).isoformat().replace("+00:00", "Z")

def stamp(value):
    """qa-compare.widen parses %Y-%m-%dT%H:%M:%S.%f%z, so the fraction is required."""
    return value if "." in value else value.replace("Z", ".000000Z")

# The token is deliberately absent from both: run-dir-carries-no-real-credential.
json.dump({"runtime": "pi", "run_id": run_id, "session_id": session,
           "started": started, "finished": finished, "pi_exit": int(status),
           "model": model, "prompts": int(turns), "dataset": dataset,
           "api_url": api_url, "pi_subagents": subagents or None,
           "pi_subagents_child_export": bool(subagents) and child_export == "1"}, open(f"{run_dir}/meta.json", "w"), indent=2)
json.dump({"runtime": "pi", "run_id": run_id, "session_id": session,
           "started_at": stamp(started), "ended_at": stamp(finished),
           "dataset": dataset}, open(f"{run_dir}/manifest.json", "w"), indent=2)
PY

echo "pi exit: $PI_STATUS" >&2
echo "verify with: qa/tools/qa-pi-compare.py qa/runs/$RUN_ID" >&2
if [ "$PI_STATUS" -ne 0 ]; then
  echo "ERROR: pi exited $PI_STATUS; the run is incomplete" >&2
  exit "$PI_STATUS"
fi
