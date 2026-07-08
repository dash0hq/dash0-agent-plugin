// SPDX-FileCopyrightText: Copyright 2026 Dash0 Inc.
// SPDX-License-Identifier: Apache-2.0

package otlp

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

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
		"hook_event_name":        "PostToolUse",
		"session_id":             "sess-123",
		"cwd":                    "/tmp/project",
		"tool_name":              "Bash",
		"tool_use_id":            "tu-456",
		"tool_input":             map[string]any{"command": "ls"},
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

	// Log record is correlated with the session trace.
	assert.Equal(t, TraceIDFromSessionID("sess-123"), lr.TraceID)
	assert.Equal(t, SpanIDFromSessionID("sess-123"), lr.SpanID)

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

func TestSendLogNoSessionIDOmitsTraceCorrelation(t *testing.T) {
	var received ExportLogsRequest

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		json.Unmarshal(body, &received)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	event := map[string]any{
		"hook_event_name": "Notification",
		"message":         "hello",
	}
	cfg := Config{OTLPUrl: srv.URL}

	require.NoError(t, SendLog(event, cfg))

	lr := received.ResourceLogs[0].ScopeLogs[0].LogRecords[0]
	assert.Empty(t, lr.TraceID)
	assert.Empty(t, lr.SpanID)
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

	assertAttr(t, received.ResourceLogs[0].Resource.Attributes, "service.name", "my-agent")
	recordAttrs := received.ResourceLogs[0].ScopeLogs[0].LogRecords[0].Attributes
	assertAttr(t, recordAttrs, "gen_ai.agent.name", "my-agent")
}

func TestSendLogHarnessName(t *testing.T) {
	var received ExportLogsRequest

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		json.Unmarshal(body, &received)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	cfg := Config{OTLPUrl: srv.URL, HarnessName: "claude-code"}
	require.NoError(t, SendLog(map[string]any{"event": "test"}, cfg))

	attrs := received.ResourceLogs[0].ScopeLogs[0].LogRecords[0].Attributes
	assertAttr(t, attrs, "gen_ai.harness.name", "claude-code")
}

