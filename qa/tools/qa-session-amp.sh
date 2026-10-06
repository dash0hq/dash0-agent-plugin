#!/usr/bin/env bash
# SPDX-FileCopyrightText: Copyright 2026 Dash0 Inc.
# SPDX-License-Identifier: Apache-2.0
#
# Drive one real Amp session and record everything needed to verify it.
#
# Amp has no shell hooks. The plugin is a TypeScript bridge that Amp loads
# in-process from ~/.config/amp/plugins/dash0/, so there is no wrapper to
# intercept and no hook payload on disk to record. Two consequences shape this
# driver, and both are the reason it looks unlike the other four:
#
#   - What is under test is the install as the machine has it, because the
#     install is a hand-built directory copy rather than a marketplace artifact
#     and copying it elsewhere would test the copy. `amp-installed-plugin-
#     matches-the-tree` in qa/setup.md is what makes that safe: it refuses to
#     run against a stale build.
#   - QA still owns the *configuration*, because AMP_PLUGIN_OPTION_* outranks
#     the machine's ~/.amp/dash0-agent-plugin.local.md. So the session exports
#     to the QA dataset with the QA token, the developer's own file is never
#     read for those values and never edited, and the plugin's debug log can be
#     turned on — a view of what the plugin SENT.
#
# The second channel is Amp's own --stream-json, which is independent of the
# plugin. `amp threads export` is NOT: the plugin reads that itself to build
# usage, so agreement there would prove a faithful copy rather than a correct
# measurement.
#
# Records into qa/runs/<run-id>/:
#   stream.jsonl      Amp's own --stream-json event stream (the second channel)
#   plugin-debug.log  every span the plugin emitted, as it emitted it
#   thread-id         the thread id, which is also gen_ai.conversation.id
#   thread-export.json  `amp threads export`, for usage cross-reads only
#   meta.json         prompt, dataset, timings, and the exact amp invocation
#
# Verify with qa-amp-compare.py, which reads the spans back out of Dash0 and
# lines them up against an expectation computed from stream.jsonl.
#
# Usage:
#   qa/tools/qa-session-amp.sh "<prompt>" [run-id]
#   QA_AMP_RESUME="<second prompt>" qa/tools/qa-session-amp.sh "..."  # two turns, one thread
#   QA_AMP_TURNS=<file> qa/tools/qa-session-amp.sh "..."  # one prompt per line, one thread
#   QA_AMP_USAGE=1 qa/tools/qa-session-amp.sh "..."   # opt into per-model usage export
#   QA_AMP_MCP=1 qa/tools/qa-session-amp.sh "..."     # two stub MCP servers, isolated
#   QA_AMP_SKILL=1 qa/tools/qa-session-amp.sh "..."   # the qa-echo skill fixture
#   QA_AMP_EXECUTOR=orb qa/tools/qa-session-amp.sh "..."  # run in an orb, see setup.md
#
# Auth: the machine's own `amp login`. QA does not provision Amp credentials.

set -euo pipefail

ROOT=$(git rev-parse --show-toplevel)
cd "$ROOT"

PROMPT=${1:?usage: qa-session-amp.sh "<prompt>" [run-id]}
RUN_ID=${2:-amp-$(date -u +%Y%m%dT%H%M%SZ)}
RUN_DIR="qa/runs/$RUN_ID"
CONFIG=qa/config.local.json

[ -f "$CONFIG" ] || { echo "missing $CONFIG; see '## Configure' in qa/setup.md" >&2; exit 2; }

# The dataset is read from ampDataset, not dataset: the amp arm deliberately
# stays out of the shared one. See '### Amp writes to its own dataset' in setup.md.
eval "$(python3 - "$CONFIG" <<'PY'
import json, shlex, sys
c = json.load(open(sys.argv[1]))
for key, var in (("ingestUrl", "QA_OTLP_URL"), ("authToken", "QA_AUTH_TOKEN"),
                 ("ampDataset", "QA_DATASET"), ("apiUrl", "QA_API_URL")):
    value = c.get(key)
    if not value:
        sys.exit(f"{sys.argv[1]}: {key} is missing")
    print(f"{var}={shlex.quote(value)}")
PY
)"

mkdir -p "$RUN_DIR"

# The recorder is a second Amp plugin bound to the same four events, installed
# project-scoped so the developer's ~/.config/amp/plugins/ is untouched. It is
# what makes the bridge's INPUT observable: without it a wrong span cannot be
# told apart from a wrong event, and that distinction is what found the
# answering-message defect. QA_AMP_RECORD is what arms it; unset, it is inert.
mkdir -p .amp/plugins/qa-recorder
cp qa/recorder/amp/index.ts .amp/plugins/qa-recorder/index.ts
# Removed on the way out. Left behind it is gitignored, so `git status` does not
# show it, and every later Amp session in this checkout loads it.
trap 'rm -rf "$ROOT/.amp/plugins/qa-recorder"' EXIT
export QA_AMP_RECORD="$PWD/$RUN_DIR/record.jsonl"

