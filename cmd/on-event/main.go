// SPDX-FileCopyrightText: Copyright 2026 Dash0 Inc.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"compress/zlib"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/dash0hq/dash0-agent-plugin/internal/dotenv"
	"github.com/dash0hq/dash0-agent-plugin/internal/otlp"
	"github.com/dash0hq/dash0-agent-plugin/internal/pipeline"
	"github.com/dash0hq/dash0-agent-plugin/internal/version"
)

func main() {
	if len(os.Args) > 1 && os.Args[1] == "session-url" {
		if err := printSessionURL(); err != nil {
			fmt.Fprintf(os.Stderr, "on-event session-url: %v\n", err)
			os.Exit(1)
		}
		return
	}
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "on-event: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	dotenv.Load(".env")

	dataDir := os.Getenv("CLAUDE_PLUGIN_DATA")
	if dataDir == "" {
		return fmt.Errorf("CLAUDE_PLUGIN_DATA is not set")
	}

	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		return fmt.Errorf("creating data directory: %w", err)
	}

	raw, err := io.ReadAll(os.Stdin)
	if err != nil {
		return fmt.Errorf("reading stdin: %w", err)
	}

	var event map[string]any
	if err := json.Unmarshal(raw, &event); err != nil {
		return fmt.Errorf("parsing JSON from stdin: %w", err)
	}

	cfg := otlp.Config{
		OTLPUrl:      pluginOption("OTLP_URL"),
		AuthToken:    pluginOptionSecure("AUTH_TOKEN"),
		Dataset:      pluginOption("DATASET"),
		AgentName:    pluginOption("AGENT_NAME"),
		HarnessName:  "claude-code",
		TeamName:     pluginOption("TEAM_NAME"),
		Provider:     "anthropic",
		OmitUserInfo: pluginOptionBoolDefault("OMIT_USER_INFO", false),
		OmitIO:       pluginOptionBoolDefault("OMIT_IO", true),
		Debug:        pluginOptionBool("DEBUG"),
		DebugFile:    pluginOption("DEBUG_FILE"),
	}
	if cfg.AgentName == "" {
		cfg.AgentName = "claude-code"
	}
	pipeline.ValidateOTLPURL(&cfg)

	now := time.Now().UTC()
	result, err := pipeline.Process(event, cfg, dataDir, now)
	if err != nil {
		return err
	}

	hookEvent, _ := event["hook_event_name"].(string)
	sessionID, _ := event["session_id"].(string)

	for _, msg := range result.Messages {
		text := msg.UserText
		// Annotate the SessionStart connectivity-success message with the
		// running plugin version and session URL.
		if hookEvent == "SessionStart" && text == "dash0: connected" {
			text = fmt.Sprintf("dash0: connected (v%s)", version.Version)
			if appURL := deriveAppURL(cfg.OTLPUrl); appURL != "" {
				sessionURL := buildSessionURL(appURL, sessionID)
				text += " → " + sessionURL
			}
		}
		// SessionStart's "telemetry is not active" message gets a Claude-Code-specific
		// instructions tail pointing at /plugin → Configure.
		if hookEvent == "SessionStart" && strings.HasPrefix(text, "dash0: telemetry is not active") {
			text = "dash0: telemetry is not active — configure the plugin to start sending data. Run /plugin → Installed → dash0 → Configure, then /reload-plugins."
		}
		printHookResponse(text, msg.ModelContext)
	}

	if (hookEvent == "Stop" || hookEvent == "StopFailure") && cfg.OTLPUrl != "" && pluginOptionBool("SHOW_SESSION_LINK") {
		if appURL := deriveAppURL(cfg.OTLPUrl); appURL != "" {
			sessionURL := buildSessionURL(appURL, sessionID)
			printHookResponse(fmt.Sprintf("dash0: view session → %s", sessionURL), "")
		}
	}

	return nil
}

