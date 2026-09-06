#!/usr/bin/env bash
# SPDX-FileCopyrightText: Copyright 2026 Dash0 Inc.
# SPDX-License-Identifier: Apache-2.0
#
# Live-session tests for the OpenCode plugin (L3). Every other layer feeds the
# pipeline events we recorded earlier; this one drives the real `opencode`
# binary against a scripted model and asserts the spans that reach a collector.
# It is the only layer that can catch the plugin failing to load, a hook that
# stopped firing, or an OpenCode upgrade renaming a field.
#
# Usage: test/live/opencode/run.sh
# Requires: opencode, node, go, make, jq, curl. No model credentials and no
# network beyond localhost.
set -euo pipefail
# shellcheck source=test/contracts/lib.sh
source "$(cd "$(dirname "${BASH_SOURCE[0]}")/../../contracts" && pwd)/lib.sh"

# Resolved against the real HOME before any sandbox replaces it. Reusing the
# developer's cache avoids a models.dev fetch per session, which makes
# OpenCode's first start hang well past any sane timeout on a cold cache.
CACHE_DIR="${OPENCODE_LIVE_CACHE:-$HOME/.cache}"
# The scripted model and MCP servers live with the capture harness that first
# needed them. One scripted turn, one place to change it: a second copy here
# would drift from the fixture the golden tests replay.
CAPTURE="$REPO/test/capture/opencode"
LLM_PORT="${MOCK_LLM_PORT:-8817}"
OTLP="http://localhost:4319"

command -v opencode >/dev/null 2>&1 || skip_or_fail "the opencode CLI is not installed"
for t in node jq curl; do
  command -v "$t" >/dev/null 2>&1 || skip_or_fail "$t is not installed"
done

WRAPPER="$REPO/opencode/opencode-on-event.sh"
BUNDLE="$REPO/opencode/dist/dash0-opencode-plugin.js"
VERSION=$(grep '^VERSION=' "$WRAPPER" | sed 's/VERSION="//;s/"//')

echo "== building the binary and the plugin bundle =="
BIN_SRC="$(mktemp -d)/opencode-on-event"
make -C "$REPO" build-binary PKG=./cmd/opencode-on-event OUT="$BIN_SRC" >/dev/null
( cd "$REPO/opencode" && ./build.sh >/dev/null )

start_mock_otlp

echo "== the scripted model server returns the scripted response =="
# On a port of its own, then killed: the server numbers its usage per request,
# so probing the one the session uses would shift every token count below.
probe_port=$((LLM_PORT + 1))
MOCK_LLM_PORT="$probe_port" node "$CAPTURE/mock-llm.mjs" & probe_pid=$!
for _ in $(seq 1 40); do
  curl -sf -m 1 "http://127.0.0.1:$probe_port/v1/models?probe=1" >/dev/null 2>&1 && break
  sleep 0.25
done
scripted=$(curl -sf -m 5 -X POST "http://127.0.0.1:$probe_port/v1/chat/completions" \
  -H 'content-type: application/json' \
  -d '{"model":"mock-model","messages":[{"role":"user","content":"go"}],
       "tools":[{"type":"function","function":{"name":"read"}},
                {"type":"function","function":{"name":"task"}}]}')
kill "$probe_pid" 2>/dev/null || true
fail=0
echo "$scripted" | jq -e '.choices[0].message.tool_calls[0].function.name == "read"' >/dev/null \
  || { echo "ERROR: the scripted server did not return the first scripted tool call"; fail=1; }
echo "$scripted" | jq -e '.usage.completion_tokens_details.reasoning_tokens == 5' >/dev/null \
  || { echo "ERROR: the scripted server reported no reasoning tokens"; fail=1; }
echo "$scripted" | jq -e '.usage.prompt_tokens_details.cached_tokens == 7' >/dev/null \
  || { echo "ERROR: the scripted server reported no cached prompt tokens"; fail=1; }
[ "$fail" -eq 0 ] || exit 1
echo "PASS: the scripted model server answers deterministically"

