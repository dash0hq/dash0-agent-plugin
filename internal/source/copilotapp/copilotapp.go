// SPDX-FileCopyrightText: Copyright 2026 Dash0 Inc.
// SPDX-License-Identifier: Apache-2.0

// Package copilotapp turns what the GitHub Copilot desktop app's session
// extension (copilot-app/extension.mjs) forwards into the pipeline's canonical
// events, and builds each turn — tokens, model, response, tool executions and
// sub-agents — from the SDK session events the extension buffered for it.
//
// The app needs none of the CLI's indirection. There is no native-OTel file to
// read and no launcher to set one up: the extension subscribes to the session
// event stream, which carries usage (assistant.usage), the response
// (assistant.message), real tool timings (tool.execution_start/complete) and
// the sub-agent tree (subagent.*, parentToolCallId) directly. The resulting
// Turn is the CLI's own type, so both runtimes emit identical spans through
// copilot.EmitToolSpans / copilot.EmitAgentSpans.
//
// The extension sends four events, named on argv:
//
//   - sessionStart        → SessionStart
//   - userPromptSubmitted → UserPromptSubmit (main-agent user.message only)
//   - turnEnd             → Stop, with the turn's buffered session events
//   - sessionEnd          → SessionEnd
package copilotapp

import (
	"encoding/json"
	"strings"
	"time"

	"github.com/dash0hq/dash0-agent-plugin/internal/otlp"
	"github.com/dash0hq/dash0-agent-plugin/internal/pipeline"
	"github.com/dash0hq/dash0-agent-plugin/internal/source/copilot"
)

var eventNameMap = map[string]string{
	"sessionStart":        "SessionStart",
	"userPromptSubmitted": "UserPromptSubmit",
	"turnEnd":             "Stop",
	"sessionEnd":          "SessionEnd",
}

// Normalize maps an extension payload to the pipeline's canonical event. It
// returns nil for an unknown event name or a null payload.
//
// It copies a fixed set of fields instead of passing the payload through: the
// span builder turns every key it does not recognize into an attribute, so the
// buffered session events, or any field a later extension version adds, would
// otherwise ship on the chat span.
func Normalize(eventName string, payload map[string]any) map[string]any {
	if payload == nil {
		return nil
	}
	canonical, ok := eventNameMap[eventName]
	if !ok {
		return nil
	}
	event := map[string]any{"hook_event_name": canonical}
	if sid, _ := payload["sessionId"].(string); sid != "" {
		event["session_id"] = sid
	}
	if cwd, _ := payload["cwd"].(string); cwd != "" {
		event["cwd"] = cwd
	}
	if canonical == "UserPromptSubmit" {
		if p, ok := payload["prompt"].(string); ok {
			event["prompt"] = p
			// Copilot injects agent-side context (e.g. a background agent
			// finishing) as a user.message wrapped in <system_notification>. The
			// model reacts to it; nobody typed it.
			if strings.HasPrefix(strings.TrimSpace(p), "<system_notification>") {
				event["prompt_role"] = "assistant"
			}
		}
	}
	return event
}

// Timestamp returns the payload's ISO-8601 timestamp — when the extension saw
// the event, not when the binary got to it — or fallback when absent.
func Timestamp(payload map[string]any, fallback time.Time) time.Time {
	if raw, _ := payload["timestamp"].(string); raw != "" {
		if t, err := time.Parse(time.RFC3339Nano, raw); err == nil {
			return t.UTC()
		}
	}
	return fallback
}

// Event is one SDK session event as the extension forwards it: the envelope's
// type, timestamp and agentId, and its data.
type Event struct {
	Type      string         `json:"type"`
	Timestamp string         `json:"timestamp"`
	AgentID   string         `json:"agentId,omitempty"`
	Data      map[string]any `json:"data"`
}

func (e Event) time() time.Time {
	t, _ := time.Parse(time.RFC3339Nano, e.Timestamp)
	return t
}

func (e Event) str(key string) string {
	s, _ := e.Data[key].(string)
	return s
}

func (e Event) num(key string) int64 {
	f, _ := e.Data[key].(float64)
	return int64(f)
}

// isSubAgent reports whether the event belongs to a sub-agent rather than the
// main agent. The envelope agentId is the current marker; parentToolCallId is
// deprecated but still sent, and an older app may send only that.
func (e Event) isSubAgent() bool {
	return e.AgentID != "" || e.str("parentToolCallId") != ""
}

// DecodeEvents reads the payload's "events" array. Malformed entries are
// skipped rather than failing the turn.
func DecodeEvents(payload map[string]any) []Event {
	raw, ok := payload["events"].([]any)
	if !ok {
		return nil
	}
	events := make([]Event, 0, len(raw))
	for _, item := range raw {
		b, err := json.Marshal(item)
		if err != nil {
			continue
		}
		var e Event
		if json.Unmarshal(b, &e) != nil || e.Type == "" {
			continue
		}
		if e.Data == nil {
			e.Data = map[string]any{}
		}
		events = append(events, e)
	}
	return events
}

// ToolSpanID derives an execute_tool span id from its tool call id, and
// AgentSpanID the id of the invoke_agent span that call spawned. Deterministic,
// so a parent can be named before it is built, and distinct, because a `task`
// call stands for both spans.
func ToolSpanID(toolCallID string) string { return otlp.SpanIDFromAgentID("tool:" + toolCallID) }

// AgentSpanID: see ToolSpanID.
func AgentSpanID(toolCallID string) string { return otlp.SpanIDFromAgentID("agent:" + toolCallID) }

