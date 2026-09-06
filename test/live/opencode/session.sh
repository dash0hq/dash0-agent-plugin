#!/usr/bin/env bash
# SPDX-FileCopyrightText: Copyright 2026 Dash0 Inc.
# SPDX-License-Identifier: Apache-2.0
#
# One scripted OpenCode session in a sandbox of its own. Sourced by run.sh,
# which asserts against a mock collector, and by dash0-session.sh, which sends
# the same session to a real Dash0 ingress. Both must drive the identical turn
# or the live verification proves nothing about what CI checks.
#
# Requires from the caller: REPO, CACHE_DIR, CAPTURE, LLM_PORT, and a built
# binary at BIN_SRC. Provides: build_session_inputs, session, SESSION_EXIT.

# The scripted model and MCP servers live with the capture harness that first
# needed them. One scripted turn, one place to change it: a second copy would
# drift from the fixture the golden tests replay.
SESSION_PROMPT="Read the readme, then read a missing file, then call the capture echo tool, then delegate a sub-task, then check the working tree."

# build_session_inputs — build the binary and the plugin bundle once, ahead of
# any session. Sets BIN_SRC, WRAPPER, BUNDLE and VERSION.
build_session_inputs() {
  WRAPPER="$REPO/opencode/opencode-on-event.sh"
  BUNDLE="$REPO/opencode/dist/dash0-opencode-plugin.js"
  VERSION=$(grep '^VERSION=' "$WRAPPER" | sed 's/VERSION="//;s/"//')
  BIN_SRC="$(mktemp -d)/opencode-on-event"
  make -C "$REPO" build-binary PKG=./cmd/opencode-on-event OUT="$BIN_SRC" >/dev/null
  ( cd "$REPO/opencode" && ./build.sh >/dev/null )
}

# session TOKEN OTLP_URL DATASET [EXTRA_CONFIG_LINES] — run one full session and
# echo the sandbox directory. EXTRA lines are appended to the config file, so a
# caller must not repeat a key the three arguments already write: the wrapper
# greps each key and would export both matches as one value.
#
# Sets SESSION_EXIT rather than failing, because the fail-open contracts assert
# on opencode's exit status.
SESSION_EXIT=0
SESSION_SEQ=0
# shellcheck disable=SC2034  # SESSION_EXIT is read by the sourcing script
session() {
  local token="$1" otlp="$2" dataset="$3" extra="${4:-}" sandbox home project
  sandbox="$(mktemp -d)"; home="$sandbox/home"; project="$sandbox/project"
  mkdir -p "$home/.config/opencode/plugin" "$home/.local/share" "$project"
  printf 'live fixture project\n' > "$project/README.md"

  # A git repo with an identity and a remote of its own. Without it the VCS and
  # user.email attributes are simply absent, and their absence would read as a
  # gap in the plugin rather than as a bare sandbox.
  git -C "$project" init -q -b main
  git -C "$project" config user.name "Live Fixture"
  git -C "$project" config user.email "live-fixture@dash0.com"
  git -C "$project" remote add origin https://github.com/dash0hq/opencode-live-fixture.git
  git -C "$project" add -A
  git -C "$project" -c commit.gpgsign=false commit -qm "fixture"

  cp "$BUNDLE" "$home/.config/opencode/plugin/dash0-opencode-plugin.js"
  cp "$WRAPPER" "$home/.config/opencode/plugin/opencode-on-event.sh"
  chmod +x "$home/.config/opencode/plugin/opencode-on-event.sh"

  {
    echo "---"
    echo "otlp_url: \"$otlp\""
    echo "auth_token: \"$token\""
    echo "dataset: \"$dataset\""
    echo 'team_name: "opencode-live"'
    [ -n "$extra" ] && printf '%s\n' "$extra"
    echo "---"
  } > "$home/.config/opencode/dash0-agent-plugin.local.md"

  # The wrapper resolves its binary by version and would otherwise download it
  # from a published release. Seeding the local build keeps the run offline and
  # tests the code in this checkout rather than the last one that shipped.
  local bindir="$sandbox/state/dash0-agent-plugin/opencode/bin"
  mkdir -p "$bindir"
  cp "$BIN_SRC" "$bindir/opencode-on-event-${VERSION}-$(os_arch)"

  # One scripted server per session, on a port of its own. It has to start after
  # the project exists, because the script reads files inside it — a shared
  # server would resolve them against the runner's own directory instead. Its
  # per-request usage counter starts at zero here too, which is what makes the
  # token totals below exactly predictable.
  local port=$((LLM_PORT + 10 + SESSION_SEQ)); SESSION_SEQ=$((SESSION_SEQ + 1))
  MOCK_LLM_PORT="$port" MOCK_LLM_PROJECT_DIR="$project" MOCK_LLM_BASH_STEP=1 \
    node "$CAPTURE/mock-llm.mjs" & local llm_pid=$!
  local ready=
  for _ in $(seq 1 40); do
    curl -sf -m 1 "http://127.0.0.1:$port/v1/models?probe=1" >/dev/null 2>&1 && { ready=1; break; }
    sleep 0.25
  done
  [ -n "$ready" ] || { echo "ERROR: the scripted model never came up on $port" >&2; kill "$llm_pid" 2>/dev/null; return 1; }

  jq --arg base "http://127.0.0.1:$port/v1" \
     --arg mcp "$CAPTURE/mock-mcp.mjs" \
     --arg plugin "$home/.config/opencode/plugin/dash0-opencode-plugin.js" \
     '.provider.mock.options.baseURL = $base
      | .mcp.capture.command = ["node", $mcp]
      | .plugin = [$plugin]' \
     "$CAPTURE/opencode.json" > "$home/.config/opencode/opencode.json"

  SESSION_EXIT=0
  (
    cd "$project"
    HOME="$home" \
    XDG_CONFIG_HOME="$home/.config" \
    XDG_DATA_HOME="$home/.local/share" \
    XDG_STATE_HOME="$sandbox/state" \
    XDG_CACHE_HOME="$CACHE_DIR" \
      opencode run --print-logs --model mock/mock-model "$SESSION_PROMPT" \
        < /dev/null > "$sandbox/opencode.log" 2>&1
  ) || SESSION_EXIT=$?
  kill "$llm_pid" 2>/dev/null || true

  printf '%s\n' "$sandbox"
}
