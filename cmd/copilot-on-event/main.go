// SPDX-FileCopyrightText: Copyright 2026 Dash0 Inc.
// SPDX-License-Identifier: Apache-2.0

// copilot-on-event is the GitHub Copilot CLI entrypoint. Copilot spawns this
// binary for each hook event (via copilot/copilot-on-event.sh, which forwards
// the event name as an argv and pipes the payload on stdin). The binary:
//
//  1. Reads the event name from argv (camelCase Copilot payloads carry no
//     hook_event_name field) and the payload from stdin.
//  2. Normalizes it to the pipeline's canonical vocabulary (internal/source/copilot).
//  3. On a turn boundary (agentStop→Stop), recovers that turn's token/cost/model
//     from Copilot's native-OTel file and attaches it to the event.
//  4. Hands off to pipeline.Process, which emits canonical spans — the same
//     shape as the Claude Code and Cursor runtimes.
//
// Telemetry failures never break the user's session: errors go to stderr and
// the process always exits 0. This fail-open contract is mandatory (Copilot's
// tool-gating hooks treat a non-zero exit as a block).
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
	"github.com/dash0hq/dash0-agent-plugin/internal/source/copilot"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "copilot-on-event: %v\n", err)
	}
}

func run() error {
	dotenv.Load(".env")

	eventName := ""
	if len(os.Args) > 1 {
		eventName = os.Args[1]
	}

	raw, err := io.ReadAll(os.Stdin)
	if err != nil {
		return fmt.Errorf("reading stdin: %w", err)
	}
	var event map[string]any
	if err := json.Unmarshal(raw, &event); err != nil {
		return fmt.Errorf("parsing JSON from stdin: %w", err)
	}

	// Every Copilot payload (camelCase and pascalCase alike) carries the
	// workspace as `cwd`, but Copilot spawns the hook with a process CWD that
	// isn't the workspace root — so vcs.Detect()'s `git rev-parse --git-dir`
	// fails and spans lose repo/branch metadata (only the global git identity
	// survives). chdir into the payload's cwd before anything git-dependent runs
	// so the pipeline sees the right working tree. This precedes Normalize, which
	// consumes the raw case-variant keys.
	chdirToCwd(event)

	event = copilot.Normalize(eventName, event)
	if event == nil {
		return nil // event the pipeline doesn't consume — exit cleanly
	}

	dataDir, err := resolveDataDir()
	if err != nil {
		return err
	}

	cfg := otlp.Config{
		OTLPUrl:      dash0Env("OTLP_URL"),
		AuthToken:    pluginOptionSecure("AUTH_TOKEN"),
		Dataset:      dash0Env("DATASET"),
		AgentName:    agentName(),
		HarnessName:  "github-copilot-cli",
		TeamName:     dash0Env("TEAM_NAME"),
		OmitUserInfo: dash0EnvBool("OMIT_USER_INFO", false),
		OmitIO:       dash0EnvBool("OMIT_IO", true),
		Debug:        dash0EnvBool("DEBUG", false),
		DebugFile:    dash0Env("DEBUG_FILE"),
	}
	pipeline.ValidateOTLPURL(&cfg)

	hookEvent, _ := event["hook_event_name"].(string)

	// Copilot fires sessionStart and userPromptSubmitted at session startup in a
	// NONDETERMINISTIC order (unlike Claude/Cursor, where sessionStart is always
	// first). Routing SessionStart through pipeline.Process saves a fresh trace
	// context that BLANKS the trace/span IDs userPromptSubmitted established for
	// the turn's chat span — leaving the chat span uncorrelated. So the adapter
	// absorbs the ordering quirk: handle SessionStart here (connectivity check
	// only) and keep it off the pipeline.
	if hookEvent == "SessionStart" {
		// Sweep native-OTel files left behind by prior unclean exits (where the
		// launcher's rm never ran) so the convention dir doesn't grow unbounded.
		copilot.SweepOldOtelFiles(time.Now())
		// Connectivity check once per session: Copilot re-fires SessionStart on
		// each --resume, so gate on a marker to avoid a repeated round-trip and
		// "dash0: connected" line (the pipeline does the same for other runtimes).
		sessionID, _ := event["session_id"].(string)
		sessionDir := pipeline.SessionDir(dataDir, sessionID)
		if _, err := os.Stat(filepath.Join(sessionDir, "started")); err != nil {
			reportConnectivity(cfg)
			_ = os.MkdirAll(sessionDir, 0o755)
			_ = os.WriteFile(filepath.Join(sessionDir, "started"), nil, 0o644)
		}
		return nil
	}

	// On a turn boundary, recover this turn's usage from the native-OTel file
	// and attach it before pipeline.Process (the Cursor pattern). transcript_path
	// is intentionally absent, so the pipeline's Claude-transcript reader is skipped.
	var usageCursor, usageCursorDir string
	if hookEvent == "Stop" {
		sessionID, _ := event["session_id"].(string)
		sessionDir := pipeline.SessionDir(dataDir, sessionID)
		_ = os.MkdirAll(sessionDir, 0o755)
		if usage, newCursor := copilot.ReadTurnUsage(sessionID, sessionDir); usage != nil {
			attachUsage(event, usage)
			usageCursor, usageCursorDir = newCursor, sessionDir
		}
	}

	// Tool spans (PostToolUse) carry no model in the payload, and the pipeline's
	// transcript fallback is disabled for Copilot; tag them with the turn's model
	// from the native-OTel file so they match the Claude/Cursor tool spans.
	if hookEvent == "PostToolUse" || hookEvent == "PostToolUseFailure" {
		if _, has := event["model"]; !has {
			sessionID, _ := event["session_id"].(string)
			if m := copilot.LatestModel(sessionID); m != "" {
				event["model"] = m
			}
		}
	}

	result, err := pipeline.Process(event, cfg, dataDir, time.Now().UTC())
	if err != nil {
		return err
	}
	// Advance the usage cursor only AFTER Process — so a turn whose span can't be
	// built (e.g. missing trace context) isn't marked consumed and instead folds
	// into a later turn.
	if usageCursorDir != "" {
		copilot.SaveCursor(usageCursorDir, usageCursor)
	}
	for _, msg := range result.Messages {
		if msg.UserText != "" {
			fmt.Fprintln(os.Stderr, msg.UserText)
		}
	}
	return nil
}

