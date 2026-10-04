// SPDX-FileCopyrightText: Copyright 2026 Dash0 Inc.
// SPDX-License-Identifier: Apache-2.0

// copilot-app-on-event is the GitHub Copilot desktop app entrypoint. The app
// has no command hooks; a session extension (copilot-app/extension.mjs) watches
// the session's event stream and spawns this binary, through
// copilot-app/copilot-app-on-event.sh, with the event name on argv and a JSON
// payload on stdin:
//
//   - sessionStart        when the extension joins a session
//   - userPromptSubmitted when the user sends a message
//   - turnEnd             when the session goes idle, carrying that turn's
//     usage, message, tool and sub-agent events
//   - sessionEnd          when the session ends
//
// On turnEnd the binary builds the turn from those events, attaches usage,
// model and response to the Stop event for pipeline.Process's chat span, then
// emits one invoke_agent span per sub-agent and one execute_tool span per tool
// call — the same tree the CLI produces: chat → execute_tool task →
// invoke_agent → execute_tool view.
//
// Telemetry failures never break the user's session: errors go to stderr and
// the process always exits 0.
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
	"github.com/dash0hq/dash0-agent-plugin/internal/source/copilotapp"
)

const name = "copilot-app-on-event"

var hn = harness.CopilotApp

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "%s: %v\n", name, err)
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

	payload, err := pipeline.ReadEvent(os.Stdin)
	if err != nil {
		return err
	}
	now := copilotapp.Timestamp(payload, time.Now().UTC())

	event := copilotapp.Normalize(eventName, payload)
	if event == nil {
		return nil
	}
	pipeline.ChdirToEventCwd(event)

	dataDir, err := hn.DataDir()
	if err != nil {
		return err
	}
	cfg := hn.Config()
	hookEvent, _ := event["hook_event_name"].(string)

	// The data directory also holds the bootstrap's binary cache, so an id that
	// names it must not become a session directory: Process would join it, and
	// its SessionEnd would remove it. See cmd/copilot-on-event.
	sessionID, _ := event["session_id"].(string)
	sessionDir := ""
	switch {
	case !pipeline.IsSafeSessionID(sessionID):
		// Process substitutes a random id and says so on the span.
	case copilot.ReservedSessionID(sessionID):
		sessionID = copilot.UnreserveSessionID(sessionID)
		event["session_id"] = sessionID
		sessionDir = pipeline.SessionDir(dataDir, sessionID)
	default:
		sessionDir = pipeline.SessionDir(dataDir, sessionID)
	}

	if hookEvent == "SessionStart" && sessionDir != "" {
		// A session killed with the app delivers no sessionEnd, so nothing
		// removes its directory. The marker is what lets the sweep tell this
		// runtime's sessions from the others sharing the state root.
		copilot.MarkSessionStarted(dataDir, sessionID)
		copilot.SweepOldSessionDirs(dataDir, sessionID, now)
	}

	// The trace context must be read before Process: its Stop branch clears it.
	var turn *copilot.Turn
	var turnCtx *otlp.TraceContext
	if hookEvent == "Stop" {
		turn = copilotapp.BuildTurn(copilotapp.DecodeEvents(payload), now)
		if turn != nil && turn.Usage != nil {
			copilot.AttachUsage(event, turn.Usage)
		}
		if sessionDir != "" {
			turnCtx, _ = otlp.LoadTraceContext(sessionDir)
		}
	}

	result, err := pipeline.Process(event, cfg, dataDir, now)
	if err != nil {
		return err
	}

	// Without a trace context Process emitted no chat span, so there is nothing
	// for these to hang off.
	if turn != nil && turnCtx != nil && turnCtx.TraceID != "" {
		// Agents first: a sub-agent's tools parent onto its invoke_agent span.
		copilot.EmitAgentSpans(turn, turnCtx, cfg, name)
		copilot.EmitToolSpans(turn, turnCtx, cfg, name)
	}

	for _, msg := range result.Messages {
		if msg.UserText != "" {
			fmt.Fprintln(os.Stderr, msg.UserText)
		}
	}
	return nil
}