# --plugin-ready-timeout is OFF unless this flag is passed. Without it Amp can
# start the turn before the plugin has loaded and skip agent.start/agent.end
# entirely, which produces a healthy session and zero spans. Never remove it.
AMP_ARGS=(--plugin-ready-timeout 10 --stream-json --no-archive-after-execute)
[ "${QA_AMP_EXECUTOR:-local}" = "local" ] || AMP_ARGS+=(--executor "$QA_AMP_EXECUTOR")

# QA_AMP_MCP=1 registers qa/mcp-fixture twice, as qa_fixture_alpha and
# qa_fixture_beta, the same two names the claude and cursor MCP specs use, so a
# result here is comparable with theirs.
#
# --mcp-config MERGES with the settings file rather than replacing it, and amp
# has no --strict-mcp-config, so isolation comes from --settings-file instead:
# the run gets a QA-owned settings file and the developer's real connectors are
# not in it. QA_AMP_SKILL=1 uses that same file for amp.skills.path.
SETTINGS="$RUN_DIR/settings.json"

if [ "${QA_AMP_MCP:-0}" = "1" ]; then
  go build -o "$RUN_DIR/mcp-fixture" ./qa/mcp-fixture
  python3 - "$PWD/$RUN_DIR/mcp-fixture" "$RUN_DIR/mcp-config.json" <<'PY'
import json, sys
binary, out = sys.argv[1], sys.argv[2]
servers = {f"qa_fixture_{n}": {"command": binary, "args": [],
                               "env": {"QA_MCP_SERVER_NAME": n}}
           for n in ("alpha", "beta")}
# Amp's --mcp-config takes the server map FLAT, unlike Claude Code's
# {"mcpServers": {...}} and unlike amp's own settings key. A wrapper of either
# shape is rejected with "Invalid MCP server configuration".
json.dump(servers, open(out, "w"), indent=2)
print(f"qa: {len(servers)} stub MCP servers: {', '.join(sorted(servers))}",
      file=sys.stderr)
PY
  AMP_ARGS+=(--mcp-config "$PWD/$RUN_DIR/mcp-config.json")
fi

if [ "${QA_AMP_SKILL:-0}" = "1" ]; then
  mkdir -p "$RUN_DIR/skills"
  cp -R qa/skill-fixture/qa-echo "$RUN_DIR/skills/"
fi

if [ "${QA_AMP_MCP:-0}" = "1" ] || [ "${QA_AMP_SKILL:-0}" = "1" ]; then
  python3 - "$SETTINGS" "$PWD/$RUN_DIR/skills" "${QA_AMP_SKILL:-0}" <<'PY'
import json, sys
out, skills, want_skills = sys.argv[1:4]
# An empty mcpServers is the isolation: --mcp-config merges into whatever this
# file declares, so declaring none means the run's only servers are QA's.
settings = {"amp.mcpServers": {}}
if want_skills == "1":
    settings["amp.skills.path"] = skills
json.dump(settings, open(out, "w"), indent=2)
PY
  AMP_ARGS+=(--settings-file "$PWD/$SETTINGS")
fi

# QA owns the plugin's configuration even though it does not own the install.
# AMP_PLUGIN_OPTION_* beats the machine's file; DASH0_* does not, and using it
# would silently leave spans in the developer's own dataset.
export AMP_PLUGIN_OPTION_OTLP_URL="$QA_OTLP_URL"
export AMP_PLUGIN_OPTION_AUTH_TOKEN="$QA_AUTH_TOKEN"
export AMP_PLUGIN_OPTION_DATASET="$QA_DATASET"
export AMP_PLUGIN_OPTION_DEBUG=true
export AMP_PLUGIN_OPTION_DEBUG_FILE="$PWD/$RUN_DIR/plugin-debug.log"
export AMP_PLUGIN_OPTION_OMIT_IO=true
[ "${QA_AMP_USAGE:-0}" = "1" ] && export AMP_PLUGIN_OPTION_EXPORT_USAGE=true

STARTED=$(date -u +%Y-%m-%dT%H:%M:%SZ)
echo "run: $RUN_DIR" >&2
echo "prompt: $PROMPT" >&2

set +e
# stdin is pinned to /dev/null: `-x [message]` falls back to reading the prompt
# from stdin, so an inherited stdin that never closes makes amp exit with
# "Timeout while reading from stdin" and record nothing.
amp "${AMP_ARGS[@]}" -x "$PROMPT" < /dev/null \
  > "$RUN_DIR/stream.jsonl" 2> "$RUN_DIR/amp-stderr.log"
AMP_STATUS=$?
set -e

