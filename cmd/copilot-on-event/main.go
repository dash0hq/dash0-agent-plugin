// SPDX-FileCopyrightText: Copyright 2026 Dash0 Inc.
// SPDX-License-Identifier: Apache-2.0

// copilot-on-event is the GitHub Copilot CLI entrypoint. Copilot spawns this
// binary for each hook event (via copilot/copilot-on-event.sh, which forwards
// the event name as an argv and pipes the payload on stdin). The binary:
//
//  1. Reads the event name from argv (camelCase Copilot payloads carry no
//     hook_event_name field) and the payload from stdin.
//  2. Normalizes it to the pipeline's canonical vocabulary (internal/source/copilot).
//  3. On a turn boundary (agentStop→Stop), recovers the whole turn from
//     Copilot's native-OTel file: token/cost/model/response (attached to the
//     Stop event for the pipeline's chat span) AND the turn's tool executions.
//  4. Hands off to pipeline.Process for the chat span, then emits one
//     execute_tool span per recovered tool call — real durations, sub-agent
//     tools nested under their spawning `task` span. (Copilot's postToolUse
//     hooks are NOT used for tool spans: they carry no duration and never fire
//     inside sub-agents; the native-OTel file is the authoritative source.)
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
	// first). pipeline.Process handles this generally: its SessionStart branch
	// MERGES into any existing trace context rather than overwriting it, so the
	// trace/span IDs an already-delivered userPromptSubmitted established survive.
	// SessionStart can therefore flow through the pipeline like every other event
	// (connectivity check + "started" marker included) — the only Copilot-specific
	// need here is sweeping stale native-OTel files.
	if hookEvent == "SessionStart" {
		// Sweep native-OTel files left behind by prior unclean exits (where the
		// launcher's rm never ran) so the convention dir doesn't grow unbounded.
		copilot.SweepOldOtelFiles(time.Now())
	}

	// On a turn boundary, recover the whole turn from the native-OTel file:
	// usage/model/response are attached to the Stop event before pipeline.Process
	// (the Cursor pattern; transcript_path is intentionally absent, so the
	// pipeline's Claude-transcript reader is skipped), and the turn's tool calls
	// are emitted as spans after Process. The trace context must be captured
	// BEFORE Process — the Stop branch clears it.
	var turn *copilot.Turn
	var turnCtx *otlp.TraceContext
	var turnCursor, turnDir string
	if hookEvent == "Stop" {
		sessionID, _ := event["session_id"].(string)
		sessionDir := pipeline.SessionDir(dataDir, sessionID)
		_ = os.MkdirAll(sessionDir, 0o755)
		if t, newCursor := copilot.ReadTurn(sessionID, sessionDir); t != nil {
			turn = t
			if t.Usage != nil {
				attachUsage(event, t.Usage)
			}
			turnCursor, turnDir = newCursor, sessionDir
		}
		turnCtx, _ = otlp.LoadTraceContext(sessionDir)
	}

	result, err := pipeline.Process(event, cfg, dataDir, time.Now().UTC())
	if err != nil {
		return err
	}
	if turnDir != "" {
		// Emit the tool spans and advance the cursor TOGETHER, gated on an intact
		// trace context (captured before Process, which clears it). When the context
		// is missing — blank TraceID — skip BOTH: pipeline.Process likewise refuses
		// to emit the chat span (see sendLLMTrace), so leaving the cursor put folds
		// this turn's usage and tools into a later turn instead of marking them
		// consumed and dropping them. Advancing only after a successful emit — and
		// only after Process — keeps the cursor and the spans from drifting apart.
		if turn != nil && turnCtx != nil && turnCtx.TraceID != "" {
			emitToolSpans(turn, turnCtx, cfg)
			copilot.SaveCursor(turnDir, turnCursor)
		}
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

// emitToolSpans emits one execute_tool span per tool call recovered from the
// native-OTel file, onto the turn's trace: native span ids are reused verbatim
// (same 16-hex format as ours — idempotent across re-reads), timings are the
// tool's real start/end, and parents collapse the native invoke_agent/chat
// layers — a sub-agent's tools nest under their spawning `task` span, top-level
// tools under the turn's chat span. Events are synthesized in the pipeline's
// canonical shape and run through the same extractor enrichments as
// hook-sourced tool events on the other runtimes, so OmitIO redaction and the
// dash0.gen_ai.* details stay uniform. Export failures log and continue —
// fail-open, and one lost span must not block the rest.
func emitToolSpans(turn *copilot.Turn, ctx *otlp.TraceContext, cfg otlp.Config) {
	for _, tc := range turn.Tools {
		event := map[string]any{
			"session_id": ctx.SessionID,
			"tool_name":  tc.Name,
		}
		// Native arguments are a JSON string; decode so extractors (command
		// family, skill name) see the same map shape hooks deliver elsewhere.
		var args map[string]any
		if json.Unmarshal([]byte(tc.Arguments), &args) == nil && args != nil {
			event["tool_input"] = args
		} else if tc.Arguments != "" {
			event["tool_input"] = tc.Arguments
		}
		if tc.Result != "" {
			event["tool_response"] = tc.Result
		}
		if tc.CallID != "" {
			event["tool_use_id"] = tc.CallID
		}
		if turn.Usage != nil && turn.Usage.Model != "" {
			event["model"] = turn.Usage.Model
		}

		// Derive the shared semantic attributes (URLs, line counts, bash/skill,
		// MCP server + normalized name). Same rule set the hook-driven path runs,
		// so OmitIO redaction and the dash0.gen_ai.* details stay uniform.
		pipeline.EnrichToolEvent(event)

		// Label a sub-agent spawn with its instance name (e.g. "echo-runner") so
		// task spans are tellable apart; a non-content field, so not OmitIO-gated.
		// Set directly under its wire key (the pipeline passes unmapped keys
		// through verbatim), keeping this Copilot-specific detail local.
		if strings.EqualFold(tc.Name, "task") && args != nil {
			if name, _ := args["name"].(string); name != "" {
				event["dash0.gen_ai.tool.task.name"] = name
			}
		}

		parent := tc.ParentSpanID
		if parent == "" {
			parent = ctx.SpanID // top-level tool → the turn's chat span
		}
		span := otlp.NewToolSpan(ctx.TraceID, tc.SpanID, parent, tc.Start, tc.End, event, tc.Failed, cfg)
		if err := otlp.SendTrace(span, event, cfg); err != nil {
			fmt.Fprintf(os.Stderr, "copilot-on-event: tool span export: %v\n", err)
		}
	}
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
