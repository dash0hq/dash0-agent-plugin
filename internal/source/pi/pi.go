// SPDX-FileCopyrightText: Copyright 2026 Dash0 Inc.
// SPDX-License-Identifier: Apache-2.0

// Package pi builds the spans of one completed Pi turn, which pi/index.ts
// collects from Pi's extension events and hands over in one envelope.
package pi

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

// MaxEnvelopeBytes is the largest envelope the helper accepts. The bridge
// sheds content to stay below it.
const MaxEnvelopeBytes = 1 << 20

const maxItems = 512

// Turn is the private bridge protocol, not a Pi API type. It holds one user
// turn: from the run that a prompt started until Pi settles, so retries,
// compaction retries and queued follow-ups belong to the same turn. Prompt,
// Response and tool content are exported only when omit_io is off; the shared
// exporter makes that decision.
type Turn struct {
	SessionID string `json:"session_id"`
	// ParentSessionID is set when Pi forked or cloned this session from another.
	ParentSessionID string    `json:"parent_session_id"`
	Start           time.Time `json:"start"`
	End             time.Time `json:"end"`
	// Status is "done", "error" when the last model call failed, or "aborted"
	// when the user stopped the run.
	Status    string `json:"status"`
	Calls     []Call `json:"calls"`
	Tools     []Tool `json:"tools"`
	Truncated bool   `json:"truncated"`
	Prompt    string `json:"prompt"`
	Response  string `json:"response"`
}

// Call is one model request: one assistant message as Pi finalized it.
type Call struct {
	// Provider is Pi's provider ID, for example "anthropic" or "claude-code".
	Provider      string    `json:"provider"`
	Model         string    `json:"model"`
	ResponseModel string    `json:"response_model"`
	Start         time.Time `json:"start"`
	End           time.Time `json:"end"`
	StopReason    string    `json:"stop_reason"`
	// ThinkingLevel is the provider-native effort level of the request.
	ThinkingLevel string `json:"thinking_level"`
	// BillingMode is "subscription" when Pi used a subscription login for the
	// call, else "unknown". An API key does not prove list price.
	BillingMode string `json:"billing_mode"`
	Usage       Usage  `json:"usage"`
}

// Usage holds Pi's normalized token counts. Pi reports input exclusive of
// cache reads and writes for every provider; output includes reasoning.
type Usage struct {
	Input      int64 `json:"input"`
	Output     int64 `json:"output"`
	CacheRead  int64 `json:"cache_read"`
	CacheWrite int64 `json:"cache_write"`
	// CacheWrite1h is the part of CacheWrite with 1-hour retention. Only
	// Anthropic reports it.
	CacheWrite1h *int64 `json:"cache_write_1h"`
	Reasoning    *int64 `json:"reasoning"`
}

// Tool is one finalized tool execution.
type Tool struct {
	ID string `json:"id"`
	// ParentID is the ID of the tool that made this call, for example a
	// codemode script. Empty for a call the model made.
	ParentID string    `json:"parent_id"`
	Name     string    `json:"name"`
	Start    time.Time `json:"start"`
	End      time.Time `json:"end"`
	Error    bool      `json:"error"`
	// Call is the index in Turn.Calls of the model call that requested this
	// tool, or nil when the bridge saw none.
	Call   *int   `json:"call"`
	Input  string `json:"input"`
	Output string `json:"output"`
}

// Pi's own session ID rule (assertValidSessionId), with a length bound.
var sessionPattern = regexp.MustCompile(`^[A-Za-z0-9](?:[A-Za-z0-9._-]{0,254}[A-Za-z0-9])?$`)

