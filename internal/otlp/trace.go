package otlp

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"time"
)

// OTLP JSON wire format types for traces.

type ExportTracesRequest struct {
	ResourceSpans []ResourceSpans `json:"resourceSpans"`
}

type ResourceSpans struct {
	Resource   Resource     `json:"resource"`
	ScopeSpans []ScopeSpans `json:"scopeSpans"`
}

type ScopeSpans struct {
	Scope Scope  `json:"scope"`
	Spans []Span `json:"spans"`
}

type Span struct {
	TraceID           string      `json:"traceId"`
	SpanID            string      `json:"spanId"`
	ParentSpanID      string      `json:"parentSpanId"`
	Name              string      `json:"name"`
	Kind              int         `json:"kind"`
	StartTimeUnixNano string      `json:"startTimeUnixNano"`
	EndTimeUnixNano   string      `json:"endTimeUnixNano"`
	Attributes        []Attribute `json:"attributes"`
	Events            []SpanEvent `json:"events"`
	Links             []SpanLink  `json:"links"`
	Flags             int         `json:"flags"`
	TraceState        string      `json:"traceState"`
	Status            SpanStatus  `json:"status"`
}

type SpanEvent struct{}
type SpanLink struct{}

type SpanStatus struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// Span kind constants.
const (
	SpanKindInternal = 1
	SpanKindClient   = 3
)

// Span status code constants.
const (
	StatusCodeUnset = 0
	StatusCodeError = 2
)

// TraceIDFromSessionID derives a deterministic 16-byte trace ID from a session
// (conversation) ID by hashing it with SHA-256 and taking the first 16 bytes.
// This allows other spans in the same conversation to join the same trace.
func TraceIDFromSessionID(sessionID string) string {
	h := sha256.Sum256([]byte(sessionID))
	return hex.EncodeToString(h[:16])
}

// SpanIDFromSessionID derives a deterministic 8-byte span ID from a session
// (conversation) ID. It uses the latter half of the SHA-256 hash (bytes 16-24)
// to avoid colliding with the trace ID derived from the same input.
func SpanIDFromSessionID(sessionID string) string {
	h := sha256.Sum256([]byte(sessionID))
	return hex.EncodeToString(h[16:24])
}

// SpanIDFromAgentID derives a deterministic 8-byte span ID from a sub-agent ID.
// This allows tool calls and LLM invocations by the sub-agent to reference the
// Agent tool call span as their parent.
func SpanIDFromAgentID(agentID string) string {
	h := sha256.Sum256([]byte(agentID))
	return hex.EncodeToString(h[:8])
}

// GenerateTraceID returns a random 16-byte trace ID as a 32-char hex string.
func GenerateTraceID() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("generating trace ID: %w", err)
	}
	return hex.EncodeToString(b), nil
}

// GenerateSpanID returns a random 8-byte span ID as a 16-char hex string.
func GenerateSpanID() (string, error) {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("generating span ID: %w", err)
	}
	return hex.EncodeToString(b), nil
}