// attachUsage sets the per-turn token/cost/model attributes on the Stop event
// as int64 (so the OTLP layer encodes them as integer attributes).
func attachUsage(event map[string]any, u *copilot.Usage) {
	event["gen_ai.usage.input_tokens"] = u.InputTokens
	event["gen_ai.usage.output_tokens"] = u.OutputTokens
	event["gen_ai.usage.cache_read.input_tokens"] = u.CacheReadInputTokens
	if u.ReasoningOutputTokens > 0 {
		event["gen_ai.usage.reasoning.output_tokens"] = u.ReasoningOutputTokens
	}
	if u.Cost > 0 {
		event["github.copilot.cost"] = u.Cost
	}
	if u.Model != "" {
		if _, has := event["model"]; !has {
			event["model"] = u.Model
		}
	}
	// The agentStop payload carries no response text (only stopReason), so the
	// turn's final assistant message comes from the native-OTel chat span. The
	// pipeline renders last_assistant_message as gen_ai.output.messages — the
	// same path Claude/Cursor/Codex use — so OmitIO redaction stays uniform.
	if u.ResponseText != "" {
		if _, has := event["last_assistant_message"]; !has {
			event["last_assistant_message"] = u.ResponseText
		}
	}
}

// reportConnectivity runs the SessionStart connectivity check and prints a
// status line to stderr — mirroring what pipeline.Process does for the other
// runtimes, but WITHOUT routing SessionStart through the pipeline (which would
// clobber the turn's trace context; see run()).
func reportConnectivity(cfg otlp.Config) {
	if cfg.OTLPUrl == "" {
		fmt.Fprintln(os.Stderr, "dash0: telemetry is not active — configure the plugin to start sending data.")
		return
	}
	if err := otlp.CheckConnectivity(cfg); err != nil {
		fmt.Fprintf(os.Stderr, "dash0: connectivity check failed — %v\n", err)
		return
	}
	fmt.Fprintln(os.Stderr, "dash0: connected")
}

// resolveDataDir picks the per-session scratch root. Precedence:
// COPILOT_PLUGIN_DATA (Copilot's writable per-plugin dir) > DASH0_PLUGIN_DATA >
// XDG_STATE_HOME > ~/.local/state — all under dash0-agent-plugin/copilot.
func resolveDataDir() (string, error) {
	if v := os.Getenv("COPILOT_PLUGIN_DATA"); v != "" {
		return v, nil
	}
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
	return filepath.Join(base, "dash0-agent-plugin", "copilot"), nil
}

func agentName() string {
	if v := os.Getenv("DASH0_AGENT_NAME"); v != "" {
		return v
	}
	return "github-copilot-cli"
}

func dash0Env(key string) string {
	return os.Getenv("DASH0_" + key)
}

// pluginOptionSecure reads only COPILOT_PLUGIN_OPTION_<key> (never DASH0_<key>),
// so the auth token doesn't leak into tool-spawned shells.
func pluginOptionSecure(key string) string {
	return os.Getenv("COPILOT_PLUGIN_OPTION_" + key)
}

// chdirToCwd moves the process into the hook payload's cwd. Best-effort: if the
// field is missing or chdir fails, we keep the original CWD and let vcs.Detect
// produce what it can.
func chdirToCwd(event map[string]any) {
	cwd, ok := event["cwd"].(string)
	if !ok || cwd == "" {
		return
	}
	_ = os.Chdir(cwd)
}

func dash0EnvBool(key string, defaultVal bool) bool {
	v := strings.ToLower(strings.TrimSpace(dash0Env(key)))
	if v == "" {
		return defaultVal
	}
	return v == "true" || v == "1"
}
