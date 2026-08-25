// SPDX-FileCopyrightText: Copyright 2026 Dash0 Inc.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/dash0hq/dash0-agent-plugin/internal/dotenv"
	"github.com/dash0hq/dash0-agent-plugin/internal/harness"
	"github.com/dash0hq/dash0-agent-plugin/internal/otlp"
	"github.com/dash0hq/dash0-agent-plugin/internal/pipeline"
	"github.com/dash0hq/dash0-agent-plugin/internal/sessionurl"
)

var hn = harness.Claude

func main() {
	if len(os.Args) > 1 && os.Args[1] == "session-url" {
		printSessionURL()
		return
	}

	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "on-event: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	dotenv.Load(".env")

	// Claude Code always sets this. Treat a missing value as a hard error rather
	// than falling back, because a silent fallback would put session state where
	// Claude cannot see it.
	dataDir := os.Getenv("CLAUDE_PLUGIN_DATA")
	if dataDir == "" {
		return fmt.Errorf("CLAUDE_PLUGIN_DATA is not set")
	}

	event, err := pipeline.ReadEvent(os.Stdin)
	if err != nil {
		return err
	}

	cfg := hn.Config()
	now := time.Now().UTC()
	result, err := pipeline.Process(event, cfg, dataDir, now)
	if err != nil {
		return err
	}

	printSessionMessage(event, result, cfg)

	return nil
}

// printSessionMessage displays a link to the current Session in Dash0.
func printSessionMessage(event map[string]any, result pipeline.Result, cfg otlp.Config) {
	hookEvent, _ := event["hook_event_name"].(string)

	for _, msg := range result.Messages {
		text := msg.UserText
		// SessionStart's "telemetry is not active" message gets a Claude-Code-specific
		// instructions tail pointing at /plugin → Configure. The connectivity-success
		// message (version + session link) is assembled by the shared pipeline.
		if hookEvent == "SessionStart" && strings.HasPrefix(text, "dash0: telemetry is not active") {
			text = "dash0: telemetry is not active — configure the plugin to start sending data. Run /plugin → Installed → dash0 → Configure, then /reload-plugins."
		}
		// Only Claude Code renders session messages, so this hint lives here rather
		// than in the shared pipeline. It rides in the connected message instead of
		// being printed on its own: Claude Code parses stdout as ONE hook response,
		// and a second JSON object makes it discard the whole output — verified,
		// nothing at all is shown. The "dash0: connected" message is the marker for a
		// first SessionStart with working telemetry: without it, either the session
		// was resumed or the user has a more urgent problem to fix first.
		if hookEvent == "SessionStart" && strings.HasPrefix(text, "dash0: connected") && cfg.TeamName == "" {
			text += "\ndash0: no team configured — set Team Name via /plugin → Configure."
		}
		printHookResponse(text, msg.ModelContext)
	}

	if (hookEvent == "Stop" || hookEvent == "StopFailure") && cfg.OTLPUrl != "" && hn.PluginOptionBool("SHOW_SESSION_LINK") {
		sessionID, _ := event["session_id"].(string)
		if link := sessionurl.SessionURL(cfg.OTLPUrl, sessionID, cfg.Dataset); link != "" {
			printHookResponse(fmt.Sprintf("dash0: view session → %s", link), "")
		}
	}
}

// printHookResponse outputs a JSON response that Claude Code renders as both
// a user-visible message (systemMessage) and model context (additionalContext).
func printHookResponse(userMessage, modelContext string) {
	resp := map[string]string{}
	if userMessage != "" {
		// Claude Code prefixes the message with "SessionStart:startup says: " on the
		// same line. The leading newline keeps first line aligned with the ones
		// below it.
		resp["systemMessage"] = "\n" + userMessage
	}
	if modelContext != "" {
		resp["additionalContext"] = modelContext
	}
	out, _ := json.Marshal(resp)
	fmt.Fprintln(os.Stdout, string(out))
}

// printSessionURL backs the `session-url` subcommand (invoked by the
// /dash0-agent-plugin:open-session slash command): it prints the current
// session's Dash0 link to stdout, or logs the error and exits non-zero.
func printSessionURL() {
	link, err := sessionURL()
	if err != nil {
		fmt.Fprintf(os.Stderr, "on-event session-url: %v\n", err)
		os.Exit(1)
	}
	fmt.Println(link)
}

// sessionURL derives the Dash0 session link from the session_id on stdin and
// the configured OTLP URL. It returns an error when telemetry is not configured,
// the payload carries no session_id, or the OTLP host is not a recognized Dash0
// host (see sessionurl.SessionURL).
func sessionURL() (string, error) {
	event, err := pipeline.ReadEvent(os.Stdin)
	if err != nil {
		return "", err
	}
	dotenv.Load(".env")
	otlpURL := hn.PluginOption("OTLP_URL")
	if otlpURL == "" {
		return "", fmt.Errorf("OTLP_URL is not configured")
	}
	sessionID, _ := event["session_id"].(string)
	if sessionID == "" {
		return "", fmt.Errorf("session_id not provided")
	}
	link := sessionurl.SessionURL(otlpURL, sessionID, hn.PluginOption("DATASET"))
	if link == "" {
		return "", fmt.Errorf("cannot derive app URL from OTLP_URL %q", otlpURL)
	}
	return link, nil
}
