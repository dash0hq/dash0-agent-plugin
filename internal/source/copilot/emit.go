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

// The emitters below are shared by both Copilot entrypoints: the CLI recovers a
// Turn from the native-OTel file, the desktop app (internal/source/copilotapp)
// builds one from session events. Either way the same spans go out.

// AttachUsage sets the per-turn token, model and response attributes on the Stop
// event.
func AttachUsage(event map[string]any, u *Usage) {
	if !u.NoTokens {
		attachTokens(event, u)
	}
	if u.Model != "" {
		if _, has := event["model"]; !has {
			event["model"] = u.Model
		}
	}
	// Both go out: only the responding model is priceable when none was pinned.
	if u.ResponseModel != "" {
		if _, has := event["response_model"]; !has {
			event["response_model"] = u.ResponseModel
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

func attachTokens(event map[string]any, u *Usage) {
	event["gen_ai.usage.input_tokens"] = u.InputTokens
	event["gen_ai.usage.output_tokens"] = u.OutputTokens
	event["gen_ai.usage.cache_read.input_tokens"] = u.CacheReadInputTokens
	if u.CacheCreationInputTokens > 0 {
		event["gen_ai.usage.cache_creation.input_tokens"] = u.CacheCreationInputTokens
	}
	if u.ReasoningOutputTokens > 0 {
		event["gen_ai.usage.reasoning.output_tokens"] = u.ReasoningOutputTokens
	}
}

// EmitToolSpans emits one execute_tool span per tool call recovered from the
// native-OTel file, onto the turn's trace: native span ids are reused verbatim
// (same 16-hex format as ours — idempotent across re-reads), timings are the
// tool's real start/end, and parents follow the native tree — a sub-agent's
// tools nest under its invoke_agent span (see EmitAgentSpans), top-level tools
// under the turn's chat span. Events are synthesized in the pipeline's
// canonical shape and run through the same extractor enrichments as
// hook-sourced tool events on the other runtimes, so OmitIO redaction and the
// dash0.gen_ai.* details stay uniform.
func EmitToolSpans(turn *Turn, ctx *otlp.TraceContext, cfg otlp.Config, logPrefix string) {
	agents := map[string]SubAgent{}
	for _, sa := range turn.Agents {
		agents[sa.SpanID] = sa
	}
	for _, tc := range turn.Tools {
		event := map[string]any{
			"session_id": ctx.SessionID,
			"tool_name":  tc.Name,
		}
		if turn.Cwd != "" {
			event["cwd"] = turn.Cwd
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
		addTurnModels(event, turn.Usage)
		if tc.SkillName != "" {
			event["skill_name"] = tc.SkillName
		}
		// A sub-agent's tool names that sub-agent, as its invoke_agent span does,
		// instead of falling back to the configured agent name.
		if sa, ok := agents[tc.ParentSpanID]; ok {
			addAgentIdentity(event, sa)
			if req, resp := agentModels(sa); resp != "" {
				event["model"] = req
				event["response_model"] = resp
			}
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
			fmt.Fprintf(os.Stderr, "%s: tool span export: %v\n", logPrefix, err)
		}
	}
}

// addTurnModels names the turn's models on a span that does not carry the
// turn's tokens, so its response model is the main agent's reply model, when
// known, rather than the one the chat span prices at.
func addTurnModels(event map[string]any, u *Usage) {
	if u == nil {
		return
	}
	if u.Model != "" {
		event["model"] = u.Model
	}
	response := u.ReplyModel
	if response == "" {
		response = u.ResponseModel
	}
	if response != "" {
		event["response_model"] = response
	}
}

// agentModels is a sub-agent's requested and responding model. Its own usage
// keeps the request "auto" when auto mode picked the model; otherwise both are
// the model its subagent events name. Empty when the source reports neither.
func agentModels(sa SubAgent) (request, response string) {
	if sa.Usage != nil && sa.Usage.ResponseModel != "" {
		return sa.Usage.Model, sa.Usage.ResponseModel
	}
	return sa.Model, sa.Model
}

// EmitAgentSpans emits one invoke_agent span per sub-agent the turn spawned,
// between the `task` tool that spawned it and the tools it ran. Copilot's own
// OpenTelemetry describes that layer and the plugin used to collapse it, which
// left the sub-agent's identity with nowhere standard to go and put a custom
// key on the tool span instead.
//
// The event is shaped so the pipeline's existing mapping does the work:
// agent_type becomes gen_ai.agent.name and drives the invoke_agent span name,
// agent_id becomes gen_ai.agent.id. Same keys as Claude and Codex produce.
//
// Usage is attached only when the source attributed it to the sub-agent (the
// app does), and then the turn's chat span leaves it out. The CLI's file folds
// a sub-agent's tokens into the parent turn's total, so there the same tokens
// here as well would double them for anyone summing across a trace.
func EmitAgentSpans(turn *Turn, ctx *otlp.TraceContext, cfg otlp.Config, logPrefix string) {
	for _, sa := range turn.Agents {
		event := map[string]any{"session_id": ctx.SessionID}
		if turn.Cwd != "" {
			event["cwd"] = turn.Cwd
		}
		addAgentIdentity(event, sa)
		if req, resp := agentModels(sa); resp != "" {
			event["model"] = req
			event["response_model"] = resp
		} else {
			addTurnModels(event, turn.Usage)
		}

		if sa.Usage != nil {
			attachTokens(event, sa.Usage)
		}

		parent := sa.ParentSpanID
		if parent == "" {
			parent = ctx.SpanID // no spawning tool span this turn → the chat span
		}
		span := otlp.NewLLMSpan(ctx.TraceID, sa.SpanID, parent, sa.Start, sa.End, event, sa.Failed, cfg)
		if err := otlp.SendTrace(span, event, cfg); err != nil {
			fmt.Fprintf(os.Stderr, "%s: agent span export: %v\n", logPrefix, err)
		}
	}
}

// addAgentIdentity names the sub-agent on an event: agent_type becomes
// gen_ai.agent.name, agent_id gen_ai.agent.id.
func addAgentIdentity(event map[string]any, sa SubAgent) {
	agentType := sa.AgentType
	if agentType == "" {
		// NewLLMSpan reads agent_type to decide it is an invoke_agent span at
		// all, so an unnamed agent would silently become a chat span.
		agentType = "agent"
	}
	event["agent_type"] = agentType
	if sa.CallID != "" {
		event["agent_id"] = sa.CallID
	}
}
