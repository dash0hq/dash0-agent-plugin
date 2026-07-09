// SPDX-FileCopyrightText: Copyright 2026 Dash0 Inc.
// SPDX-License-Identifier: Apache-2.0

// cursor-on-event is the Cursor-side entrypoint. Cursor spawns this binary
// fresh for every hook event (via scripts/cursor-on-event.sh which downloads
// the matching release on first run), pipes the hook JSON in on stdin, and
// expects a clean exit. The binary:
//
//  1. Reads the Cursor hook payload from stdin.
//  2. Normalizes it to the pipeline's canonical event vocabulary (see
//     internal/source/cursor).
//  3. Hands off to pipeline.Process, which writes scratch state, manages
//     trace context across hook invocations, and emits OTLP spans.
//
// Telemetry failures never break the user's agent loop: errors are logged to
// stderr and the process exits 0.
package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/dash0hq/dash0-agent-plugin/internal/dotenv"
	"github.com/dash0hq/dash0-agent-plugin/internal/otlp"
	"github.com/dash0hq/dash0-agent-plugin/internal/pipeline"
	"github.com/dash0hq/dash0-agent-plugin/internal/source/cursor"
)

func main() {
	// Cursor's hook contract treats a non-zero exit as a blocking failure
	// (when failClosed: true is set). We always exit 0 so a broken exporter
	// never breaks the user's session — errors land on stderr only.
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "cursor-on-event: %v\n", err)
	}
}

func run() error {
	dotenv.Load(".env")

	dataDir, err := resolveDataDir()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		return fmt.Errorf("creating data directory %s: %w", dataDir, err)
	}

	raw, err := io.ReadAll(os.Stdin)
	if err != nil {
		return fmt.Errorf("reading stdin: %w", err)
	}

	var event map[string]any
	if err := json.Unmarshal(raw, &event); err != nil {
		return fmt.Errorf("parsing JSON from stdin: %w", err)
	}

	// Cursor spawns hooks with a CWD that isn't the workspace root, so
	// vcs.Detect()'s `git rev-parse --git-dir` would fail and we'd lose
	// repo/branch metadata. Every payload carries `workspace_roots`; chdir
	// into the first entry before normalization so git commands in the
	// pipeline see the right working tree.
	chdirToWorkspaceRoot(event)

	event = cursor.Normalize(event)
	if event == nil {
		// Helper hook the pipeline doesn't consume — exit cleanly.
		return nil
	}

	// Provider is intentionally unset: Cursor proxies many vendors, so the
	// provider is derived per-event from the model name. Events without a
	// model (PreToolUse, SessionStart, etc.) simply omit gen_ai.provider.name
	// — they're not GenAI operations.
	cfg := otlp.Config{
		OTLPUrl:      dash0Env("OTLP_URL"),
		AuthToken:    pluginOptionSecure("AUTH_TOKEN"),
		Dataset:      dash0Env("DATASET"),
		AgentName:    agentName(),
		HarnessName:  "cursor",
		TeamName:     dash0Env("TEAM_NAME"),
		OmitUserInfo: dash0EnvBool("OMIT_USER_INFO", false),
		OmitIO:       dash0EnvBool("OMIT_IO", true),
		Debug:        dash0EnvBool("DEBUG", false),
		DebugFile:    dash0Env("DEBUG_FILE"),
	}
	pipeline.ValidateOTLPURL(&cfg)

	now := time.Now().UTC()
	result, err := pipeline.Process(event, cfg, dataDir, now)
	if err != nil {
		return err
	}

	// Cursor's observational hooks ignore stdout for fail-open hooks. We log
	// status messages to stderr instead so they appear in Cursor's hook log
	// without affecting the agent loop.
	for _, msg := range result.Messages {
		if msg.UserText != "" {
			fmt.Fprintln(os.Stderr, msg.UserText)
		}
	}

	return nil
}

// resolveDataDir picks the per-source scratch root for Cursor sessions.
// Precedence: DASH0_PLUGIN_DATA env override > XDG_STATE_HOME > ~/.local/state.
// All sit under a dash0-agent-plugin/cursor subdirectory so we don't collide
// with other tools or the Claude Code plugin.
func resolveDataDir() (string, error) {
	if v := os.Getenv("DASH0_PLUGIN_DATA"); v != "" {
		return v, nil
	}
	base := os.Getenv("XDG_STATE_HOME")
	if base == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("resolving HOME: %w", err)
		}
		base = filepath.Join(home, ".local", "state")
	}
	return filepath.Join(base, "dash0-agent-plugin", "cursor"), nil
}

// agentName picks the service.name / gen_ai.agent.name attribute for spans.
// Defaults to "cursor"; can be overridden via DASH0_AGENT_NAME (e.g. to tag
// engineering vs. product orgs separately).
func agentName() string {
	if v := os.Getenv("DASH0_AGENT_NAME"); v != "" {
		return v
	}
	return "cursor"
}

// dash0Env reads DASH0_<key>. Used for non-sensitive options; the bootstrap
// script exports these from the YAML config file, and DASH0_* env vars also
// work as a fallback (useful for CI/dev).
//
// Note: sensitive values (AUTH_TOKEN) must use pluginOptionSecure instead to
// prevent env var leakage into tool-spawned shells.
func dash0Env(key string) string {
	return os.Getenv("DASH0_" + key)
}

// pluginOptionSecure reads only from CURSOR_PLUGIN_OPTION_<key> without falling
// back to DASH0_<key>. Use for sensitive values like auth tokens that must not
// leak into tool-spawned shell environments — Cursor agents can spawn arbitrary
// processes (via Bash, MCP, etc.) which inherit our env, and other Dash0 tools
// look for DASH0_AUTH_TOKEN specifically.
func pluginOptionSecure(key string) string {
	return os.Getenv("CURSOR_PLUGIN_OPTION_" + key)
}

// chdirToWorkspaceRoot moves the process into the first workspace root from
// the Cursor hook payload. Best-effort: if the field is missing, not a list
// of strings, or chdir fails, we keep the original CWD and let vcs.Detect
// produce what it can.
func chdirToWorkspaceRoot(event map[string]any) {
	roots, ok := event["workspace_roots"].([]any)
	if !ok || len(roots) == 0 {
		return
	}
	root, ok := roots[0].(string)
	if !ok || root == "" {
		return
	}
	_ = os.Chdir(root)
}

func dash0EnvBool(key string, defaultVal bool) bool {
	v := strings.ToLower(strings.TrimSpace(dash0Env(key)))
	if v == "" {
		return defaultVal
	}
	return v == "true" || v == "1"
}
