#!/usr/bin/env bash
# SPDX-FileCopyrightText: Copyright 2026 Dash0 Inc.
# SPDX-License-Identifier: Apache-2.0
#
# Drive one real Claude Code session and record everything needed to verify it.
#
# The installed plugin is the thing under test. It runs with its own configuration,
# exports to the real Dash0 target it is configured for, and this script does not
# reconfigure it — an earlier design did, and overriding auth_token silently gave
# the installed plugin a 401 so nothing arrived anywhere.
#
# What this adds is a second hook handler that records what the plugin was fed:
#
#   record/events/*.json       every hook payload, byte for byte
#   record/transcripts/*.jsonl the transcript as it stood at each hook
#   record/index.jsonl         the two joined, in wall-clock order
#   claude-result.json         Claude Code's own usage and cost figures
#
# Verify with qa-compare.py, which reads the spans back out of Dash0 and lines
# them up against an expectation computed from the recording.
#
# Usage:
#   qa/tools/qa-session.sh "<prompt>" [run-id]
#   QA_MODEL=haiku qa/tools/qa-session.sh "..."        # cheap probe
#   QA_SWAP_BINARY=1 qa/tools/qa-session.sh "..."      # test the working tree
#   QA_ALLOWED_TOOLS="Bash Read Write" qa/tools/qa-session.sh "..."
#   QA_MCP=1 qa/tools/qa-session.sh "..."               # two stub MCP servers

set -euo pipefail

ROOT=$(git rev-parse --show-toplevel)
cd "$ROOT"

PROMPT=${1:?usage: qa-session.sh "<prompt>" [run-id]}
RUN_ID=${2:-$(date -u +%Y%m%dT%H%M%SZ)}
SWAP_BINARY=${QA_SWAP_BINARY:-0}
USE_MCP=${QA_MCP:-0}
# Word-split on purpose: --allowed-tools takes a variadic list.
# With QA_MCP=1 the default flips to the two stub servers, because the native
# tools are not what such a run is measuring and every extra permitted tool is
# another call the model might make instead.
DEFAULT_TOOLS="Bash Read"
[[ "$USE_MCP" == "1" ]] && DEFAULT_TOOLS="mcp__qa_fixture_alpha mcp__qa_fixture_beta"
read -r -a ALLOWED_TOOLS <<<"${QA_ALLOWED_TOOLS:-$DEFAULT_TOOLS}"
MODEL_ARGS=()
[[ -n ${QA_MODEL:-} ]] && MODEL_ARGS=(--model "$QA_MODEL")

RUN="$ROOT/qa/runs/$RUN_ID"
PROJECT="$RUN/project"
RECORD="$RUN/record"
# The recorder appends, so reusing a run id leaves two sessions in one record/.
# That is fine, and deliberately not cleaned up here. qa-compare.py filters the
# index by the manifest's session_id, which is what actually fixed the bug where
# it counted hooks across both sessions and reported the surplus as missing
# telemetry. Deleting record/ would additionally throw away the evidence every
# spec asks to keep, and a delete built from an unvalidated $RUN_ID can reach
# outside the run tree.
mkdir -p "$PROJECT/.claude" "$RECORD"

go build -o "$RUN/recorder" ./qa/recorder

# Two stub MCP servers, one binary, two config keys. Claude Code derives the
# mcp__<server>__<tool> name a hook sees from the key, so two keys are what make
# "the server attribute is per call, not per session" answerable at all.
#
# --strict-mcp-config is not optional here. Without it the session also loads the
# developer's real connectors — Slack, Linear, Drive — and a QA prompt could
# reach a production system. With it, the only MCP servers in the session are
# these two local processes.
MCP_ARGS=()
if [[ "$USE_MCP" == "1" ]]; then
  go build -o "$RUN/mcp-fixture" ./qa/mcp-fixture
  python3 - "$RUN/mcp-fixture" "$RUN/mcp-config.json" <<'PY'
import json, sys
binary, out = sys.argv[1], sys.argv[2]
servers = {
    f"qa_fixture_{name}": {
        "command": binary,
        "args": [],
        "env": {"QA_MCP_SERVER_NAME": name},
    }
    for name in ("alpha", "beta")
}
json.dump({"mcpServers": servers}, open(out, "w"), indent=2)
print(f"qa: {len(servers)} stub MCP servers: {', '.join(sorted(servers))}")
PY
  MCP_ARGS=(--mcp-config "$RUN/mcp-config.json" --strict-mcp-config)
fi

# Optionally put this working tree's binary where the INSTALLED plugin's
# bootstrap resolves it, so a session tests an unreleased change. This overwrites
# the cache the developer's own sessions use, so the original is restored on exit
# — including on failure. Off by default: a QA run that quietly swaps the binary
# under every other session on the machine is not a safe default.
VERSION=$(grep '^VERSION=' claude/claude-on-event.sh | cut -d'"' -f2)
OS=$(uname -s | tr '[:upper:]' '[:lower:]')
ARCH=$(uname -m | sed 's/x86_64/amd64/;s/aarch64/arm64/')
INSTALLED_BIN="$HOME/.claude/plugins/data/dash0-agent-plugin-dash0/bin/on-event-${VERSION}-${OS}-${ARCH}"
BINARY_UNDER_TEST="installed release $VERSION"
if [[ "$SWAP_BINARY" == "1" ]]; then
  [[ -f "$INSTALLED_BIN" ]] || { echo "qa: no installed binary at $INSTALLED_BIN" >&2; exit 1; }
  cp "$INSTALLED_BIN" "$RUN/installed-binary.bak"
  trap 'cp "$RUN/installed-binary.bak" "$INSTALLED_BIN"; echo "qa: restored the installed binary"' EXIT
  go build -o "$INSTALLED_BIN" ./cmd/claude-on-event/
  BINARY_UNDER_TEST="working tree $(git rev-parse --short HEAD)"
  echo "qa: swapped in the working tree binary; it will be restored on exit"
