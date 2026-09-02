// SPDX-FileCopyrightText: Copyright 2026 Dash0 Inc.
// SPDX-License-Identifier: Apache-2.0

// copilot-on-event is the GitHub Copilot CLI entrypoint. Copilot spawns it per
// hook event through copilot/copilot-on-event.sh, or copilot-on-event.ps1 on
// Windows, which passes the event name in argv (camelCase payloads carry no
// hook_event_name) and the payload on stdin.
//
// It normalizes the event to the pipeline's vocabulary, and on a turn boundary
// (agentStop to Stop) recovers the turn from Copilot's native-OTel file: usage,
// model and response for the chat span, plus the turn's sub-agents and tool calls.
// pipeline.Process emits the chat span; copilot.EmitTurn emits the rest, keeping
// the native tree chat -> execute_tool task -> invoke_agent -> execute_tool bash.
//
// Telemetry failures never break the user's session: errors go to stderr and the
// process always exits 0. copilot/hooks.json registers lifecycle events only, so
// a non-zero exit gates no tool call; it prints a hook error into the session on
// every turn instead.
package main

import (
	"fmt"
	"os"
	"time"

	"github.com/dash0hq/dash0-agent-plugin/internal/dotenv"
	"github.com/dash0hq/dash0-agent-plugin/internal/harness"
	"github.com/dash0hq/dash0-agent-plugin/internal/otlp"
	"github.com/dash0hq/dash0-agent-plugin/internal/pipeline"
	"github.com/dash0hq/dash0-agent-plugin/internal/source/copilot"
)

var hn = harness.Copilot

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "copilot-on-event: %v\n", err)
	}
}

func run() error {
	if !hn.Enabled() {
		return nil
	}

	dotenv.Load(".env")

	eventName := ""
	if len(os.Args) > 1 {
		eventName = os.Args[1]
	}

	event, err := pipeline.ReadEvent(os.Stdin)
	if err != nil {
		return err
	}

	// Every Copilot payload carries the workspace as `cwd`. chdir before anything
	// git-dependent runs, so the pipeline sees the right working tree.
	pipeline.ChdirToEventCwd(event)

	event = copilot.Normalize(eventName, event)
	if event == nil {
		return nil
	}

	dataDir, err := hn.DataDir()
	if err != nil {
		return err
	}

	cfg := hn.Config()
	hookEvent, _ := event["hook_event_name"].(string)

	// pipeline.Process replaces a missing or unsafe session id with a random one, but
	// only once the event reaches it. Everything below runs first and either joins a
	// path or deletes a directory, so it asks first: an empty id resolves SessionDir to
	// dataDir ITSELF, and a RemoveAll there takes the binary cache and every concurrent
	// session's state with it. A safe path segment is not enough either, since "started"
	// and "bin" both name directories this plugin owns.
	//
	// An id this cannot vouch for means no marker, no suppression and no sweep; the
	// event still takes the normal path. Losing suppression costs a spurious
	// conversation, where trusting the id costs somebody else's data.
	sessionID, _ := event["session_id"].(string)
	sessionDir := ""
	switch {
	case !pipeline.IsSafeSessionID(sessionID):
		// Process substitutes a random id and says so on the span.
	case copilot.ReservedSessionID(sessionID):
		// Process joins the id itself and its SessionEnd removes what it joined,
		// so the payload's id is renamed to land on an ordinary directory. The
		// local sessionID keeps what Copilot sent: it is also the conversation id
		// ReadTurn looks up in the native-OTel file.
		event["session_id"] = copilot.UnreserveSessionID(sessionID)
	default:
		sessionDir = pipeline.SessionDir(dataDir, sessionID)
	}

	// The marker that tells a real session from a sub-agent's: only sessionStart
	// writes one, only agentStop reads it, nothing deletes it. See session.go.
	//
	// It goes down BEFORE the sweeps. Hooks for one session run concurrently, so an
	// agentStop can be reading it while this one runs, and directory I/O in between
	// widens that window. A marker written late is a turn dropped for nothing.
	//
	// Copilot fires sessionStart and userPromptSubmitted in a nondeterministic order,
	// which nothing here depends on: Process merges a SessionStart into whatever trace
	// context userPromptSubmitted established.
	if hookEvent == "SessionStart" && sessionDir != "" {
		copilot.MarkSessionStarted(dataDir, sessionID)
		// Files left by unclean exits, where the launcher's rm never ran.
		copilot.SweepOldOtelFiles(time.Now())
		// And what killed runs left behind: a session that ends deletes its own,
		// one that is killed delivers no sessionEnd. This session's is kept.
		copilot.SweepOldSessionDirs(dataDir, sessionID, time.Now())
	}

	// On a turn boundary, recover the turn from the native-OTel file. Usage, model and
	// response go onto the Stop event before Process (the Cursor pattern; no
	// transcript_path, so the Claude-transcript reader is skipped); the sub-agents and
	// tool calls are emitted after it. The trace context is captured BEFORE Process,
	// which clears it in the Stop branch.
	var turn *copilot.Turn
	var turnCtx *otlp.TraceContext
	var turnCursor, turnSession string
	if hookEvent == "Stop" {
		recovered, newCursor := copilot.PrepareTurn(event, sessionID)

		// A turn ending in a session that never started is a sub-agent's, and
		// processing it mints a standalone token-less conversation: the sub-agent's
		// native-OTel spans carry the PARENT's conversation id. Normalize drops the
		// sub-agent sessions it can name (`copilot -p` calls them call_<toolCallId>),
		// but an interactive session gives them a plain UUID.
		//
		// Both conditions, because a missing marker alone is not evidence. The
		// bootstrap downloads the binary inside the hook, under Copilot's 10s
		// timeout, and every fail-open path exits before the binary runs, so the
		// first session after a version bump on a slow link is a real session with
		// no marker. Suppressing on the marker alone silenced it permanently.
		//
		// The file settles it: a sub-agent has no spans under its own conversation
		// id, so PrepareTurn returns nil for one and a turn for a real session. One
		// gap remains, the smaller one: with native OTel off there is no file to
		// ask, so a real session that also lost its marker stays suppressed.
		//
		// A suppressed session emits nothing, so its scratch directory has no
		// further use.
		if sessionDir != "" && recovered == nil && !copilot.SessionStarted(dataDir, sessionID) {
			_ = os.RemoveAll(sessionDir)
			return nil
		}

		if recovered != nil {
			turn = recovered
			turnCursor, turnSession = newCursor, sessionID
		}
		if sessionDir != "" {
			turnCtx, _ = otlp.LoadTraceContext(sessionDir)
		}
	}

	result, err := pipeline.Process(event, cfg, dataDir, time.Now().UTC())
	if err != nil {
		return err
	}
	// Emits the recovered spans and advances the cursor together, and only with the
	// trace context captured above. See copilot.EmitTurn for why a missing context
	// defers the turn rather than consuming it.
	copilot.EmitTurn(turn, turnCursor, turnSession, turnCtx, cfg)
	for _, msg := range result.Messages {
		if msg.UserText != "" {
			fmt.Fprintln(os.Stderr, msg.UserText)
		}
	}
	return nil
}