// SendTrace sends a single span as an OTLP trace to the configured endpoint.
// Returns nil without sending if OTLPUrl is empty.
func SendTrace(span Span, event map[string]any, cfg Config) error {
	if cfg.OTLPUrl == "" {
		return nil
	}

	serviceName := "claude-code"
	if cfg.AgentName != "" {
		serviceName = cfg.AgentName
	}
	resourceAttrs := []Attribute{
		{Key: "service.name", Value: StringVal(serviceName)},
		{Key: "gen_ai.provider.name", Value: StringVal("anthropic")},
	}
	if cfg.AgentName != "" {
		resourceAttrs = append(resourceAttrs, Attribute{Key: "gen_ai.agent.name", Value: StringVal(cfg.AgentName)})
	}
	resourceAttrs = append(resourceAttrs, vcsResourceAttributes(cfg)...)

	req := ExportTracesRequest{
		ResourceSpans: []ResourceSpans{{
			Resource: Resource{
				Attributes: resourceAttrs,
			},
			ScopeSpans: []ScopeSpans{{
				Scope: Scope{
					Name:    "dash0-agent-plugin",
					Version: "0.1.0",
				},
				Spans: []Span{span},
			}},
		}},
	}

	payload, err := json.Marshal(req)
	if err != nil {
		return fmt.Errorf("marshalling OTLP traces request: %w", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, cfg.OTLPUrl+"/v1/traces", bytes.NewReader(payload))
	if err != nil {
		return fmt.Errorf("creating HTTP request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	if cfg.AuthToken != "" {
		httpReq.Header.Set("Authorization", "Bearer "+cfg.AuthToken)
	}
	if cfg.Dataset != "" {
		httpReq.Header.Set("Dash0-Dataset", cfg.Dataset)
	}

	resp, err := http.DefaultClient.Do(httpReq)
	if err != nil {
		return fmt.Errorf("sending OTLP traces request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 300 {
		return fmt.Errorf("OTLP traces endpoint returned %s", resp.Status)
	}

	return nil
}

// NewToolSpan creates a child span for a PostToolUse or PostToolUseFailure event.
// startTime is the time the tool began executing (from the PreToolUse event),
// endTime is the time the tool finished (from the PostToolUse event).
func NewToolSpan(traceID, spanID, parentSpanID string, startTime, endTime time.Time, event map[string]any, failed bool, cfg Config) Span {
	toolName, _ := event["tool_name"].(string)
	attrs := eventAttributes(event, cfg)

	status := SpanStatus{Code: StatusCodeUnset, Message: ""}
	if failed {
		errMsg, _ := event["error"].(string)
		status = SpanStatus{Code: StatusCodeError, Message: errMsg}
	}

	return Span{
		TraceID:           traceID,
		SpanID:            spanID,
		ParentSpanID:      parentSpanID,
		Name:              "execute_tool " + toolName,
		Kind:              SpanKindInternal,
		StartTimeUnixNano: strconv.FormatInt(startTime.UnixNano(), 10),
		EndTimeUnixNano:   strconv.FormatInt(endTime.UnixNano(), 10),
		Attributes:        attrs,
		Events:            []SpanEvent{},
		Links:             []SpanLink{},
		Flags:             0,
		TraceState:        "",
		Status:            status,
	}
}

// NewLLMSpan creates a child span for an LLM invocation, spanning from
// UserPromptSubmit to Stop (or StopFailure).
func NewLLMSpan(traceID, spanID, parentSpanID string, startTime, endTime time.Time, event map[string]any, failed bool, cfg Config) Span {
	model, _ := event["model"].(string)
	attrs := eventAttributes(event, cfg)

	status := SpanStatus{Code: StatusCodeUnset, Message: ""}
	if failed {
		errMsg, _ := event["error"].(string)
		status = SpanStatus{Code: StatusCodeError, Message: errMsg}
	}

	return Span{
		TraceID:           traceID,
		SpanID:            spanID,
		ParentSpanID:      parentSpanID,
		Name:              "chat " + model,
		Kind:              SpanKindClient,
		StartTimeUnixNano: strconv.FormatInt(startTime.UnixNano(), 10),
		EndTimeUnixNano:   strconv.FormatInt(endTime.UnixNano(), 10),
		Attributes:        attrs,
		Events:            []SpanEvent{},
		Links:             []SpanLink{},
		Flags:             0,
		TraceState:        "",
		Status:            status,
	}
}

// NewSessionSpan creates a root span for a session start event.
func NewSessionSpan(traceID, spanID string, ts time.Time, event map[string]any, cfg Config) Span {
	attrs := eventAttributes(event, cfg)
	tsNano := strconv.FormatInt(ts.UnixNano(), 10)

	return Span{
		TraceID:           traceID,
		SpanID:            spanID,
		ParentSpanID:      "",
		Name:              "session_start",
		Kind:              SpanKindInternal,
		StartTimeUnixNano: tsNano,
		EndTimeUnixNano:   tsNano,
		Attributes:        attrs,
		Events:            []SpanEvent{},
		Links:             []SpanLink{},
		Flags:             0,
		TraceState:        "",
		Status:            SpanStatus{Code: 0, Message: ""},
	}
}
