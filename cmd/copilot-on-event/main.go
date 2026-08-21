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
//     Copilot's native-OTel file: token/cost/model/response (attached to the
//     Stop event for the pipeline's chat span) AND the turn's tool executions.
//  4. Hands off to pipeline.Process for the chat span, then emits one
//     execute_tool span per recovered tool call — real durations, sub-agent
//     tools nested under their spawning `task` span.
//
// Telemetry failures never break the user's session: errors go to stderr and
// the process always exits 0. This fail-open contract is mandatory (Copilot's
// tool-gating hooks treat a non-zero exit as a block).
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
	"github.com/dash0hq/dash0-agent-plugin/internal/source/copilot"
)

var hn = harness.Copilot

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

	event, err := pipeline.ReadEvent(os.Stdin)
	if err != nil {
		return err
	}

	// Every Copilot payload (camelCase and pascalCase alike) carries the
	// workspace as `cwd`. chdir into the payload's cwd before anything git-dependent runs
	// so the pipeline sees the right working tree.
	pipeline.ChdirToEventCwd(event)

	// After the chdir: a project-level config file has to be looked up in the
	// workspace the agent is working in, which is the directory hn.Config() below
	// reads it from. Checking before the chdir looked for it in whatever
	// directory the harness happened to spawn the hook from, so a project that
	// set `enabled: false` kept emitting.
	if !hn.Enabled() {
		return nil
	}

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

	// Copilot fires sessionStart and userPromptSubmitted at session startup in a
	// NONDETERMINISTIC order. pipeline.Process handles this generally: its SessionStart
	// branch MERGES into any existing trace context rather than overwriting it, so the
	// trace/span IDs an already-delivered userPromptSubmitted established survive.
	// SessionStart can therefore flow through the pipeline like every other event
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

// attachUsage sets the per-turn token/cost/model attributes on the Stop event.
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
	// pipeline renders last_assistant_message as gen_ai.output.messages.
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
// dash0.gen_ai.* details stay uniform.
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
		// task spans are tellable apart.
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