// Provider IDs and model names are free text in Pi, because extensions can
// register providers. This only keeps them to a sane size and alphabet.
var namePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:/@+\[\]-]{0,255}$`)

func validStatus(s string) bool { return s == "done" || s == "error" || s == "aborted" }

// Validate rejects ambiguous or oversized bridge data before any export.
func (t Turn) Validate() error {
	if !sessionPattern.MatchString(t.SessionID) || t.Start.IsZero() || t.End.Before(t.Start) || !validStatus(t.Status) || len(t.Tools) > maxItems || len(t.Calls) > maxItems {
		return fmt.Errorf("invalid Pi turn")
	}
	if t.ParentSessionID != "" && !sessionPattern.MatchString(t.ParentSessionID) {
		return fmt.Errorf("invalid Pi parent session")
	}
	for _, c := range t.Calls {
		u := c.Usage
		if !namePattern.MatchString(c.Provider) || !namePattern.MatchString(c.Model) || c.End.Before(c.Start) ||
			u.Input < 0 || u.Output < 0 || u.CacheRead < 0 || u.CacheWrite < 0 ||
			(u.CacheWrite1h != nil && (*u.CacheWrite1h < 0 || *u.CacheWrite1h > u.CacheWrite)) ||
			(u.Reasoning != nil && (*u.Reasoning < 0 || *u.Reasoning > u.Output)) {
			return fmt.Errorf("invalid Pi model call")
		}
		if c.BillingMode != "" && c.BillingMode != "subscription" && c.BillingMode != "unknown" {
			return fmt.Errorf("invalid Pi billing mode")
		}
		if c.ResponseModel != "" && !namePattern.MatchString(c.ResponseModel) {
			return fmt.Errorf("invalid Pi response model")
		}
	}
	return nil
}

// providers maps Pi's built-in provider IDs to gen_ai.provider.name values.
// An ID not listed is exported as it is, as OpenCode V2 does.
var providers = map[string]string{
	"anthropic":      "anthropic",
	"claude-code":    "anthropic",
	"openai":         "openai",
	"openai-codex":   "openai",
	"azure":          "azure.ai.openai",
	"google":         "gcp.gemini",
	"google-vertex":  "gcp.vertex_ai",
	"amazon-bedrock": "aws.bedrock",
	"xai":            "x_ai",
	"deepseek":       "deepseek",
	"mistral":        "mistral_ai",
	"groq":           "groq",
}

func providerName(id string) string {
	if p, ok := providers[id]; ok {
		return p
	}
	return id
}

// callAttributes puts one call's model and token counts on a span event.
func callAttributes(event map[string]any, c Call) {
	event["model"] = c.Model
	event["dash0.pi.provider"] = c.Provider
	if c.ResponseModel != "" && c.ResponseModel != c.Model {
		event["response_model"] = c.ResponseModel
	}
	if c.ThinkingLevel != "" {
		event["effort"] = c.ThinkingLevel
	}
	u := c.Usage
	// Pi fills absent counts with zeros. A call that reports nothing at all,
	// such as one aborted before the response, has no measurement to export.
	if u.Input+u.Output+u.CacheRead+u.CacheWrite == 0 {
		return
	}
	// Exported inclusive of cache, as the GenAI conventions define it and as
	// Dash0's cost calculation expects from every harness but Claude Code.
	event["gen_ai.usage.input_tokens"] = u.Input + u.CacheRead + u.CacheWrite
	event["gen_ai.usage.output_tokens"] = u.Output
	event["gen_ai.usage.cache_read.input_tokens"] = u.CacheRead
	event["gen_ai.usage.cache_creation.input_tokens"] = u.CacheWrite
	// Only beside tokens, as on Claude Code: the mode says what a cost
	// figure means, and a span with no tokens has none.
	if c.BillingMode != "" {
		event["dash0.gen_ai.billing_mode"] = c.BillingMode
	}
	if u.CacheWrite1h != nil {
		event["dash0.gen_ai.usage.cache_creation.ephemeral_1h.input_tokens"] = *u.CacheWrite1h
	}
	if u.Reasoning != nil && *u.Reasoning > 0 {
		event["gen_ai.usage.reasoning.output_tokens"] = *u.Reasoning
	}
}

func callFailed(c Call) bool { return c.StopReason == "error" || c.StopReason == "aborted" }

// BuildTrace builds one completed turn on the shared span builders.
//
// The root is the turn's chat span. The last model call names it and
// supplies its tokens, as on every other harness; each earlier call is a
// separate chat child with its own model, provider, timing and tokens. Each
// call's tokens are on exactly one span, so a sum over the trace is the turn.
func BuildTrace(t Turn, cfg otlp.Config) (otlp.ExportTracesRequest, error) {
	if err := t.Validate(); err != nil {
		return otlp.ExportTracesRequest{}, err
	}
	cwd, err := os.Getwd()
	if err != nil {
		return otlp.ExportTracesRequest{}, fmt.Errorf("reading working directory: %w", err)
	}
	cfg = cfg.WithSpanContext()
	traceID, err := otlp.GenerateTraceID()
	if err != nil {
		return otlp.ExportTracesRequest{}, fmt.Errorf("building Pi turn: %w", err)
	}
	rootID := otlp.SpanIDFromSessionID(traceID)
	base := func() map[string]any {
		event := map[string]any{"session_id": t.SessionID, "cwd": cwd}
		if t.ParentSessionID != "" {
			event["dash0.pi.session.parent.id"] = t.ParentSessionID
		}
		return event
	}

	root := base()
	if t.Truncated {
		root["dash0.pi.truncated"] = true
	}
	if t.Status != "done" {
		root["error"] = "Pi turn " + t.Status
	}
	if t.Prompt != "" {
		root["prompt"] = t.Prompt
	}
	if t.Response != "" {
		root["last_assistant_message"] = t.Response
	}
	rootCfg := cfg
	calls := t.Calls
	if len(calls) > 0 {
		last := calls[len(calls)-1]
		calls = calls[:len(calls)-1]
		callAttributes(root, last)
		rootCfg.Provider = providerName(last.Provider)
	}
	spans := []otlp.Span{otlp.NewLLMSpan(traceID, rootID, "", t.Start, t.End, root, t.Status != "done", rootCfg)}

	callSpanID := func(i int) string { return otlp.SpanIDFromAgentID(fmt.Sprintf("call:%s:%d", traceID, i)) }
	for i, c := range calls {
		event := base()
		callAttributes(event, c)
		if callFailed(c) {
			event["error"] = "Pi model call " + c.StopReason
		}
		callCfg := cfg
		callCfg.Provider = providerName(c.Provider)
		spans = append(spans, otlp.NewLLMSpan(traceID, callSpanID(i), rootID, c.Start, c.End, event, callFailed(c), callCfg))
	}

	toolSpanID := func(id string) string { return otlp.SpanIDFromAgentID("tool:" + traceID + ":" + id) }
	// A bad tool loses its own span, not the turn. The set is complete before
	// any span is built, because Pi reports a nested call before its parent.
	valid := make(map[string]bool)
	for _, tool := range t.Tools {
		if tool.ID != "" && len(tool.ID) <= 256 && tool.Name != "" && len(tool.Name) <= 256 && !tool.End.Before(tool.Start) {
			valid[tool.ID] = true
		}
	}
	emitted := make(map[string]bool)
	for _, tool := range t.Tools {
		if !valid[tool.ID] || emitted[tool.ID] {
			continue
		}
		emitted[tool.ID] = true
		event := base()
		event["tool_name"] = tool.Name
		event["tool_use_id"] = tool.ID
		toolCfg := cfg
		if tool.Call != nil && *tool.Call >= 0 && *tool.Call < len(t.Calls) {
			c := t.Calls[*tool.Call]
			event["model"] = c.Model
			toolCfg.Provider = providerName(c.Provider)
		}
		if tool.Input != "" {
			// Decoded so the shared extractors see the map shape that hooks
			// deliver on the other runtimes.
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
		if tool.Error {
			event["error"] = "Pi tool error"
		}
		pipeline.EnrichToolEvent(event)
		parent := rootID
		if tool.ParentID != "" && valid[tool.ParentID] {
			parent = toolSpanID(tool.ParentID)
		}
		spans = append(spans, otlp.NewToolSpan(traceID, toolSpanID(tool.ID), parent, tool.Start, tool.End, event, tool.Error, toolCfg))
	}

	return otlp.ExportTracesRequest{ResourceSpans: []otlp.ResourceSpans{{
		Resource:   otlp.Resource{Attributes: []otlp.Attribute{{Key: "service.name", Value: otlp.StringVal(cfg.AgentName)}, {Key: "service.version", Value: otlp.StringVal(version.Version)}}},
		ScopeSpans: []otlp.ScopeSpans{{Scope: otlp.Scope{Name: "dash0-agent-plugin", Version: version.Version}, Spans: spans}},
	}}}, nil
}