// BuildTurn assembles the turn the events describe. It returns nil when there
// is nothing to report, so the caller emits a bare chat span.
//
// Usage is summed across the main agent and its sub-agents: attribution stays
// flat, matching the CLI, so a trace's total is the chat span's. The model and
// response are the main agent's.
//
// end closes tool calls and sub-agents that started but never finished (an
// aborted turn); they are marked failed.
func BuildTurn(events []Event, end time.Time) *copilot.Turn {
	if len(events) == 0 {
		return nil
	}
	turn := &copilot.Turn{}

	// A sub-agent's events name the `task` call that spawned it. Collect those
	// first so its tools nest under its invoke_agent span whatever the order.
	spawned := map[string]bool{}
	for _, e := range events {
		if e.Type == "subagent.started" && e.str("toolCallId") != "" {
			spawned[e.str("toolCallId")] = true
		}
	}
	parentFor := func(e Event) string {
		ptc := e.str("parentToolCallId")
		switch {
		case ptc == "":
			return "" // main agent → the chat span
		case spawned[ptc]:
			return AgentSpanID(ptc)
		default:
			return ToolSpanID(ptc)
		}
	}

	var usage copilot.Usage
	sawUsage := false
	toolIndex := map[string]int{}
	agentIndex := map[string]int{}

	for _, e := range events {
		switch e.Type {
		case "assistant.usage":
			sawUsage = true
			usage.InputTokens += e.num("inputTokens")
			usage.OutputTokens += e.num("outputTokens")
			usage.CacheReadInputTokens += e.num("cacheReadTokens")
			usage.CacheCreationInputTokens += e.num("cacheWriteTokens")
			usage.ReasoningOutputTokens += e.num("reasoningTokens")
			if m := e.str("model"); m != "" && !e.isSubAgent() {
				// Auto mode resolves per call. What was asked for is "auto",
				// which is what the CLI reports too; the responding model is the
				// priceable one.
				usage.Model = m
				if auto, _ := e.Data["isAuto"].(bool); auto {
					usage.Model = "auto"
				}
				usage.ResponseModel = m
			}

		case "assistant.message":
			if c := e.str("content"); strings.TrimSpace(c) != "" && !e.isSubAgent() {
				usage.ResponseText = c
			}

		case "tool.execution_start":
			callID := e.str("toolCallId")
			if callID == "" {
				continue
			}
			name := e.str("toolName")
			if server, tool := e.str("mcpServerName"), e.str("mcpToolName"); server != "" && tool != "" {
				// The pipeline's MCP extractors read the server from this form.
				name = "mcp__" + server + "__" + tool
			}
			tc := copilot.ToolCall{
				SpanID:       ToolSpanID(callID),
				ParentSpanID: parentFor(e),
				Name:         name,
				Arguments:    arguments(e.Data["arguments"]),
				CallID:       callID,
				Start:        e.time(),
			}
			if strings.EqualFold(name, "skill") {
				tc.SkillName = pipeline.ExtractSkillName(e.Data["arguments"])
			}
			toolIndex[callID] = len(turn.Tools)
			turn.Tools = append(turn.Tools, tc)

		case "tool.execution_complete":
			i, ok := toolIndex[e.str("toolCallId")]
			if !ok {
				continue
			}
			tc := &turn.Tools[i]
			tc.End = e.time()
			if success, ok := e.Data["success"].(bool); ok && !success {
				tc.Failed = true
			}
			tc.Result = toolResult(e.Data)

		case "subagent.started":
			callID := e.str("toolCallId")
			if callID == "" {
				continue
			}
			agentType := e.str("agentName")
			agentIndex[callID] = len(turn.Agents)
			turn.Agents = append(turn.Agents, copilot.SubAgent{
				SpanID:       AgentSpanID(callID),
				ParentSpanID: ToolSpanID(callID),
				AgentType:    agentType,
				CallID:       callID,
				Model:        e.str("model"),
				Start:        e.time(),
			})

		case "subagent.completed", "subagent.failed":
			i, ok := agentIndex[e.str("toolCallId")]
			if !ok {
				continue
			}
			if m := e.str("model"); m != "" {
				turn.Agents[i].Model = m
			}
			turn.Agents[i].End = e.time()
			turn.Agents[i].Failed = e.Type == "subagent.failed"
		}
	}

	for i := range turn.Tools {
		t := &turn.Tools[i]
		closeOpen(&t.Start, &t.End, &t.Failed, end)
	}
	for i := range turn.Agents {
		a := &turn.Agents[i]
		closeOpen(&a.Start, &a.End, &a.Failed, end)
	}

	if sawUsage || usage.ResponseText != "" {
		turn.Usage = &usage
	}
	if turn.Usage == nil && len(turn.Tools) == 0 && len(turn.Agents) == 0 {
		return nil
	}
	return turn
}

// closeOpen ends a span that never saw its completion event at the turn's end,
// as failed: the turn was over and it was not.
func closeOpen(start, end *time.Time, failed *bool, turnEnd time.Time) {
	if start.IsZero() {
		*start = turnEnd
	}
	if end.IsZero() {
		*end = turnEnd
		*failed = true
	}
	if end.Before(*start) {
		*end = *start
	}
}

// arguments renders tool arguments as the JSON string copilot.ToolCall holds.
// The app sends an object; a string is passed through.
func arguments(v any) string {
	switch val := v.(type) {
	case nil:
		return ""
	case string:
		return val
	default:
		b, err := json.Marshal(val)
		if err != nil {
			return ""
		}
		return string(b)
	}
}

// toolResult is what the model saw: the result content on success, the error
// message on failure.
func toolResult(data map[string]any) string {
	if r, ok := data["result"].(map[string]any); ok {
		if c, _ := r["content"].(string); c != "" {
			return c
		}
	}
	if e, ok := data["error"].(map[string]any); ok {
		if m, _ := e["message"].(string); m != "" {
			return m
		}
	}
	return ""
}
