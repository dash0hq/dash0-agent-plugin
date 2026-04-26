package otlp

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestTraceIDFromSessionID(t *testing.T) {
	id := TraceIDFromSessionID("sess-123")
	assert.Len(t, id, 32, "trace ID should be 32 hex characters")

	// Deterministic: same session ID always produces the same trace ID.
	assert.Equal(t, id, TraceIDFromSessionID("sess-123"))

	// Different session IDs produce different trace IDs.
	assert.NotEqual(t, id, TraceIDFromSessionID("sess-456"))
}

func TestGenerateTraceID(t *testing.T) {
	id, err := GenerateTraceID()
	require.NoError(t, err)
	assert.Len(t, id, 32, "trace ID should be 32 hex characters")

	id2, err := GenerateTraceID()
	require.NoError(t, err)
	assert.NotEqual(t, id, id2, "trace IDs should be unique")
}

func TestSpanIDFromSessionID(t *testing.T) {
	id := SpanIDFromSessionID("sess-123")
	assert.Len(t, id, 16, "span ID should be 16 hex characters")

	// Deterministic: same session ID always produces the same span ID.
	assert.Equal(t, id, SpanIDFromSessionID("sess-123"))

	// Different session IDs produce different span IDs.
	assert.NotEqual(t, id, SpanIDFromSessionID("sess-456"))

	// Span ID does not overlap with trace ID from the same session.
	traceID := TraceIDFromSessionID("sess-123")
	assert.NotEqual(t, traceID[:16], id, "span ID should not be a prefix of the trace ID")
}

func TestSpanIDFromAgentID(t *testing.T) {
	id := SpanIDFromAgentID("agent-abc")
	assert.Len(t, id, 16, "span ID should be 16 hex characters")

	// Deterministic: same agent ID always produces the same span ID.
	assert.Equal(t, id, SpanIDFromAgentID("agent-abc"))

	// Different agent IDs produce different span IDs.
	assert.NotEqual(t, id, SpanIDFromAgentID("agent-xyz"))
}

func TestGenerateSpanID(t *testing.T) {
	id, err := GenerateSpanID()
	require.NoError(t, err)
	assert.Len(t, id, 16, "span ID should be 16 hex characters")

	id2, err := GenerateSpanID()
	require.NoError(t, err)
	assert.NotEqual(t, id, id2, "span IDs should be unique")
}

func TestNewSessionSpan(t *testing.T) {
	ts := time.Date(2025, 6, 15, 12, 0, 0, 0, time.UTC)
	event := map[string]any{
		"hook_event_name": "SessionStart",
		"session_id":      "sess-123",
		"cwd":             "/tmp/project",
	}

	span := NewSessionSpan("abc123traceabc123traceabc123tr", "span1234span1234", ts, event, Config{})

	assert.Equal(t, "abc123traceabc123traceabc123tr", span.TraceID)
	assert.Equal(t, "span1234span1234", span.SpanID)
	assert.Empty(t, span.ParentSpanID)
	assert.Equal(t, "session_start", span.Name)
	assert.Equal(t, SpanKindInternal, span.Kind)
	assert.Equal(t, "1749988800000000000", span.StartTimeUnixNano)
	assert.Equal(t, "1749988800000000000", span.EndTimeUnixNano)
	assert.Equal(t, SpanStatus{Code: 0, Message: ""}, span.Status)
	assert.NotNil(t, span.Events)
	assert.NotNil(t, span.Links)
	assert.Equal(t, "", span.TraceState)

	// Verify event attributes are included (skipping hook_event_name and timestamp).
	assertAttr(t, span.Attributes, "gen_ai.conversation.id", "sess-123")
	assertAttr(t, span.Attributes, "process.working_directory", "/tmp/project")
	assertNoAttr(t, span.Attributes, "hook_event_name")
}

func TestNewToolSpan(t *testing.T) {
	startTime := time.Date(2025, 6, 15, 12, 0, 30, 0, time.UTC)
	endTime := time.Date(2025, 6, 15, 12, 1, 0, 0, time.UTC)
	event := map[string]any{
		"hook_event_name": "PostToolUse",
		"session_id":      "sess-123",
		"tool_name":       "Bash",
		"tool_input":      "ls -la",
	}

	span := NewToolSpan("aabb"+"ccdd"+"eeff"+"0011"+"2233"+"4455"+"6677"+"8899", "span1234span1234", "parentidparentid", startTime, endTime, event, false, Config{})

	assert.Equal(t, "parentidparentid", span.ParentSpanID)
	assert.Equal(t, "execute_tool Bash", span.Name)
	assert.Equal(t, SpanKindInternal, span.Kind)
	assert.Equal(t, "1749988830000000000", span.StartTimeUnixNano)
	assert.Equal(t, "1749988860000000000", span.EndTimeUnixNano)
	assert.Equal(t, SpanStatus{Code: StatusCodeUnset, Message: ""}, span.Status)

	assertAttr(t, span.Attributes, "gen_ai.tool.name", "Bash")
	assertAttr(t, span.Attributes, "gen_ai.tool.call.arguments", "ls -la")
}

