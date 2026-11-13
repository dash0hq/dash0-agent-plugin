// SPDX-FileCopyrightText: Copyright 2026 Dash0 Inc.
// SPDX-License-Identifier: Apache-2.0

// Package pi builds the spans of one completed Pi turn, which pi/index.ts
// collects from Pi's extension events and hands over in one envelope.
package pi

import (
	"fmt"
	"os"
	"regexp"
	"time"

	"github.com/dash0hq/dash0-agent-plugin/internal/otlp"
	"github.com/dash0hq/dash0-agent-plugin/internal/pipeline"
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
	// when the user stopped the run. An aborted turn is not an error.
	Status    string `json:"status"`
	Calls     []Call `json:"calls"`
	Tools     []Tool `json:"tools"`
	Truncated bool   `json:"truncated"`
	Prompt    string `json:"prompt"`
	Response  string `json:"response"`
	// Skill names the skill that a person's /skill:name command loaded into
	// the prompt that started this turn.
	Skill string `json:"skill"`
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
	// Skill names the skill whose SKILL.md this call read. Pi has no skill
	// tool: the model loads a skill by reading its file.
	Skill string `json:"skill"`
	// Subagents are the children that a foreground pi-subagents `subagent`
	// call ran in the Pi process. A background child is a session of its own.
	Subagents []Subagent `json:"subagents"`
	// Usage is the model usage that the tool reported for its own work, for
	// example a tool that calls a model itself. It does not include the usage
	// of the tools that this tool called: each of them reports its own. Pi
	// gives no provider or model for it.
	Usage *Usage `json:"usage"`
}

// Pi's own session ID rule (assertValidSessionId), with a length bound.
var sessionPattern = regexp.MustCompile(`^[A-Za-z0-9](?:[A-Za-z0-9._-]{0,254}[A-Za-z0-9])?$`)

// Provider IDs and model names are free text in Pi, because extensions can
// register providers. This only keeps them to a sane size and alphabet.
var namePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:/@+\[\]-]{0,255}$`)

// Pi's skill name rule is lowercase letters, digits and hyphens. This accepts
// a little more, so a renamed rule costs nothing but the bound.
var skillPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$`)

func validStatus(s string) bool { return s == "done" || s == "error" || s == "aborted" }

// Validate rejects ambiguous or oversized bridge data before any export.
func (t Turn) Validate() error {
	if !sessionPattern.MatchString(t.SessionID) || t.Start.IsZero() || t.End.Before(t.Start) || !validStatus(t.Status) || len(t.Tools) > maxItems || len(t.Calls) > maxItems {
		return fmt.Errorf("invalid Pi turn")
	}
	if t.ParentSessionID != "" && !sessionPattern.MatchString(t.ParentSessionID) {
		return fmt.Errorf("invalid Pi parent session")
	}
	if t.Skill != "" && !skillPattern.MatchString(t.Skill) {
		return fmt.Errorf("invalid Pi skill")
	}
	return nil
}

// validSpan reports whether a span can be built from start and end. A zero
// time is outside the range of UnixNano, as Turn.Validate also checks.
func validSpan(start, end time.Time) bool { return !start.IsZero() && !end.Before(start) }

// valid reports whether a model call can be exported. Provider IDs and model
// names are free text in Pi, so a bad call loses its own span, not the turn.
func (c Call) valid() bool {
	return namePattern.MatchString(c.Provider) && namePattern.MatchString(c.Model) && validSpan(c.Start, c.End) &&
		validUsage(c.Usage) && validBillingMode(c.BillingMode) &&
		(c.ResponseModel == "" || namePattern.MatchString(c.ResponseModel))
}

func validBillingMode(m string) bool { return m == "" || m == "subscription" || m == "unknown" }

// validUsage checks the counts that every call has. callAttributes drops an
// inconsistent optional count by itself, so that count does not cost the call.
func validUsage(u Usage) bool {
	return u.Input >= 0 && u.Output >= 0 && u.CacheRead >= 0 && u.CacheWrite >= 0
}

func (tool Tool) valid() bool {
	return tool.ID != "" && len(tool.ID) <= 256 && tool.Name != "" && len(tool.Name) <= 256 && validSpan(tool.Start, tool.End)
}

// onParentCycle reports whether the parent chain of the tool id leads back
// to id. Bridge data can name a tool as its own parent, or two tools as each
// other's parent. Each tool on such a loop goes under the root instead, so no
// span is its own ancestor. parentOf holds only valid tools.
func onParentCycle(id string, parentOf map[string]string) bool {
	seen := map[string]bool{}
	for cur := parentOf[id]; cur != "" && !seen[cur]; cur = parentOf[cur] {
		if cur == id {
			return true
		}
		seen[cur] = true
	}
	return false
}

