// SPDX-FileCopyrightText: Copyright 2026 Dash0 Inc.
// SPDX-License-Identifier: Apache-2.0

// copilot-on-event is the GitHub Copilot CLI entrypoint. Copilot spawns this
// binary for each hook event (via copilot/copilot-on-event.sh, which forwards
// the event name as an argv and pipes the payload on stdin). The binary:
//
//  1. Reads the event name from argv (camelCase Copilot payloads carry no
//     hook_event_name field) and the payload from stdin.
//  2. Normalizes it to the pipeline's canonical vocabulary.
//  3. On a turn boundary (agentStop→Stop), recovers the whole turn from
//     Copilot's native-OTel file: token/model/response (attached to the Stop
//     event for the pipeline's chat span) AND the turn's tool executions. The
//     file's own cost figure is left behind — see copilot.AttachUsage.
//  4. Hands off to pipeline.Process for the chat span, then emits the turn's
//     recovered spans: one invoke_agent per sub-agent and one execute_tool per
//     tool call, with real durations and the native tree preserved —
//     chat → execute_tool task → invoke_agent → execute_tool bash.
//
// Telemetry failures never break the user's session: errors go to stderr and
// the process always exits 0. This fail-open contract is mandatory (Copilot's
// tool-gating hooks treat a non-zero exit as a block).
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

	// Every Copilot payload (camelCase and pascalCase alike) carries the
	// workspace as `cwd`. chdir into the payload's cwd before anything git-dependent runs
	// so the pipeline sees the right working tree.
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

	// pipeline.Process replaces a missing or unsafe session id with a random one,
	// but only once the event reaches it. Everything below runs first and either
	// joins a path or deletes a directory, so it asks first: an empty id resolves
	// SessionDir to dataDir ITSELF, and a RemoveAll on that takes the bootstrap's
	// binary cache and every concurrent session's state with it.
	//
	// A safe path segment is not enough: "started" and "bin" are both safe and
	// both name a directory this plugin owns, so they get the same treatment.
	//
	// An id this cannot vouch for means no marker, no suppression and no sweep.
	// The event still takes the normal path, under whatever id Process settles
	// on. That direction is the safe one: losing the sub-agent suppression for a
	// malformed payload costs at worst a spurious conversation, while trusting
	// the id costs somebody else's data.
	sessionID, _ := event["session_id"].(string)
	sessionDir := ""
	switch {
	case !pipeline.IsSafeSessionID(sessionID):
		// Process substitutes a random id and says so on the span.
	case copilot.ReservedSessionID(sessionID):
		// Keeping this file off those directories is not enough on its own:
		// Process joins the id itself, and its SessionEnd removes what it
		// joined. Rename the id in the payload so Process lands on an ordinary
		// session directory instead.
		//
		// The local sessionID keeps the id Copilot sent, because it is also the
		// conversation id ReadTurn looks up in the native-OTel file, which is
		// not this plugin's to rewrite.
		event["session_id"] = copilot.UnreserveSessionID(sessionID)
	default:
		sessionDir = pipeline.SessionDir(dataDir, sessionID)
	}

	// The marker that tells a real session from a sub-agent's: only sessionStart
	// writes one, only agentStop reads it, and nothing ever deletes it. See
	// session.go.
	//
	// It goes down BEFORE the sweeps. Hooks for one session are concurrent
	// processes, so an agentStop can be reading it while this one runs, and
	// unbounded directory I/O in between only widens that window. A marker
	// written late is a turn dropped for nothing.
	//
	// Copilot fires sessionStart and userPromptSubmitted at session startup in a
	// NONDETERMINISTIC order, which nothing here depends on: pipeline.Process
	// MERGES a SessionStart into any trace context an already-delivered
	// userPromptSubmitted established, and the marker is read at agentStop, by
	// which time a real session has had its SessionStart either way.
	if hookEvent == "SessionStart" && sessionDir != "" {
		copilot.MarkSessionStarted(dataDir, sessionID)
		// Native-OTel files left behind by prior unclean exits, where the
		// launcher's rm never ran, so the convention dir stays bounded.
		copilot.SweepOldOtelFiles(time.Now())
		// And what killed runs left in the data directory. A session that ends
		// deletes its own; one that is killed delivers no sessionEnd, so nothing
		// did. This session's own directory is kept.
		copilot.SweepOldSessionDirs(dataDir, sessionID, time.Now())
	}

	// On a turn boundary, recover the whole turn from the native-OTel file:
	// usage/model/response are attached to the Stop event before pipeline.Process
	// (the Cursor pattern; transcript_path is intentionally absent, so the
	// pipeline's Claude-transcript reader is skipped), and the turn's tool calls
	// are emitted as spans after Process. The trace context must be captured
	// BEFORE Process — the Stop branch clears it.
	var turn *copilot.Turn
	var turnCtx *otlp.TraceContext
	var turnCursor, turnSession string
	if hookEvent == "Stop" {
		recovered, newCursor := copilot.ReadTurn(sessionID)

		// A turn ending in a session that never started is a sub-agent's, and
		// processing it mints a standalone, token-less conversation: its own chat
		// span, with no model and no usage, because the sub-agent's native-OTel
		// spans carry the PARENT's conversation id. Normalize drops the sub-agent
		// sessions it can name — `copilot -p` calls them call_<toolCallId> — but
		// an interactive session gives them a plain UUID, and that one shipped.
		//
		// Both conditions, because a missing marker alone is not evidence. Only
		// sessionStart writes one, and that hook can fail to write it for reasons
		// that have nothing to do with sub-agents: the bootstrap downloads the
		// binary inside the hook, under Copilot's 10s timeout, and every one of
		// its fail-open paths exits before the binary runs. The first session
		// after a version bump on a slow link is a real session with no marker,
		// and suppressing on the marker alone silenced all of it, permanently.
		//
		// The file settles it. A sub-agent has no spans under its own
		// conversation id — that is the whole reason its turn has nothing to
		// report — so ReadTurn returns nil for one and a turn for a real session.
		//
		// It leaves one gap, and it is the smaller one: with native OTel off
		// there is no file to ask, so a real session that also lost its marker is
		// still suppressed. Such a session emits only bare chat spans anyway, and
		// a marker is lost only when the sessionStart hook failed outright.
		//
		// Nothing is emitted for a suppressed session, so its scratch directory
		// has no further use: the userPromptSubmitted that created it is the only
		// reason it exists.
		if sessionDir != "" && recovered == nil && !copilot.SessionStarted(dataDir, sessionID) {
			_ = os.RemoveAll(sessionDir)
			return nil
		}

		if recovered != nil {
			turn = recovered
			if recovered.Usage != nil {
				copilot.AttachUsage(event, recovered.Usage)
			}
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
	if turnSession != "" {
		// Emit the tool spans and advance the cursor TOGETHER, gated on an intact
		// trace context (captured before Process, which clears it). When the context
		// is missing — blank TraceID — skip BOTH: pipeline.Process likewise refuses
		// to emit the chat span (see sendLLMTrace), so leaving the cursor put folds
		// this turn's usage and tools into a later turn instead of marking them
		// consumed and dropping them. Advancing only after a successful emit — and
		// only after Process — keeps the cursor and the spans from drifting apart.
		if turn != nil && turnCtx != nil && turnCtx.TraceID != "" {
			// Agents first: a sub-agent's tools parent onto its invoke_agent span,
			// so the parent is on the wire before its children.
			copilot.EmitAgentSpans(turn, turnCtx, cfg, "copilot-on-event")
			copilot.EmitToolSpans(turn, turnCtx, cfg, "copilot-on-event")
			copilot.SaveCursor(turnSession, turnCursor)
		}
	}
	for _, msg := range result.Messages {
		if msg.UserText != "" {
			fmt.Fprintln(os.Stderr, msg.UserText)
		}
	}
	return nil
}