func TestNewToolSpanFailure(t *testing.T) {
	startTime := time.Date(2025, 6, 15, 12, 0, 30, 0, time.UTC)
	endTime := time.Date(2025, 6, 15, 12, 1, 0, 0, time.UTC)
	event := map[string]any{
		"hook_event_name": "PostToolUseFailure",
		"session_id":      "sess-123",
		"tool_name":       "Bash",
		"error":           "command not found",
	}

	span := NewToolSpan("aabb"+"ccdd"+"eeff"+"0011"+"2233"+"4455"+"6677"+"8899", "span1234span1234", "parentidparentid", startTime, endTime, event, true, Config{})

	assert.Equal(t, "execute_tool Bash", span.Name)
	assert.Equal(t, StatusCodeError, span.Status.Code)
	assert.Equal(t, "command not found", span.Status.Message)
}

func TestNewLLMSpan(t *testing.T) {
	startTime := time.Date(2025, 6, 15, 12, 0, 0, 0, time.UTC)
	endTime := time.Date(2025, 6, 15, 12, 0, 45, 0, time.UTC)
	event := map[string]any{
		"hook_event_name": "Stop",
		"session_id":      "sess-123",
		"model":           "claude-sonnet-4-20250514",
	}

	span := NewLLMSpan("abc123traceabc123traceabc123tr", "span1234span1234", "parentidparentid", startTime, endTime, event, false, Config{})

	assert.Equal(t, "abc123traceabc123traceabc123tr", span.TraceID)
	assert.Equal(t, "span1234span1234", span.SpanID)
	assert.Equal(t, "parentidparentid", span.ParentSpanID)
	assert.Equal(t, "chat claude-sonnet-4-20250514", span.Name)
	assert.Equal(t, SpanKindClient, span.Kind)
	assert.Equal(t, "1749988800000000000", span.StartTimeUnixNano)
	assert.Equal(t, "1749988845000000000", span.EndTimeUnixNano)
	assert.Equal(t, SpanStatus{Code: StatusCodeUnset, Message: ""}, span.Status)

	assertAttr(t, span.Attributes, "gen_ai.request.model", "claude-sonnet-4-20250514")
	assertAttr(t, span.Attributes, "gen_ai.conversation.id", "sess-123")
}

func TestNewLLMSpanFailure(t *testing.T) {
	startTime := time.Date(2025, 6, 15, 12, 0, 0, 0, time.UTC)
	endTime := time.Date(2025, 6, 15, 12, 0, 45, 0, time.UTC)
	event := map[string]any{
		"hook_event_name": "StopFailure",
		"session_id":      "sess-123",
		"model":           "claude-sonnet-4-20250514",
		"error":           "context window exceeded",
	}

	span := NewLLMSpan("abc123traceabc123traceabc123tr", "span1234span1234", "parentidparentid", startTime, endTime, event, true, Config{})

	assert.Equal(t, "chat claude-sonnet-4-20250514", span.Name)
	assert.Equal(t, SpanKindClient, span.Kind)
	assert.Equal(t, StatusCodeError, span.Status.Code)
	assert.Equal(t, "context window exceeded", span.Status.Message)
}