// validCalls drops the calls that cannot be exported and points each tool at
// its call's new index, or at none when its call was dropped.
func validCalls(t Turn) ([]Call, []Tool) {
	index := make([]int, len(t.Calls))
	var calls []Call
	for i, c := range t.Calls {
		index[i] = -1
		if c.valid() {
			index[i] = len(calls)
			calls = append(calls, c)
		}
	}
	tools := make([]Tool, len(t.Tools))
	for i, tool := range t.Tools {
		if tool.Call != nil {
			if *tool.Call >= 0 && *tool.Call < len(index) && index[*tool.Call] >= 0 {
				n := index[*tool.Call]
				tool.Call = &n
			} else {
				tool.Call = nil
			}
		}
		tools[i] = tool
	}
	return calls, tools
}

// providers maps Pi's built-in provider IDs to gen_ai.provider.name values.
// The keys are Pi's KnownProvider IDs, not its API IDs: Azure is "azure",
// and "azure-openai-responses" is the API that it uses.
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

// withProvider sets the provider of one span from Pi's provider ID. A mapped
// ID wins over the vendor of the model name, so a model that Azure or Bedrock
// serves keeps that provider. An unmapped ID, such as a router like
// "openrouter", does not say who serves the model, so the vendor of the model
// name wins, and the raw ID is the fallback, as on OpenCode V2.
func withProvider(cfg otlp.Config, id string) otlp.Config {
	if p, ok := providers[id]; ok {
		cfg.Provider = p
		cfg.PreferProvider = true
		return cfg
	}
	cfg.Provider = id
	cfg.PreferProvider = false
	return cfg
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
	// The user stopped the call. This is not an error, so the span status
	// stays unset and this attribute records the stop.
	if c.StopReason == "aborted" {
		event["dash0.pi.call.stop_reason"] = c.StopReason
	}
	if usageAttributes(event, c.Usage) && c.BillingMode != "" {
		// Only beside tokens, as on Claude Code: the mode says what a cost
		// figure means, and a span with no tokens has none.
		event["dash0.gen_ai.billing_mode"] = c.BillingMode
	}
}

// hasUsage reports whether u holds a measurement. Pi fills absent counts
// with zeros, so a call that reports nothing at all, such as one aborted
// before the response, has no measurement to export.
func hasUsage(u Usage) bool { return u.Input+u.Output+u.CacheRead+u.CacheWrite > 0 }

// usageAttributes puts the token counts of u on a span event. It returns
// false and sets nothing when u holds no measurement.
func usageAttributes(event map[string]any, u Usage) bool {
	if !hasUsage(u) {
		return false
	}
	// Exported inclusive of cache, as the GenAI conventions define it and as
	// Dash0's cost calculation expects from every harness but Claude Code.
	event["gen_ai.usage.input_tokens"] = u.Input + u.CacheRead + u.CacheWrite
	event["gen_ai.usage.output_tokens"] = u.Output
	event["gen_ai.usage.cache_read.input_tokens"] = u.CacheRead
	event["gen_ai.usage.cache_creation.input_tokens"] = u.CacheWrite
	// An optional count that contradicts its total is dropped alone. The
	// call and its other counts stay.
	if u.CacheWrite1h != nil && *u.CacheWrite1h >= 0 && *u.CacheWrite1h <= u.CacheWrite {
		event["dash0.gen_ai.usage.cache_creation.ephemeral_1h.input_tokens"] = *u.CacheWrite1h
	}
	if u.Reasoning != nil && *u.Reasoning > 0 && *u.Reasoning <= u.Output {
		event["gen_ai.usage.reasoning.output_tokens"] = *u.Reasoning
	}
	return true
}

// callFailed reports whether a model call failed. A call that the user
// stopped ("aborted") did not fail.
func callFailed(c Call) bool { return c.StopReason == "error" }