func printSessionURL() error {
	dotenv.Load(".env")

	otlpURL := pluginOption("OTLP_URL")
	if otlpURL == "" {
		return fmt.Errorf("OTLP_URL is not configured")
	}
	appURL := deriveAppURL(otlpURL)
	if appURL == "" {
		return fmt.Errorf("cannot derive app URL from OTLP_URL %q", otlpURL)
	}

	raw, err := io.ReadAll(os.Stdin)
	if err != nil {
		return fmt.Errorf("reading stdin: %w", err)
	}
	var input map[string]any
	if err := json.Unmarshal(raw, &input); err != nil {
		return fmt.Errorf("parsing JSON from stdin: %w", err)
	}
	sessionID, _ := input["session_id"].(string)
	if sessionID == "" {
		return fmt.Errorf("session_id not provided")
	}

	fmt.Println(buildSessionURL(appURL, sessionID))
	return nil
}

// printHookResponse outputs a JSON response that Claude Code renders as both
// a user-visible message (systemMessage) and model context (additionalContext).
func printHookResponse(userMessage, modelContext string) {
	resp := map[string]string{}
	if userMessage != "" {
		resp["systemMessage"] = userMessage
	}
	if modelContext != "" {
		resp["additionalContext"] = modelContext
	}
	out, _ := json.Marshal(resp)
	fmt.Fprintln(os.Stdout, string(out))
}

// deriveAppURL maps an OTLP ingress URL to the corresponding Dash0 app URL.
// Returns empty string if the URL doesn't match a known Dash0 pattern.
func deriveAppURL(otlpURL string) string {
	if otlpURL == "" {
		return ""
	}
	u, err := url.Parse(otlpURL)
	if err != nil {
		return ""
	}
	host := u.Hostname()
	switch {
	case strings.HasSuffix(host, ".dash0.com"):
		return "https://app.dash0.com"
	case strings.HasSuffix(host, ".dash0-dev.com"):
		return "https://app.dash0-dev.com"
	default:
		return ""
	}
}

// buildSessionURL constructs a full Dash0 AI Coding URL with the encoded URL
// state parameter that the Dash0 UI expects. It points at the sessions table
// on the /coding-agents page and sets agentSession so the UI auto-opens the
// session detail sidebar.
func buildSessionURL(appURL, sessionID string) string {
	const codingAgentsPath = "/coding-agents"
	state := map[string]any{
		codingAgentsPath: map[string]any{
			"tab":          map[string]any{"pageTab": "sessions"},
			"agentSession": sessionID,
		},
	}
	stateJSON, err := json.Marshal(state)
	if err != nil {
		return appURL + codingAgentsPath
	}
	var buf bytes.Buffer
	w := zlib.NewWriter(&buf)
	_, _ = w.Write(stateJSON)
	_ = w.Close()
	encoded := base64.URLEncoding.WithPadding(base64.NoPadding).EncodeToString(buf.Bytes())
	return appURL + codingAgentsPath + "?s=" + encoded
}

// envBool returns true when the environment variable is set to "true" or "1".
func envBool(key string) bool {
	v := strings.ToLower(strings.TrimSpace(os.Getenv(key)))
	return v == "true" || v == "1"
}

// pluginOption returns the configured value for the given key, preferring
// the userConfig-derived CLAUDE_PLUGIN_OPTION_<key> over the legacy DASH0_<key>.
// An empty CLAUDE_PLUGIN_OPTION_<key> falls through to DASH0_<key>.
//
// Note: sensitive values (AUTH_TOKEN) must use pluginOptionSecure instead to
// prevent env var leakage into tool-spawned shells.
func pluginOption(key string) string {
	if v := os.Getenv("CLAUDE_PLUGIN_OPTION_" + key); v != "" {
		return v
	}
	return os.Getenv("DASH0_" + key)
}

// pluginOptionSecure reads only from CLAUDE_PLUGIN_OPTION_<key> without falling
// back to DASH0_<key>. Use for sensitive values like auth tokens that must not
// leak into tool-spawned shell environments.
func pluginOptionSecure(key string) string {
	return os.Getenv("CLAUDE_PLUGIN_OPTION_" + key)
}

// pluginOptionBool is the boolean counterpart of pluginOption.
func pluginOptionBool(key string) bool {
	v := strings.ToLower(strings.TrimSpace(pluginOption(key)))
	return v == "true" || v == "1"
}

// pluginOptionBoolDefault returns defaultVal when the option is unset/empty,
// and parses as boolean otherwise.
func pluginOptionBoolDefault(key string, defaultVal bool) bool {
	v := strings.ToLower(strings.TrimSpace(pluginOption(key)))
	if v == "" {
		return defaultVal
	}
	return v == "true" || v == "1"
}