func TestSendTrace(t *testing.T) {
	var received ExportTracesRequest
	var reqHeaders http.Header
	var reqPath string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reqPath = r.URL.Path
		reqHeaders = r.Header
		body, _ := io.ReadAll(r.Body)
		require.NoError(t, json.Unmarshal(body, &received))
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	event := map[string]any{
		"hook_event_name": "SessionStart",
		"session_id":      "sess-123",
	}
	cfg := Config{
		OTLPUrl:   srv.URL,
		AuthToken: "test-token",
		Dataset:   "test-dataset",
	}
	span := Span{
		TraceID:           "aaaabbbbccccddddaaaabbbbccccdddd",
		SpanID:            "1111222233334444",
		Name:              "session_start",
		Kind:              SpanKindInternal,
		StartTimeUnixNano: "1749988800000000000",
		EndTimeUnixNano:   "0",
		Status:            SpanStatus{Code: 0},
	}

	require.NoError(t, SendTrace(span, event, cfg))

	assert.Equal(t, "/v1/traces", reqPath)
	assert.Equal(t, "application/json", reqHeaders.Get("Content-Type"))
	assert.Equal(t, "Bearer test-token", reqHeaders.Get("Authorization"))
	assert.Equal(t, "test-dataset", reqHeaders.Get("Dash0-Dataset"))

	require.Len(t, received.ResourceSpans, 1)
	rs := received.ResourceSpans[0]

	assertAttr(t, rs.Resource.Attributes, "service.name", "claude-code")
	assertAttr(t, rs.Resource.Attributes, "gen_ai.provider.name", "anthropic")

	require.Len(t, rs.ScopeSpans, 1)
	ss := rs.ScopeSpans[0]
	assert.Equal(t, "dash0-agent-plugin", ss.Scope.Name)
	assert.Equal(t, "0.1.0", ss.Scope.Version)

	require.Len(t, ss.Spans, 1)
	s := ss.Spans[0]
	assert.Equal(t, "aaaabbbbccccddddaaaabbbbccccdddd", s.TraceID)
	assert.Equal(t, "1111222233334444", s.SpanID)
	assert.Equal(t, "session_start", s.Name)
	assert.Equal(t, SpanKindInternal, s.Kind)
	assert.Equal(t, "1749988800000000000", s.StartTimeUnixNano)
	assert.Equal(t, "0", s.EndTimeUnixNano)
}

func TestNewToolSpanOmitIO(t *testing.T) {
	startTime := time.Date(2025, 6, 15, 12, 0, 30, 0, time.UTC)
	endTime := time.Date(2025, 6, 15, 12, 1, 0, 0, time.UTC)
	event := map[string]any{
		"hook_event_name": "PostToolUse",
		"session_id":      "sess-123",
		"tool_name":       "Bash",
		"tool_input":      "ls -la",
		"tool_response":   "file1.go",
	}

	span := NewToolSpan("aabb"+"ccdd"+"eeff"+"0011"+"2233"+"4455"+"6677"+"8899", "span1234span1234", "parentidparentid", startTime, endTime, event, false, Config{OmitIO: true})

	// Tool name is still present.
	assertAttr(t, span.Attributes, "gen_ai.tool.name", "Bash")
	// Content attributes are omitted.
	assertNoAttr(t, span.Attributes, "gen_ai.tool.call.arguments")
	assertNoAttr(t, span.Attributes, "gen_ai.tool.call.result")
}

func TestNewLLMSpanOmitIO(t *testing.T) {
	startTime := time.Date(2025, 6, 15, 12, 0, 0, 0, time.UTC)
	endTime := time.Date(2025, 6, 15, 12, 0, 45, 0, time.UTC)
	event := map[string]any{
		"hook_event_name":        "Stop",
		"session_id":             "sess-123",
		"model":                  "claude-sonnet-4-20250514",
		"prompt":                 "hello",
		"last_assistant_message": "hi there",
	}

	span := NewLLMSpan("abc123traceabc123traceabc123tr", "span1234span1234", "parentidparentid", startTime, endTime, event, false, Config{OmitIO: true})

	// Model is still present.
	assertAttr(t, span.Attributes, "gen_ai.request.model", "claude-sonnet-4-20250514")
	// Content attributes are omitted.
	assertNoAttr(t, span.Attributes, "gen_ai.input.messages")
	assertNoAttr(t, span.Attributes, "gen_ai.output.messages")
}

func TestSendTraceSkipsWhenNotConfigured(t *testing.T) {
	span := Span{Name: "test"}
	assert.NoError(t, SendTrace(span, map[string]any{}, Config{}))
}

func TestSendTraceReturnsErrorOnHTTPFailure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	span := Span{Name: "test"}
	cfg := Config{OTLPUrl: srv.URL}
	assert.Error(t, SendTrace(span, map[string]any{}, cfg))
}

func TestSendTraceRetries5xx(t *testing.T) {
	attempts := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		if attempts == 1 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	span := Span{Name: "test"}
	cfg := Config{OTLPUrl: srv.URL}
	assert.NoError(t, SendTrace(span, map[string]any{}, cfg))
	assert.Equal(t, 2, attempts, "should retry once on 5xx")
}

func TestSendTraceDoesNotRetry4xx(t *testing.T) {
	attempts := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()

	span := Span{Name: "test"}
	cfg := Config{OTLPUrl: srv.URL}
	assert.Error(t, SendTrace(span, map[string]any{}, cfg))
	assert.Equal(t, 1, attempts, "should not retry on 4xx")
}