// turnFailed reports whether a turn failed. A turn that the user stopped
// did not fail.
func turnFailed(t Turn) bool { return t.Status != "done" && t.Status != "aborted" }

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
	t.Calls, t.Tools = validCalls(t)
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
		root["dash0.pi.turn.status"] = t.Status
	}
	if turnFailed(t) {
		root["error"] = "Pi turn " + t.Status
	}
	if t.Prompt != "" {
		root["prompt"] = t.Prompt
	}
	if t.Response != "" {
		root["last_assistant_message"] = t.Response
	}
	if t.Skill != "" {
		root["skill_name"] = t.Skill
		root["skill_source"] = "command"
	}
	rootCfg := cfg
	calls := t.Calls
	if len(calls) > 0 {
		last := calls[len(calls)-1]
		calls = calls[:len(calls)-1]
		callAttributes(root, last)
		rootCfg = withProvider(cfg, last.Provider)
	}
	spans := []otlp.Span{otlp.NewLLMSpan(traceID, rootID, "", t.Start, t.End, root, turnFailed(t), rootCfg)}

	callSpanID := func(i int) string { return otlp.SpanIDFromAgentID(fmt.Sprintf("call:%s:%d", traceID, i)) }
	for i, c := range calls {
		event := base()
		callAttributes(event, c)
		if callFailed(c) {
			event["error"] = "Pi model call " + c.StopReason
		}
		callCfg := withProvider(cfg, c.Provider)
		spans = append(spans, otlp.NewLLMSpan(traceID, callSpanID(i), rootID, c.Start, c.End, event, callFailed(c), callCfg))
	}

	toolSpanID := func(id string) string { return otlp.SpanIDFromAgentID("tool:" + traceID + ":" + id) }
	// A bad tool loses its own span, not the turn. The set is complete before
	// any span is built, because Pi reports a nested call before its parent.
	valid := make(map[string]bool)
	// parentOf holds the parent of the first valid tool with each ID, which
	// is the one that gets a span.
	parentOf := make(map[string]string)
	for _, tool := range t.Tools {
		if tool.valid() && !valid[tool.ID] {
			valid[tool.ID] = true
			parentOf[tool.ID] = tool.ParentID
		}
	}
	emitted := make(map[string]bool)
	for _, tool := range t.Tools {
		if !tool.valid() || emitted[tool.ID] {
			continue
		}
		emitted[tool.ID] = true
		event := base()
		event["tool_name"] = tool.Name
		event["tool_use_id"] = tool.ID
		toolCfg := cfg
		// validCalls has pointed Call at a kept call, or set it to nil.
		if tool.Call != nil {
			c := t.Calls[*tool.Call]
			event["model"] = c.Model
			toolCfg = withProvider(cfg, c.Provider)
		}
		if tool.Input != "" {
			event["tool_input"] = pipeline.ToolInput(tool.Input)
		}
		if tool.Output != "" {
			event["tool_response"] = tool.Output
		}
		if tool.Error {
			event["error"] = "Pi tool error"
		}
		if skillPattern.MatchString(tool.Skill) {
			event["skill_name"] = tool.Skill
			event["skill_source"] = "model"
		}
		pipeline.EnrichToolEvent(event)
		parent := rootID
		if tool.ParentID != "" && valid[tool.ParentID] && !onParentCycle(tool.ID, parentOf) {
			parent = toolSpanID(tool.ParentID)
		}
		spans = append(spans, otlp.NewToolSpan(traceID, toolSpanID(tool.ID), parent, tool.Start, tool.End, event, tool.Error, toolCfg))
		spans = append(spans, subagentSpans(traceID, toolSpanID(tool.ID), tool, cfg, base)...)
		if span, ok := toolUsageSpan(traceID, toolSpanID(tool.ID), tool, cfg, base); ok {
			spans = append(spans, span)
		}
	}

	return otlp.NewTracesRequest(cfg, spans...), nil
}

// toolUsageSpan builds a chat span under a tool's span for the model usage
// that the tool reported for its own work. Dash0's coding-agent token metrics
// read only chat spans, so the tokens go there and not on the tool span. The
// span takes the times of its tool. Pi gives no provider or model for this
// usage, so the span has neither and no cost can be calculated for it.
//
// A subagent tool reports the total of its children as its usage. The
// children's chat spans already carry those tokens, so a tool with children
// gets no usage span. A count that is not valid costs only this span.
func toolUsageSpan(traceID, toolSpanID string, tool Tool, cfg otlp.Config, base func() map[string]any) (otlp.Span, bool) {
	if tool.Usage == nil || len(tool.Subagents) > 0 || !validUsage(*tool.Usage) {
		return otlp.Span{}, false
	}
	event := base()
	if !usageAttributes(event, *tool.Usage) {
		return otlp.Span{}, false
	}
	event["dash0.pi.usage.source"] = "tool"
	id := otlp.SpanIDFromAgentID("tool-usage:" + traceID + ":" + tool.ID)
	return otlp.NewLLMSpan(traceID, id, toolSpanID, tool.Start, tool.End, event, false, cfg), true
}