# session TOKEN [CONFIG_LINES] — one full `opencode run` in a sandbox of its own,
# with the plugin installed from this checkout and the given extra config keys.
# Echoes the sandbox directory; the caller reads opencode.log from it.
#
# Sets SESSION_EXIT to opencode's exit status rather than failing, because the
# fail-open contracts below assert on it.
SESSION_EXIT=0
session() {
  local token="$1" extra="${2:-}" sandbox home project
  sandbox="$(mktemp -d)"; home="$sandbox/home"; project="$sandbox/project"
  mkdir -p "$home/.config/opencode/plugin" "$home/.local/share" "$project"
  printf 'live fixture project\n' > "$project/README.md"

  cp "$BUNDLE" "$home/.config/opencode/plugin/dash0-opencode-plugin.js"
  cp "$WRAPPER" "$home/.config/opencode/plugin/opencode-on-event.sh"
  chmod +x "$home/.config/opencode/plugin/opencode-on-event.sh"

  {
    echo "---"
    echo "otlp_url: \"$OTLP\""
    echo "auth_token: \"$token\""
    echo "dataset: \"opencode-live\""
    [ -n "$extra" ] && printf '%s\n' "$extra"
    echo "---"
  } > "$home/.config/opencode/dash0-agent-plugin.local.md"

  jq --arg base "http://127.0.0.1:$LLM_PORT/v1" \
     --arg mcp "$CAPTURE/mock-mcp.mjs" \
     --arg plugin "$home/.config/opencode/plugin/dash0-opencode-plugin.js" \
     '.provider.mock.options.baseURL = $base
      | .mcp.capture.command = ["node", $mcp]
      | .plugin = [$plugin]' \
     "$CAPTURE/opencode.json" > "$home/.config/opencode/opencode.json"

  # The wrapper resolves its binary by version and would otherwise download it
  # from a published release. Seeding the local build keeps the run offline and
  # tests the code in this checkout rather than the last one that shipped.
  local bindir="$sandbox/state/dash0-agent-plugin/opencode/bin"
  mkdir -p "$bindir"
  cp "$BIN_SRC" "$bindir/opencode-on-event-${VERSION}-$(os_arch)"

  SESSION_EXIT=0
  (
    cd "$project"
    HOME="$home" \
    XDG_CONFIG_HOME="$home/.config" \
    XDG_DATA_HOME="$home/.local/share" \
    XDG_STATE_HOME="$sandbox/state" \
    XDG_CACHE_HOME="$CACHE_DIR" \
      opencode run --print-logs --model mock/mock-model \
        "Read the readme, then read a missing file, then call the capture echo tool, then delegate a sub-task." \
        < /dev/null > "$sandbox/opencode.log" 2>&1
  ) || SESSION_EXIT=$?

  printf '%s\n' "$sandbox"
}

MOCK_LLM_PORT="$LLM_PORT" node "$CAPTURE/mock-llm.mjs" & _bg_pids+=("$!")
for _ in $(seq 1 40); do
  curl -sf -m 1 "http://127.0.0.1:$LLM_PORT/v1/models?probe=1" >/dev/null 2>&1 && break
  sleep 0.25
done

echo "== a real opencode session exports its spans =="
SANDBOX=$(session live-main)
[ "$SESSION_EXIT" -eq 0 ] || { echo "ERROR: opencode run exited $SESSION_EXIT"; tail -40 "$SANDBOX/opencode.log"; exit 1; }
sleep 2

# spans TOKEN — every span from every OTLP request that carried that token, as
# one JSON array.
spans() {
  curl -s "$OTLP/requests" | jq --arg auth "Bearer $1" \
    '[.requests[] | select(.auth == $auth) | .body | fromjson
      | .resourceSpans[].scopeSpans[].spans[]]'
}

SPANS=$(spans live-main)
COUNT=$(echo "$SPANS" | jq 'length')
[ "$COUNT" -gt 0 ] || { echo "ERROR: the live session exported no spans at all"; tail -40 "$SANDBOX/opencode.log"; exit 1; }
echo "$SPANS" | jq -r '.[] | "\(.name)\t\(.spanId)\t\(.parentSpanId)"' | column -t
# Kept for the failure case: an assertion below that fires is almost always
# about one attribute, and the run that produced it is gone by then.
echo "$SPANS" > "$SANDBOX/spans.json"
echo "   (full spans: $SANDBOX/spans.json)"

fail=0

echo "-- span names"
for want in chat execute_tool invoke_agent; do
  [ "$(echo "$SPANS" | jq --arg n "$want" '[.[] | select(.name | startswith($n))] | length')" -ge 1 ] \
    || { echo "ERROR: no span whose name starts with '$want'"; fail=1; }
