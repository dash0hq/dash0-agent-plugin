package otlp

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSendLog(t *testing.T) {
	var received ExportLogsRequest
	var reqHeaders http.Header

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/v1/logs", r.URL.Path)
		assert.Equal(t, "application/json", r.Header.Get("Content-Type"))
		reqHeaders = r.Header
		body, _ := io.ReadAll(r.Body)
		require.NoError(t, json.Unmarshal(body, &received))
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	event := map[string]any{
		"hook_event_name": "PostToolUse",
		"session_id":      "sess-123",
		"cwd":             "/tmp/project",
		"tool_name":       "Bash",
		"tool_use_id":     "tu-456",
		"tool_input":      map[string]any{"command": "ls"},
		"tool_response":          "file1.go\nfile2.go",
		"timestamp":              "2025-06-15T12:00:00Z",
		"last_assistant_message": "Here are the files.",
	}
	cfg := Config{
		OTLPUrl:   srv.URL,
		AuthToken: "test-token",
		Dataset:   "test-dataset",
	}

	require.NoError(t, SendLog(event, cfg))

	// Verify headers.
	assert.Equal(t, "Bearer test-token", reqHeaders.Get("Authorization"))
	assert.Equal(t, "test-dataset", reqHeaders.Get("Dash0-Dataset"))

	// Verify OTLP structure.
	require.Len(t, received.ResourceLogs, 1)
	rl := received.ResourceLogs[0]

	assertAttr(t, rl.Resource.Attributes, "service.name", "claude-code")

	require.Len(t, rl.ScopeLogs, 1)
	sl := rl.ScopeLogs[0]
	assert.Equal(t, "dash0-agent-plugin", sl.Scope.Name)

	require.Len(t, sl.LogRecords, 1)
	lr := sl.LogRecords[0]

	assert.Equal(t, "INFO", lr.SeverityText)
	assert.Equal(t, 9, lr.SeverityNumber)
	assert.Equal(t, "1749988800000000000", lr.TimeUnixNano)

	// Log body is the hook event name.
	require.NotNil(t, lr.Body.StringValue)
	assert.Equal(t, "PostToolUse", *lr.Body.StringValue)

	// Skipped fields should not appear as attributes.
	assertNoAttr(t, lr.Attributes, "hook_event_name")
	assertNoAttr(t, lr.Attributes, "timestamp")
	assertAttr(t, lr.Attributes, "gen_ai.conversation.id", "sess-123")
	assertAttr(t, lr.Attributes, "process.working_directory", "/tmp/project")
	assertAttr(t, lr.Attributes, "gen_ai.tool.name", "Bash")
	assertAttr(t, lr.Attributes, "gen_ai.tool.call.id", "tu-456")
	assertAttr(t, lr.Attributes, "gen_ai.tool.call.arguments", `{"command":"ls"}`)
	assertAttr(t, lr.Attributes, "gen_ai.tool.call.result", "file1.go\nfile2.go")

	// Transformed fields.
	assertNoAttr(t, lr.Attributes, "last_assistant_message")
	assertAttr(t, lr.Attributes, "gen_ai.output.messages",
		`[{"parts":[{"content":"Here are the files.","type":"text"}],"role":"assistant"}]`)
}

func TestSendLogWithAgentName(t *testing.T) {
	var received ExportLogsRequest

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		json.Unmarshal(body, &received)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	cfg := Config{
		OTLPUrl:   srv.URL,
		AgentName: "my-agent",
	}
	require.NoError(t, SendLog(map[string]any{"event": "test"}, cfg))

	attrs := received.ResourceLogs[0].Resource.Attributes
	assertAttr(t, attrs, "service.name", "my-agent")
	assertAttr(t, attrs, "gen_ai.agent.name", "my-agent")
}

func TestSendLogSkipsWhenNotConfigured(t *testing.T) {
	assert.NoError(t, SendLog(map[string]any{"event": "test"}, Config{}))
}

func TestSendLogNoAuthHeaders(t *testing.T) {
	var reqHeaders http.Header

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reqHeaders = r.Header
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	cfg := Config{OTLPUrl: srv.URL}
	require.NoError(t, SendLog(map[string]any{"event": "test"}, cfg))

	assert.Empty(t, reqHeaders.Get("Authorization"))
	assert.Empty(t, reqHeaders.Get("Dash0-Dataset"))
}

func TestSendLogReturnsErrorOnHTTPFailure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	cfg := Config{OTLPUrl: srv.URL}
	assert.Error(t, SendLog(map[string]any{"event": "test"}, cfg))
}

func TestSendLogMinimalEvent(t *testing.T) {
	var received ExportLogsRequest

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		json.Unmarshal(body, &received)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	cfg := Config{OTLPUrl: srv.URL}
	require.NoError(t, SendLog(map[string]any{"foo": "bar"}, cfg))

	lr := received.ResourceLogs[0].ScopeLogs[0].LogRecords[0]
	assertAttr(t, lr.Attributes, "foo", "bar")
}

func assertNoAttr(t *testing.T, attrs []Attribute, key string) {
	t.Helper()
	for _, a := range attrs {
		if a.Key == key {
			t.Errorf("attribute %s should not be present", key)
			return
		}
	}
}

func assertAttr(t *testing.T, attrs []Attribute, key, want string) {
	t.Helper()
	for _, a := range attrs {
		if a.Key == key {
			require.NotNil(t, a.Value.StringValue, "attribute %s: stringValue is nil", key)
			assert.Equal(t, want, *a.Value.StringValue, "attribute %s", key)
			return
		}
	}
	t.Errorf("attribute %s not found", key)
}