fi
echo "qa: under test — $BINARY_UNDER_TEST"

# The recorder's hook command. QA_RECORD_DIR is the only thing it needs, and it
# is passed here rather than in settings.json so the settings file stays generic.
cat >"$RUN/record-hook.sh" <<EOF
#!/usr/bin/env bash
# Generated by qa/tools/qa-session.sh for run $RUN_ID.
export QA_RECORD_DIR="$RECORD"
exec "$RUN/recorder"
EOF
chmod +x "$RUN/record-hook.sh"

# Every event the shipped hooks.json registers, so the recording cannot miss an
# event the plugin acts on.
python3 - "$ROOT/claude/hooks.json" "$RUN/record-hook.sh" "$PROJECT/.claude/settings.json" <<'PY'
import json, sys
shipped, hook, out = sys.argv[1], sys.argv[2], sys.argv[3]
events = list(json.load(open(shipped))["hooks"].keys())
json.dump({"hooks": {
    event: [{"hooks": [{"type": "command", "command": hook}]}] for event in events
}}, open(out, "w"), indent=2)
print(f"qa: recording {len(events)} hook events")
PY

# No .claude/dash0-agent-plugin.local.md is written. A project-level config file
# is read by the INSTALLED plugin too, and its auth_token becomes
# CLAUDE_PLUGIN_OPTION_AUTH_TOKEN for both handlers — which is how an earlier
# version of this script sent every probe session to a real ingress with a bogus
# token and got nothing in Dash0.

SESSION_ID=$(uuidgen | tr '[:upper:]' '[:lower:]')
STARTED_AT=$(date -u +%Y-%m-%dT%H:%M:%S.000Z)
echo "qa: session $SESSION_ID in $RUN"

# DASH0_* is stripped from the environment handed to claude. This repo's own .env
# carries live credentials, and cmd/claude-on-event calls dotenv.Load(".env"), so
# an inherited value could retarget the export.
set +e
(
  cd "$PROJECT" &&
    env -u DASH0_OTLP_URL -u DASH0_AUTH_TOKEN -u DASH0_DATASET \
      claude -p "$PROMPT" \
      --session-id "$SESSION_ID" \
      --output-format json \
      "${MODEL_ARGS[@]}" \
      ${MCP_ARGS[@]+"${MCP_ARGS[@]}"} \
      --allowed-tools "${ALLOWED_TOOLS[@]}" \
      >"$RUN/claude-result.json" 2>"$RUN/claude-stderr.log"
)
CLAUDE_RC=$?
set -e
ENDED_AT=$(date -u +%Y-%m-%dT%H:%M:%S.000Z)
echo "qa: claude exited $CLAUDE_RC"

# The final transcript, separate from the per-hook snapshots: the Stop hook fires
# before Claude Code writes the last entries, so no snapshot is the whole session.
# A glob rather than `find`, because the first match is the only match: the name is
# a session UUID, so no second project directory holds a file of that name.
TRANSCRIPT=""
for candidate in "$HOME/.claude/projects/"*/"$SESSION_ID.jsonl"; do
  if [[ -f "$candidate" ]]; then
    TRANSCRIPT="$candidate"
    break
  fi
done
[[ -n "$TRANSCRIPT" ]] && cp "$TRANSCRIPT" "$RUN/transcript.jsonl"

python3 claude/tools/claude-code-usage-audit.py "$SESSION_ID" >"$RUN/audit.txt" 2>&1 || true

# Counted here rather than inside the heredoc. A command substitution that fails
# there yields an empty string, `set -e` does not abort a heredoc expansion, and
# the result is a manifest reading `"hooks_recorded": ,` that no tool can parse.
HOOKS_RECORDED=0
if [[ -f "$RECORD/index.jsonl" ]]; then
  HOOKS_RECORDED=$(wc -l <"$RECORD/index.jsonl" | tr -d ' ')
fi

cat >"$RUN/manifest.json" <<EOF
{
  "run_id": "$RUN_ID",
  "session_id": "$SESSION_ID",
  "prompt": $(printf '%s' "$PROMPT" | python3 -c 'import json,sys; print(json.dumps(sys.stdin.read()))'),
  "started_at": "$STARTED_AT",
  "ended_at": "$ENDED_AT",
  "claude_exit_code": $CLAUDE_RC,
  "claude_version": "$(claude --version | awk '{print $1}')",
  "binary_under_test": "$BINARY_UNDER_TEST",
  "swapped_binary": $([[ "$SWAP_BINARY" == "1" ]] && echo true || echo false),
  "stub_mcp_servers": $([[ "$USE_MCP" == "1" ]] && echo true || echo false),
  "plugin_version": "$VERSION",
  "plugin_commit": "$(git rev-parse HEAD)",
  "plugin_dirty": $(git diff --quiet && echo false || echo true),
  "hooks_recorded": $HOOKS_RECORDED,
  "transcript_source": "${TRANSCRIPT:-none}"
}
EOF

echo "qa: recorded $HOOKS_RECORDED hook invocations"
echo "qa: run written to $RUN"
echo "qa: verify with  qa/tools/qa-compare.py $RUN"