func TestSendLogNoHarnessNameWhenUnset(t *testing.T) {
	var received ExportLogsRequest

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		json.Unmarshal(body, &received)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	cfg := Config{OTLPUrl: srv.URL}
	require.NoError(t, SendLog(map[string]any{"event": "test"}, cfg))

	assertNoAttr(t, received.ResourceLogs[0].Resource.Attributes, "gen_ai.harness.name")
	assertNoAttr(t, received.ResourceLogs[0].ScopeLogs[0].LogRecords[0].Attributes, "gen_ai.harness.name")
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

func TestIntValAttribute(t *testing.T) {
	var received ExportLogsRequest

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		json.Unmarshal(body, &received)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	event := map[string]any{
		"hook_event_name":            "Stop",
		"gen_ai.usage.input_tokens":  int64(100),
		"gen_ai.usage.output_tokens": int64(50),
	}
	cfg := Config{OTLPUrl: srv.URL}

	require.NoError(t, SendLog(event, cfg))

	lr := received.ResourceLogs[0].ScopeLogs[0].LogRecords[0]
	assertIntAttr(t, lr.Attributes, "gen_ai.usage.input_tokens", 100)
	assertIntAttr(t, lr.Attributes, "gen_ai.usage.output_tokens", 50)
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

func TestSendLogOmitIO(t *testing.T) {
	var received ExportLogsRequest

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		json.Unmarshal(body, &received)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	event := map[string]any{
		"hook_event_name":        "PostToolUse",
		"session_id":             "sess-123",
		"tool_name":              "Bash",
		"tool_input":             map[string]any{"command": "ls"},
		"tool_response":          "file1.go\nfile2.go",
		"last_assistant_message": "Here are the files.",
		"prompt":                 "list files",
	}
	cfg := Config{OTLPUrl: srv.URL, OmitIO: true}

	require.NoError(t, SendLog(event, cfg))

	lr := received.ResourceLogs[0].ScopeLogs[0].LogRecords[0]

	// Non-content attributes are still present.
	assertAttr(t, lr.Attributes, "gen_ai.conversation.id", "sess-123")
	assertAttr(t, lr.Attributes, "gen_ai.tool.name", "Bash")

	// Content attributes are present but redacted.
	// Tool I/O uses plain redaction.
	assertAttr(t, lr.Attributes, "gen_ai.tool.call.arguments", "<REDACTED>")
	assertAttr(t, lr.Attributes, "gen_ai.tool.call.result", "<REDACTED>")
	// Message attributes preserve JSON structure for UI parsing.
	assertAttrContains(t, lr.Attributes, "gen_ai.output.messages", `"role":"assistant"`)
	assertAttrContains(t, lr.Attributes, "gen_ai.output.messages", `REDACTED`)
	assertAttrContains(t, lr.Attributes, "gen_ai.input.messages", `"role":"user"`)
	assertAttrContains(t, lr.Attributes, "gen_ai.input.messages", `REDACTED`)
}

func TestTruncateContent(t *testing.T) {
	t.Run("short content is not truncated", func(t *testing.T) {
		result := truncateContent("hello world")
		assert.Equal(t, "hello world", result)
	})

	t.Run("content at exactly max size is not truncated", func(t *testing.T) {
		exact := strings.Repeat("x", MaxContentBytes)
		result := truncateContent(exact)
		assert.Equal(t, exact, result)
	})

	t.Run("content over max size is truncated with marker", func(t *testing.T) {
		large := strings.Repeat("a", MaxContentBytes+5000)
		result := truncateContent(large)
		assert.True(t, len(result) < len(large), "result should be smaller than input")
		assert.Contains(t, result, "... [truncated,")
		assert.Contains(t, result, "total]")
	})

	t.Run("truncation preserves the beginning of the content", func(t *testing.T) {
		large := "IMPORTANT_PREFIX_" + strings.Repeat("x", MaxContentBytes+1000)
		result := truncateContent(large)
		assert.True(t, strings.HasPrefix(result, "IMPORTANT_PREFIX_"))
	})

	t.Run("empty string is unchanged", func(t *testing.T) {
		assert.Equal(t, "", truncateContent(""))
	})
}

func TestToolIOTruncatedInSpan(t *testing.T) {
	largeOutput := strings.Repeat("Z", MaxContentBytes+5000)

	event := map[string]any{
		"hook_event_name": "PostToolUse",
		"session_id":      "sess-trunc",
		"tool_name":       "Bash",
		"tool_input":      largeOutput,
		"tool_response":   largeOutput,
	}

	cfg := Config{OmitIO: false}
	span := NewToolSpan("aabbccdd"+"eeff0011"+"22334455"+"66778899", "span1234span1234", "parentidparentid",
		time.Now().Add(-100*time.Millisecond), time.Now(), event, false, cfg)

	for _, a := range span.Attributes {
		if a.Key == "gen_ai.tool.call.arguments" || a.Key == "gen_ai.tool.call.result" {
			assert.LessOrEqual(t, len(*a.Value.StringValue), MaxContentBytes+100,
				"attribute %s should be truncated", a.Key)
			assert.Contains(t, *a.Value.StringValue, "... [truncated,")
		}
	}
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

func assertAttrContains(t *testing.T, attrs []Attribute, key, substr string) {
	t.Helper()
	for _, a := range attrs {
		if a.Key == key {
			require.NotNil(t, a.Value.StringValue, "attribute %s: stringValue is nil", key)
			assert.Contains(t, *a.Value.StringValue, substr, "attribute %s should contain %q", key, substr)
			return
		}
	}
	t.Errorf("attribute %s not found", key)
}

func assertIntAttr(t *testing.T, attrs []Attribute, key string, want int64) {
	t.Helper()
	for _, a := range attrs {
		if a.Key == key {
			require.NotNil(t, a.Value.IntValue, "attribute %s: intValue is nil", key)
			assert.Equal(t, strconv.FormatInt(want, 10), *a.Value.IntValue, "attribute %s", key)
			return
		}
	}
	t.Errorf("attribute %s not found", key)
}

func TestSendLogOmitUserInfoRedactsCwd(t *testing.T) {
	var received ExportLogsRequest

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		json.Unmarshal(body, &received)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	home, _ := os.UserHomeDir()

	event := map[string]any{
		"hook_event_name": "PostToolUse",
		"session_id":      "sess-123",
		"cwd":             home + "/source/my-project",
		"tool_name":       "Bash",
	}
	cfg := Config{OTLPUrl: srv.URL, OmitUserInfo: true}

	require.NoError(t, SendLog(event, cfg))

	lr := received.ResourceLogs[0].ScopeLogs[0].LogRecords[0]
	assertAttr(t, lr.Attributes, "process.working_directory", "~/source/my-project")
	assertAttr(t, lr.Attributes, "gen_ai.tool.name", "Bash")
}

func TestSendLogOmitUserInfoCwdOutsideHome(t *testing.T) {
	var received ExportLogsRequest

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		json.Unmarshal(body, &received)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	event := map[string]any{
		"hook_event_name": "PostToolUse",
		"session_id":      "sess-123",
		"cwd":             "/opt/ci/workspace",
		"tool_name":       "Bash",
	}
	cfg := Config{OTLPUrl: srv.URL, OmitUserInfo: true}

	require.NoError(t, SendLog(event, cfg))

	lr := received.ResourceLogs[0].ScopeLogs[0].LogRecords[0]
	assertAttr(t, lr.Attributes, "process.working_directory", "/opt/ci/workspace")
}

func TestSendLogMapsUserEmailAttribute(t *testing.T) {
	var received ExportLogsRequest

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		json.Unmarshal(body, &received)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	event := map[string]any{
		"hook_event_name": "SessionStart",
		"session_id":      "sess-456",
		"user_email":      "alice@example.com",
	}
	cfg := Config{OTLPUrl: srv.URL}

	require.NoError(t, SendLog(event, cfg))

	lr := received.ResourceLogs[0].ScopeLogs[0].LogRecords[0]
	assertAttr(t, lr.Attributes, "user.email", "alice@example.com")
	for _, a := range lr.Attributes {
		assert.NotEqual(t, "user_email", a.Key, "raw user_email key should not appear")
	}
}

func TestRedactHomeDir(t *testing.T) {
	home, _ := os.UserHomeDir()

	t.Run("replaces home prefix with tilde", func(t *testing.T) {
		result := redactHomeDir(home + "/projects/myapp")
		assert.Equal(t, "~/projects/myapp", result)
	})

	t.Run("exact home dir becomes tilde", func(t *testing.T) {
		result := redactHomeDir(home)
		assert.Equal(t, "~", result)
	})

	t.Run("path outside home is unchanged", func(t *testing.T) {
		result := redactHomeDir("/opt/ci/workspace")
		assert.Equal(t, "/opt/ci/workspace", result)
	})

	t.Run("partial prefix match is not redacted", func(t *testing.T) {
		result := redactHomeDir(home + "-extra/projects")
		assert.Equal(t, home+"-extra/projects", result)
	})
}

func TestHashIdentity(t *testing.T) {
	t.Run("produces stable 16-char hex output", func(t *testing.T) {
		result := hashIdentity("Guy Moses")
		assert.Len(t, result, 16)
		assert.Equal(t, result, hashIdentity("Guy Moses"))
	})

	t.Run("different inputs produce different hashes", func(t *testing.T) {
		assert.NotEqual(t, hashIdentity("Alice"), hashIdentity("Bob"))
	})

	t.Run("empty string still produces a hash", func(t *testing.T) {
		result := hashIdentity("")
		assert.Len(t, result, 16)
	})
}
