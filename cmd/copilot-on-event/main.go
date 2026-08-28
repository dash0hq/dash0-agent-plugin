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
//     file's own cost figure is left behind — see attachUsage.
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
	"encoding/json"
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
	var turnCursor, turnSession string
	if hookEvent == "Stop" {
		sessionID, _ := event["session_id"].(string)
		sessionDir := pipeline.SessionDir(dataDir, sessionID)
		if t, newCursor := copilot.ReadTurn(sessionID); t != nil {
			turn = t
			if t.Usage != nil {
				attachUsage(event, t.Usage)
			}
			turnCursor, turnSession = newCursor, sessionID
		}
		turnCtx, _ = otlp.LoadTraceContext(sessionDir)
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
			emitAgentSpans(turn, turnCtx, cfg)
			emitToolSpans(turn, turnCtx, cfg)
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

// attachUsage sets the per-turn token, model and response attributes on the Stop
// event.
func attachUsage(event map[string]any, u *copilot.Usage) {
	event["gen_ai.usage.input_tokens"] = u.InputTokens
	event["gen_ai.usage.output_tokens"] = u.OutputTokens
	event["gen_ai.usage.cache_read.input_tokens"] = u.CacheReadInputTokens
	if u.ReasoningOutputTokens > 0 {
		event["gen_ai.usage.reasoning.output_tokens"] = u.ReasoningOutputTokens
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
// tool's real start/end, and parents follow the native tree — a sub-agent's
// tools nest under its invoke_agent span (see emitAgentSpans), top-level tools
// under the turn's chat span. Events are synthesized in the pipeline's
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
		if tc.SkillName != "" {
			event["skill_name"] = tc.SkillName
		}

		// Derive the shared semantic attributes (URLs, line counts, bash/skill,
		// MCP server + normalized name). Same rule set the hook-driven path runs,
		// so OmitIO redaction and the dash0.gen_ai.* details stay uniform.
		pipeline.EnrichToolEvent(event)

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

// emitAgentSpans emits one invoke_agent span per sub-agent the turn spawned,
// between the `task` tool that spawned it and the tools it ran. Copilot's own
// OpenTelemetry describes that layer and the plugin used to collapse it, which
// left the sub-agent's identity with nowhere standard to go and put a custom
// key on the tool span instead.
//
// The event is shaped so the pipeline's existing mapping does the work:
// agent_type becomes gen_ai.agent.name and drives the invoke_agent span name,
// agent_id becomes gen_ai.agent.id. Same keys as Claude and Codex produce.
//
// No usage is attached. Attribution stays flat — a sub-agent's chat spans fold
// into the parent turn's total, which is what Copilot's file supports today —
// so putting the same tokens here as well would double them for anyone summing
// across a trace. The native span carries no usage either.
func emitAgentSpans(turn *copilot.Turn, ctx *otlp.TraceContext, cfg otlp.Config) {
	for _, sa := range turn.Agents {
		agentType := sa.AgentType
		if agentType == "" {
			// NewLLMSpan reads agent_type to decide it is an invoke_agent span at
			// all, so an unnamed agent would silently become a chat span.
			agentType = "agent"
		}
		event := map[string]any{
			"session_id": ctx.SessionID,
			"agent_type": agentType,
		}
		if sa.CallID != "" {
			event["agent_id"] = sa.CallID
		}
		if turn.Usage != nil && turn.Usage.Model != "" {
			event["model"] = turn.Usage.Model
		}

		parent := sa.ParentSpanID
		if parent == "" {
			parent = ctx.SpanID // no spawning tool span this turn → the chat span
		}
		span := otlp.NewLLMSpan(ctx.TraceID, sa.SpanID, parent, sa.Start, sa.End, event, sa.Failed, cfg)
		if err := otlp.SendTrace(span, event, cfg); err != nil {
			fmt.Fprintf(os.Stderr, "copilot-on-event: agent span export: %v\n", err)
		}
	}
}
