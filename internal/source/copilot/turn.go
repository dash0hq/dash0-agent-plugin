// SPDX-FileCopyrightText: Copyright 2026 Dash0 Inc.
// SPDX-License-Identifier: Apache-2.0

package copilot

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/dash0hq/dash0-agent-plugin/internal/otlp"
	"github.com/dash0hq/dash0-agent-plugin/internal/pipeline"
)

// A Copilot turn is recovered in two halves, either side of pipeline.Process.
// PrepareTurn runs first and attaches the ending turn's usage to the Stop event.
// EmitTurn runs after, because the recovered spans need the trace context that
// Process clears.
//
// Both halves live here, not in cmd/copilot-on-event, so they can be tested
// without spawning the binary.

// PrepareTurn recovers the ending turn and attaches its per-turn usage to the
// Stop event. It returns the turn and the cursor to save once the turn is
// emitted, or nil when there is nothing to recover. A nil turn also tells the
// caller this Stop belongs to a sub-agent (see cmd/copilot-on-event).
//
// transcript_path stays absent, so the pipeline skips its Claude-transcript
// reader: Copilot's usage comes from the native-OTel file.
func PrepareTurn(event map[string]any, sessionID string) (*Turn, string) {
	turn, cursor := ReadTurn(sessionID)
	if turn == nil {
		return nil, ""
	}
	if turn.Usage != nil {
		attachUsage(event, turn.Usage)
	}
	return turn, cursor
}

// EmitTurn emits the turn's recovered spans and advances the native-OTel cursor
// together, gated on an intact trace context. It is a no-op for an event that
// ended no turn, so the caller needs no condition of its own.
//
// Agents come before tools: a sub-agent's tools parent onto its invoke_agent
// span, so the parent must reach the wire first.
//
// A blank TraceID skips everything. pipeline.Process refuses to emit the chat
// span in that case too, so leaving the cursor put folds this turn's usage and
// tools into a later turn instead of dropping them.
func EmitTurn(turn *Turn, cursor, sessionID string, ctx *otlp.TraceContext, cfg otlp.Config) {
	if turn == nil || sessionID == "" {
		return
	}
	if ctx == nil || ctx.TraceID == "" {
		return
	}
	emitAgentSpans(turn, ctx, cfg)
	emitToolSpans(turn, ctx, cfg)
	SaveCursor(sessionID, cursor)
}

// attachUsage sets the per-turn token, model and response attributes on the Stop
// event.
func attachUsage(event map[string]any, u *Usage) {
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
	// The agentStop payload carries only stopReason, so the final assistant
	// message comes from the native-OTel chat span. The pipeline renders
	// last_assistant_message as gen_ai.output.messages.
	if u.ResponseText != "" {
		if _, has := event["last_assistant_message"]; !has {
			event["last_assistant_message"] = u.ResponseText
		}
	}
}

// emitToolSpans emits one execute_tool span per tool call recovered from the
// native-OTel file, onto the turn's trace. Native span ids are reused verbatim
// (the same 16-hex format as ours, so re-reads stay idempotent), timings are the
// tool's real start and end, and parents follow the native tree: a sub-agent's
// tools nest under its invoke_agent span, top-level tools under the chat span.
// The events run through the same extractor enrichments as hook-sourced tool
// events elsewhere, so OmitIO redaction and the dash0.gen_ai.* details match.
func emitToolSpans(turn *Turn, ctx *otlp.TraceContext, cfg otlp.Config) {
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

		// Derive the shared semantic attributes: URLs, line counts, bash and skill,
		// MCP server plus normalized name.
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
// OpenTelemetry describes that layer, so the tree keeps it rather than collapsing
// the sub-agent onto its spawning tool span.
//
// The event is shaped so the pipeline's existing mapping does the work:
// agent_type becomes gen_ai.agent.name and names the span, agent_id becomes
// gen_ai.agent.id. The same keys Claude and Codex produce.
//
// No usage is attached. A sub-agent's chat spans already fold into the parent
// turn's total, so repeating the tokens here would double them for anyone
// summing across a trace. The native span carries no usage either.
func emitAgentSpans(turn *Turn, ctx *otlp.TraceContext, cfg otlp.Config) {
	for _, sa := range turn.Agents {
		agentType := sa.AgentType
		if agentType == "" {
			// NewLLMSpan reads agent_type to decide this is an invoke_agent span,
			// so an unnamed agent would silently become a chat span.
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