done
# Anything outside the three span kinds the pipeline documents is a regression,
# not a bonus: it means an event reached a code path this layer does not model.
UNKNOWN=$(echo "$SPANS" | jq -r '[.[] | select((.name | startswith("chat")) or (.name | startswith("execute_tool")) or (.name | startswith("invoke_agent")) | not) | .name] | unique | join(",")')
[ -z "$UNKNOWN" ] || { echo "ERROR: unexpected span names exported: $UNKNOWN"; fail=1; }

echo "-- one trace, no orphans"
[ "$(echo "$SPANS" | jq '[.[].traceId] | unique | length')" -eq 1 ] \
  || { echo "ERROR: the session's spans are spread over more than one trace"; fail=1; }
ORPHANS=$(echo "$SPANS" | jq -r '[.[].spanId] as $known
  | [.[] | select(.parentSpanId != "" and (.parentSpanId | IN($known[]) | not)) | .name] | join(",")')
[ -z "$ORPHANS" ] || { echo "ERROR: spans reference a parent that was never exported: $ORPHANS"; fail=1; }

echo "-- the chat span is the turn's root and carries the usage"
CHAT=$(echo "$SPANS" | jq '[.[] | select(.name | startswith("chat"))] | first')
[ "$(echo "$CHAT" | jq -r '.parentSpanId')" = "" ] \
  || { echo "ERROR: the chat span has a parent"; fail=1; }
for k in gen_ai.usage.input_tokens gen_ai.usage.output_tokens; do
  v=$(echo "$CHAT" | jq -r --arg k "$k" 'first(.attributes[] | select(.key == $k) | .value.intValue) // "0"')
  [ "$v" -gt 0 ] 2>/dev/null \
    || { echo "ERROR: chat span reports $k = $v, expected a positive count"; fail=1; }
done

echo "-- tool spans hang under the chat span"
CHAT_ID=$(echo "$CHAT" | jq -r '.spanId')
# The Agent tool span is the delegation's own execute_tool span; it is the only
# one allowed a child, so it is excluded from the flat-under-chat rule.
AGENT_TOOL_ID=$(echo "$SPANS" | jq -r 'first(.[] | select(.name | startswith("execute_tool"))
  | select([.attributes[] | select(.key == "gen_ai.tool.name") | .value.stringValue] | first == "Agent") | .spanId) // ""')
MISPARENTED=$(echo "$SPANS" | jq -r --arg chat "$CHAT_ID" --arg agent "$AGENT_TOOL_ID" \
  '[.[] | select(.name | startswith("execute_tool"))
        | select(.parentSpanId != $chat and .parentSpanId != $agent)
        | .name] | join(",")')
[ -z "$MISPARENTED" ] || { echo "ERROR: tool spans parented outside the chat and Agent spans: $MISPARENTED"; fail=1; }

echo "-- the scripted tools all produced a span"
# The MCP call goes out as capture_echo and must arrive split: OpenCode's flat
# <serverKey>_<tool> name resolved back into the tool and its server.
for want in read echo Agent; do
  [ "$(echo "$SPANS" | jq --arg t "$want" '[.[] | .attributes[] | select(.key == "gen_ai.tool.name") | select(.value.stringValue == $t)] | length')" -ge 1 ] \
    || { echo "ERROR: no tool span for the scripted '$want' call"; fail=1; }
done
MCP_SERVER=$(echo "$SPANS" | jq -r 'first(.[] | .attributes[] | select(.key == "dash0.gen_ai.tool.mcp_server") | .value.stringValue) // ""')
[ "$MCP_SERVER" = "capture" ] \
  || { echo "ERROR: the MCP call reported server '$MCP_SERVER', expected 'capture'"; fail=1; }
# The second scripted read targets a file that does not exist, so exactly one
# tool span must carry an error status. A green run here means the plugin
# stopped reporting failures.
[ "$(echo "$SPANS" | jq '[.[] | select(.name | startswith("execute_tool")) | select(.status.code == 2)] | length')" -ge 1 ] \
  || { echo "ERROR: the failing scripted tool call produced no span with an error status"; fail=1; }

echo "-- the delegation nests: chat > Agent tool > invoke_agent > the child's tools"
[ -n "$AGENT_TOOL_ID" ] || { echo "ERROR: the delegation produced no Agent tool span"; fail=1; }
INVOKE=$(echo "$SPANS" | jq '[.[] | select(.name | startswith("invoke_agent"))] | first')
if [ "$(echo "$INVOKE" | jq -r 'type')" = "object" ] && [ -n "$AGENT_TOOL_ID" ]; then
  [ "$(echo "$INVOKE" | jq -r '.parentSpanId')" = "$AGENT_TOOL_ID" ] \
    || { echo "ERROR: invoke_agent does not hang under the Agent tool span"; fail=1; }
else
  echo "ERROR: no invoke_agent span for the delegated sub-task"; fail=1
fi

echo "-- content is withheld under the default omit_io"
# omit_io defaults on, which resolves prompts and tools to limited. The two
# dimensions withhold differently, and this is the only layer where the
# difference is observable against a real session.
for k in gen_ai.input.messages gen_ai.output.messages; do
  envelope=$(echo "$CHAT" | jq -r --arg k "$k" 'first(.attributes[] | select(.key == $k) | .value.stringValue) // ""')
  [ -n "$envelope" ] \
    || { echo "ERROR: $k is missing; prompts: limited must keep the envelope"; fail=1; continue; }
  [ "$(printf '%s' "$envelope" | jq -r '[.[].parts[].content] | unique | join(",")')" = "<REDACTED>" ] \
    || { echo "ERROR: $k content is not the redaction placeholder: $envelope"; fail=1; }
  [ "$(printf '%s' "$envelope" | jq -r '[.[].role] | join(",")')" != "" ] \
    || { echo "ERROR: $k lost the message roles it is supposed to preserve"; fail=1; }
done
for k in dash0.gen_ai.input.messages.withheld_characters dash0.gen_ai.output.messages.withheld_characters; do
  v=$(echo "$CHAT" | jq -r --arg k "$k" 'first(.attributes[] | select(.key == $k) | .value.intValue) // "0"')
  [ "$v" -gt 0 ] 2>/dev/null \
    || { echo "ERROR: $k is $v; limited must report the size it withheld"; fail=1; }
done
[ "$(echo "$CHAT" | jq -r 'first(.attributes[] | select(.key == "gen_ai.conversation.name") | .value.stringValue) // ""')" = "<REDACTED>" ] \
  || { echo "ERROR: gen_ai.conversation.name is not redacted at prompts: limited"; fail=1; }

# tools: limited omits rather than placeholds — the execute_tool convention
# makes both attributes Opt-In. A placeholder here would be a regression.
LEAKED=$(echo "$SPANS" | jq -r '[.[] | select(.name | startswith("execute_tool"))
  | .attributes[] | select(.key | IN("gen_ai.tool.call.arguments", "gen_ai.tool.call.result", "exception.message"))
  | .key] | unique | join(",")')
[ -z "$LEAKED" ] || { echo "ERROR: tools: limited exported opt-in attributes: $LEAKED"; fail=1; }

BODIES=$(curl -s "$OTLP/requests" | jq -r '[.requests[] | select(.auth == "Bearer live-main") | .body] | join("\n")')
case "$BODIES" in
  *"live fixture project"*) echo "ERROR: the readme's contents were exported under the default omit_io"; fail=1 ;;