# The thread id is not printed as a field of its own; it rides on every stream
# event. Amp has no --session-id, so it can only be discovered, never pinned.
THREAD_ID=$(python3 - "$RUN_DIR/stream.jsonl" <<'PY'
import json, sys
for line in open(sys.argv[1], encoding="utf-8", errors="replace"):
    line = line.strip()
    if not line:
        continue
    try:
        event = json.loads(line)
    except json.JSONDecodeError:
        continue
    for key in ("session_id", "thread_id", "threadID", "threadId"):
        value = event.get(key)
        if isinstance(value, str) and value.startswith("T-"):
            print(value)
            sys.exit(0)
sys.exit(1)
PY
) || { echo "could not find a thread id in $RUN_DIR/stream.jsonl" >&2; exit 3; }
printf '%s\n' "$THREAD_ID" > "$RUN_DIR/thread-id"
echo "thread: $THREAD_ID" >&2

# A conversation, not a single exchange. QA_AMP_RESUME is one follow-up;
# QA_AMP_TURNS is a file of them, one prompt per line, run in order on the same
# thread. Both go through `amp threads continue`, which is the only way to get a
# real multi-turn thread: `-x` archives the thread on exit unless
# --no-archive-after-execute is passed, and piping several lines into one `amp`
# invocation feeds them as ONE prompt rather than as several turns.
RESUMES=()
[ -n "${QA_AMP_RESUME:-}" ] && RESUMES+=("$QA_AMP_RESUME")
if [ -n "${QA_AMP_TURNS:-}" ]; then
  [ -f "$QA_AMP_TURNS" ] || { echo "QA_AMP_TURNS: no such file: $QA_AMP_TURNS" >&2; exit 2; }
  while IFS= read -r line; do
    [ -n "$line" ] && RESUMES+=("$line")
  done < "$QA_AMP_TURNS"
fi

for prompt in ${RESUMES+"${RESUMES[@]}"}; do
  echo "turn: $prompt" >&2
  set +e
  amp threads continue "$THREAD_ID" "${AMP_ARGS[@]}" -x "$prompt" < /dev/null \
    >> "$RUN_DIR/stream.jsonl" 2>> "$RUN_DIR/amp-stderr.log"
  status=$?
  set -e
  # Keep the first failure rather than the last, so a late success cannot mask
  # an earlier dropped turn.
  [ "$AMP_STATUS" -eq 0 ] && AMP_STATUS=$status
done

amp threads export "$THREAD_ID" > "$RUN_DIR/thread-export.json" 2>/dev/null || true

# Two files from one pass. meta.json is this arm's own record; manifest.json is
# the shape qa-attrs.py reads, deliberately the same one the other four runtimes
# write, which is why the attribute-surface check needs no amp-specific port.
python3 - "$RUN_DIR" "$RUN_ID" "$THREAD_ID" "$PROMPT" "$STARTED" "$AMP_STATUS" \
         "$QA_DATASET" "$QA_API_URL" "${QA_AMP_RESUME:-}" "${QA_AMP_EXECUTOR:-local}" \
         "${QA_AMP_USAGE:-0}" <<'PY'
import json, sys, datetime
(run_dir, run_id, thread, prompt, started, status, dataset, api_url,
 resume, executor, usage) = sys.argv[1:12]

finished = (datetime.datetime.now(datetime.timezone.utc)
            .isoformat().replace("+00:00", "Z"))


def stamp(value):
    """qa-compare.widen parses %Y-%m-%dT%H:%M:%S.%f%z, so the fraction is not
    optional. `date -u` cannot emit one portably on macOS, so add it here."""
    return value if "." in value else value.replace("Z", ".000000Z")


def write(name, payload):
    json.dump(payload, open(f"{run_dir}/{name}", "w"), indent=2)


# The token is deliberately absent from both:
# run-dir-carries-no-real-credential.
write("meta.json", {
    "runtime": "amp", "run_id": run_id, "thread_id": thread,
    "prompt": prompt, "resume_prompt": resume or None,
    "started": started, "finished": finished,
    "amp_exit": int(status), "executor": executor,
    "export_usage": usage == "1",
    "dataset": dataset, "api_url": api_url,
    "invocation": "amp --plugin-ready-timeout 10 --stream-json "
                  "--no-archive-after-execute -x <prompt>",
})
write("manifest.json", {
    "runtime": "amp", "run_id": run_id,
    "session_id": thread,
    "started_at": stamp(started),
    "ended_at": finished,
    "dataset": dataset,
})
PY

echo "amp exit: $AMP_STATUS" >&2
[ "$AMP_STATUS" -eq 0 ] || echo "WARNING: amp exited non-zero; the run may be incomplete" >&2
echo "verify with: qa/tools/qa-amp-compare.py $RUN_DIR" >&2
