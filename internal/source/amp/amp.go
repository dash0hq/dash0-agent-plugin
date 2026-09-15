// SPDX-FileCopyrightText: Copyright 2026 Dash0 Inc.
// SPDX-License-Identifier: Apache-2.0

package amp

import (
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"time"

	"github.com/dash0hq/dash0-agent-plugin/internal/otlp"
	"github.com/dash0hq/dash0-agent-plugin/internal/pipeline"
	"github.com/dash0hq/dash0-agent-plugin/internal/version"
)

// Turn is the private bridge protocol, not an Amp API response. Prompt and
// Response carry the turn's conversation, which the shared exporter redacts
// when OmitIO is set — the same contract as every other harness.
type Turn struct {
	ThreadID     string            `json:"thread_id"`
	ID           json.RawMessage   `json:"id"`
	Start        time.Time         `json:"start"`
	End          time.Time         `json:"end"`
	Status       string            `json:"status"`
	Executor     string            `json:"executor"`
	Tools        []Tool            `json:"tools"`
	AssistantIDs []json.RawMessage `json:"assistant_ids"`
	Truncated    bool              `json:"truncated"`
	Prompt       string            `json:"prompt"`
	Response     string            `json:"response"`
}

// Tool contains a paired call/result, their observed times, and the arguments
// and output the bridge budgeted for this turn.
type Tool struct {
	ID     string    `json:"id"`
	Name   string    `json:"name"`
	Start  time.Time `json:"start"`
	End    time.Time `json:"end"`
	Status string    `json:"status"`
	Input  string    `json:"input"`
	Output string    `json:"output"`
}

var threadPattern = regexp.MustCompile(`^T-[A-Za-z0-9_-]{1,128}$`)

func validStatus(s string) bool { return s == "done" || s == "error" || s == "cancelled" }

// Validate rejects ambiguous or oversized bridge data before any subprocess.
func (t Turn) Validate() error {
	if !threadPattern.MatchString(t.ThreadID) || messageKey(t.ID) == "" || t.Start.IsZero() || t.End.Before(t.Start) || !validStatus(t.Status) || len(t.Tools) > 512 || len(t.AssistantIDs) > 512 {
		return fmt.Errorf("invalid Amp turn")
	}
	if t.Executor != "local" && t.Executor != "remote" && t.Executor != "unknown" {
		return fmt.Errorf("invalid Amp executor")
	}
	for _, id := range t.AssistantIDs {
		if messageKey(id) == "" {
			return fmt.Errorf("invalid Amp message ID")
		}
	}
	return nil
}

// BuildTrace builds one completed turn with the shared span and enrichment rules.
func BuildTrace(t Turn, usage []Usage, usageStatus string, cfg otlp.Config) (otlp.ExportTracesRequest, error) {
	if err := t.Validate(); err != nil {
		return otlp.ExportTracesRequest{}, err
	}
	cwd, err := os.Getwd()
	if err != nil {
		return otlp.ExportTracesRequest{}, err
	}
	cfg = cfg.WithSpanContext()
	traceID, err := otlp.GenerateTraceID()
	if err != nil {
		return otlp.ExportTracesRequest{}, err
	}
	rootID := otlp.SpanIDFromSessionID(traceID)
	base := func() map[string]any {
		return map[string]any{
			"session_id":              t.ThreadID,
			"cwd":                     cwd,
			"dash0.amp.executor.kind": t.Executor,
			"dash0.amp.turn.id":       messageKey(t.ID),
		}
	}
	// The turn's root is a chat span, as it is on every other harness: Dash0
	// reads a session's turns from chat spans and treats invoke_agent as a
	// sub-agent invocation, so a root named invoke_agent left each exported
	// model call standing alone instead of under the turn that made it.
	// gen_ai.agent.name still resolves to the configured agent because
	// eventAttributes falls back to cfg.AgentName when agent_type is absent.
	root := base()
	root["dash0.amp.usage.status"] = usageStatus
	root["dash0.amp.truncated"] = t.Truncated
	if t.Status != "done" {
		root["error"] = "Amp turn " + t.Status
	}
	// The shared exporter redacts and truncates these under OmitIO, so the
	// privacy decision stays in one place rather than being made twice.
	if t.Prompt != "" {
		root["prompt"] = t.Prompt
	}
	if t.Response != "" {
		root["last_assistant_message"] = t.Response
	}
	// The last selected record is the message that answered the turn, so it
	// names the root and supplies its usage — the same record the other
	// harnesses put on their turn span. Earlier records stay separate children
	// rather than being summed into one model's name.
	if len(usage) > 0 {
		last := usage[len(usage)-1]
		usage = usage[:len(usage)-1]
		root["model"] = last.Model
		root["dash0.amp.usage.source"] = "thread-export"
		for k, v := range last.Attributes {
			root[k] = v
		}
	}
	spans := []otlp.Span{otlp.NewLLMSpan(traceID, rootID, "", t.Start, t.End, root, t.Status != "done", cfg)}
	seen := make(map[string]bool)
	for _, tool := range t.Tools {
		if tool.ID == "" || len(tool.ID) > 256 || tool.Name == "" || len(tool.Name) > 256 || seen[tool.ID] || !validStatus(tool.Status) || tool.Start.Before(t.Start) || tool.End.Before(tool.Start) || tool.End.After(t.End) {
			continue
		}
		seen[tool.ID] = true
		event := base()
		event["tool_name"] = tool.Name
		event["tool_use_id"] = tool.ID
		if tool.Input != "" {
			// Decode so the shared extractors see the map shape hooks deliver on
			// the other runtimes; a non-object payload is kept as its own text.
			var args map[string]any
			if json.Unmarshal([]byte(tool.Input), &args) == nil && args != nil {
				event["tool_input"] = args
			} else {
				event["tool_input"] = tool.Input
			}
		}
		if tool.Output != "" {
			event["tool_response"] = tool.Output
		}
		if tool.Status != "done" {
			event["error"] = "Amp tool " + tool.Status
		}
		// Derives the MCP server and normalized name this used to do by hand,
		// plus the URL, commit and line-count details the other sources get.
		pipeline.EnrichToolEvent(event)
		spanID := otlp.SpanIDFromAgentID("tool:" + tool.ID)
		spans = append(spans, otlp.NewToolSpan(traceID, spanID, rootID, tool.Start, tool.End, event, tool.Status != "done", cfg))
	}
	for i, call := range usage {
		event := base()
		event["model"] = call.Model
		event["dash0.amp.usage.source"] = "thread-export"
		event["dash0.amp.timing"] = "observation"
		for k, v := range call.Attributes {
			event[k] = v
		}
		// Keyed by the turn as well as the position: "usage:0" alone is the same
		// string in every turn, so every trace reused one span ID.
		spanID := otlp.SpanIDFromAgentID(fmt.Sprintf("usage:%s:%d", messageKey(t.ID), i))
		spans = append(spans, otlp.NewLLMSpan(traceID, spanID, rootID, t.End, t.End, event, false, cfg))
	}
	return otlp.ExportTracesRequest{ResourceSpans: []otlp.ResourceSpans{{
		Resource:   otlp.Resource{Attributes: []otlp.Attribute{{Key: "service.name", Value: otlp.StringVal(cfg.AgentName)}, {Key: "service.version", Value: otlp.StringVal(version.Version)}}},
		ScopeSpans: []otlp.ScopeSpans{{Scope: otlp.Scope{Name: "dash0-agent-plugin", Version: version.Version}, Spans: spans}},
	}}}, nil
}