esac

echo "-- the scripted usage reaches the chat span"
# The scripted server reports reasoning and cached-prompt tokens on every
# response. cache_creation is mapped but always 0: the OpenAI wire format the
# mock speaks has no cache-write field to carry one.
for k in gen_ai.usage.cache_read.input_tokens gen_ai.usage.reasoning.output_tokens; do
  v=$(echo "$CHAT" | jq -r --arg k "$k" 'first(.attributes[] | select(.key == $k) | .value.intValue) // "0"')
  [ "$v" -gt 0 ] 2>/dev/null \
    || { echo "ERROR: $k is $v; the scripted usage reports one on every call"; fail=1; }
done
echo "$CHAT" | jq -e 'any(.attributes[]; .key == "gen_ai.usage.cache_creation.input_tokens")' >/dev/null \
  || { echo "ERROR: gen_ai.usage.cache_creation.input_tokens is not mapped at all"; fail=1; }
# The sub-agent's usage is its own, not a copy of the parent's.
INVOKE_IN=$(echo "$INVOKE" | jq -r 'first(.attributes[] | select(.key == "gen_ai.usage.input_tokens") | .value.intValue) // "0"')
[ "$INVOKE_IN" -gt 0 ] 2>/dev/null \
  || { echo "ERROR: the invoke_agent span reports no usage of its own"; fail=1; }

[ "$fail" -eq 0 ] || exit 1
echo "PASS: a real opencode session produces the documented span tree"

echo "== the assertions discriminate =="
# Everything above passes on a session where each attribute is present and
# withheld exactly as configured, so a probe that quietly stopped matching would
# still read green. Running the inverse configuration makes the same probes come
# back the other way round; if they do not, they were not testing anything.
S=$(session live-inverse "$(printf 'prompts: full\ntools: disabled')")
[ "$SESSION_EXIT" -eq 0 ] || { echo "ERROR: the inverse session exited $SESSION_EXIT"; exit 1; }
sleep 2
INV=$(spans live-inverse)
fail=0
INV_CHAT=$(echo "$INV" | jq '[.[] | select(.name | startswith("chat"))] | first')
INV_IN=$(echo "$INV_CHAT" | jq -r 'first(.attributes[] | select(.key == "gen_ai.input.messages") | .value.stringValue) // ""')
case "$INV_IN" in
  *"Read the readme"*) ;;
  *) echo "ERROR: prompts: full did not export the prompt text: $INV_IN"; fail=1 ;;
esac
echo "$INV_CHAT" | jq -e 'any(.attributes[]; .key == "dash0.gen_ai.input.messages.withheld_characters")' >/dev/null \
  && { echo "ERROR: a withheld-character count was reported at prompts: full"; fail=1; }
[ "$(echo "$INV" | jq '[.[] | select(.name | startswith("execute_tool"))] | length')" -eq 0 ] \
  || { echo "ERROR: an execute_tool span survived tools: disabled"; fail=1; }
# agents was left at its default here, so the delegation still reports itself —
# but the Agent tool span it normally hangs from is gone with the rest of the
# tool spans, and a span pointing at a parent that was never exported is a
# broken trace in the backend, not a smaller one.
INV_ORPHANS=$(echo "$INV" | jq -r '[.[].spanId] as $known
  | [.[] | select(.parentSpanId != "" and (.parentSpanId | IN($known[]) | not)) | .name] | join(",")')
[ -z "$INV_ORPHANS" ] || { echo "ERROR: tools: disabled orphaned spans: $INV_ORPHANS"; fail=1; }
[ "$fail" -eq 0 ] || { echo "$INV" | jq -r '.[] | "\(.name)\t\(.spanId)\t\(.parentSpanId)"' | column -t; exit 1; }
echo "PASS: the same probes report the inverse configuration correctly"

echo "== the session survives every telemetry failure =="
# Each of these breaks the plugin in a different place. `opencode run` must
# still exit 0 and print nothing that looks like an error, because telemetry
# that can break the user's agent is worse than no telemetry.
check_fail_open() {
  local label="$1" sandbox="$2"
  local bad
  if [ "$SESSION_EXIT" -ne 0 ]; then
    echo "ERROR: $label — opencode run exited $SESSION_EXIT"
    tail -20 "$sandbox/opencode.log"
    return 1
  fi
  # The wrapper's own diagnostics are expected here; a stack trace or an
  # OpenCode-level error is not.
  bad=$(grep -Ei 'unhandled|uncaught|panic: |Error: ' "$sandbox/opencode.log" \
        | grep -v 'opencode-on-event:' | grep -v 'no-such-file' || true)
  if [ -n "$bad" ]; then
    echo "ERROR: $label — opencode reported an error:"
    printf '%s\n' "$bad" | head -5
    return 1
  fi
  echo "PASS: $label"
}

fail=0
S=$(session live-unreachable 'otlp_url: "http://127.0.0.1:1/v1/traces"')
check_fail_open "an unreachable collector" "$S" || fail=1

S=$(session live-rejected 'otlp_url: "http://localhost:4319/reject"')
check_fail_open "a rejected endpoint" "$S" || fail=1

# A config file with no closing delimiter and a key the reader has no case for.
S=$(session live-malformed '
not: [valid
  yaml at all')
check_fail_open "a malformed config file" "$S" || fail=1

echo "-- a corrupted cached binary"
CORRUPT="$(mktemp -d)"; mkdir -p "$CORRUPT/state/dash0-agent-plugin/opencode/bin"
CORRUPT_BIN="$CORRUPT/state/dash0-agent-plugin/opencode/bin/opencode-on-event-${VERSION}-$(os_arch)"
printf 'not a binary\n' > "$CORRUPT_BIN"; chmod +x "$CORRUPT_BIN"
printf 'deadbeef\n' > "$CORRUPT_BIN.sha256"
S=$(DASH0_PLUGIN_DATA="$CORRUPT/state/dash0-agent-plugin/opencode" session live-corrupt)
check_fail_open "a corrupted cached binary" "$S" || fail=1

[ "$fail" -eq 0 ] || exit 1
echo "PASS: every telemetry failure leaves the opencode session intact"
