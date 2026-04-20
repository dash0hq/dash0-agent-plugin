package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/dash0hq/dash0-agent-plugin/internal/otlp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestIntegrationWritesAndTimestamps(t *testing.T) {
	dataDir := t.TempDir()
	t.Setenv("CLAUDE_PLUGIN_DATA", dataDir)

	before := time.Now().UTC()
	feed(t, `{"hook_event_name":"SessionStart","session_id":"abc123"}`)
	after := time.Now().UTC()

	lines := readLines(t, filepath.Join(dataDir, "events.jsonl"))
	require.Len(t, lines, 1)

	var got map[string]any
	require.NoError(t, json.Unmarshal([]byte(lines[0]), &got))

	assert.Equal(t, "SessionStart", got["hook_event_name"])

	ts, ok := got["timestamp"].(string)
	require.True(t, ok, "timestamp field missing or not a string")

	parsed, err := time.Parse(time.RFC3339Nano, ts)
	require.NoError(t, err, "timestamp is not valid RFC3339Nano")
	assert.WithinRange(t, parsed, before.Truncate(time.Millisecond), after.Add(time.Millisecond))
}

func TestIntegrationCreatesDataDirectory(t *testing.T) {
	dataDir := filepath.Join(t.TempDir(), "nested", "path")
	t.Setenv("CLAUDE_PLUGIN_DATA", dataDir)

	feed(t, `{"event":"test"}`)

	assert.FileExists(t, filepath.Join(dataDir, "events.jsonl"))
}

func TestIntegrationFailsWithoutPluginData(t *testing.T) {
	t.Setenv("CLAUDE_PLUGIN_DATA", "")

	err := runWithStdin(`{"event":"test"}`)
	assert.Error(t, err)
}

func TestIntegrationFailsOnInvalidJSON(t *testing.T) {
	dataDir := t.TempDir()
	t.Setenv("CLAUDE_PLUGIN_DATA", dataDir)

	err := runWithStdin("not json")
	assert.Error(t, err)
}

func TestExtractAgentIDFromResponseString(t *testing.T) {
	resp := `{"agentId":"a13ff1c4e70c41cd1","agentType":"general-purpose","content":[]}`
	assert.Equal(t, "a13ff1c4e70c41cd1", extractAgentIDFromResponse(resp))
}

func TestExtractAgentIDFromResponseMap(t *testing.T) {
	resp := map[string]any{"agentId": "abc123", "agentType": "general-purpose"}
	assert.Equal(t, "abc123", extractAgentIDFromResponse(resp))
}

func TestExtractAgentIDFromResponseMissing(t *testing.T) {
	assert.Equal(t, "", extractAgentIDFromResponse(`{"no_agent":"here"}`))
	assert.Equal(t, "", extractAgentIDFromResponse("not json"))
	assert.Equal(t, "", extractAgentIDFromResponse(nil))
	assert.Equal(t, "", extractAgentIDFromResponse(42))
}

// feed pipes input through run() and fails the test on error.
func feed(t *testing.T, input string) {
	t.Helper()
	require.NoError(t, runWithStdin(input))
}

// runWithStdin calls run() with the given string on stdin.
func runWithStdin(input string) error {
	oldStdin := os.Stdin
	defer func() { os.Stdin = oldStdin }()

	r, w, err := os.Pipe()
	if err != nil {
		return err
	}
	os.Stdin = r
	go func() {
		w.WriteString(input)
		w.Close()
	}()

	return run()
}

// readLines reads a file and returns non-empty lines.
func readLines(t *testing.T, path string) []string {
	t.Helper()
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	var lines []string
	for _, line := range strings.Split(string(data), "\n") {
		if line != "" {
			lines = append(lines, line)
		}
	}
	return lines
}

// collectingServer returns an httptest server that collects OTLP trace spans.
func collectingServer(t *testing.T) (*httptest.Server, *[]otlp.Span, *sync.Mutex) {
	t.Helper()
	var spans []otlp.Span
	var mu sync.Mutex
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/traces" {
			body, _ := io.ReadAll(r.Body)
			var req otlp.ExportTracesRequest
			if err := json.Unmarshal(body, &req); err == nil {
				mu.Lock()
				for _, rs := range req.ResourceSpans {
					for _, ss := range rs.ScopeSpans {
						spans = append(spans, ss.Spans...)
					}
				}
				mu.Unlock()
			}
		}
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)
	return srv, &spans, &mu
}

func findSpan(spans []otlp.Span, namePrefix string) *otlp.Span {
	for i, s := range spans {
		if strings.HasPrefix(s.Name, namePrefix) {
			return &spans[i]
		}
	}
	return nil
}

func TestChatSpanIsRootWithToolChildren(t *testing.T) {
	dataDir := t.TempDir()
	t.Setenv("CLAUDE_PLUGIN_DATA", dataDir)
	srv, spans, _ := collectingServer(t)
	t.Setenv("DASH0_OTLP_URL", srv.URL)

	feed(t, `{"hook_event_name":"SessionStart","session_id":"sess-1","model":"claude-sonnet-4-20250514"}`)
	feed(t, `{"hook_event_name":"UserPromptSubmit","session_id":"sess-1","prompt":"hello"}`)
	feed(t, `{"hook_event_name":"PreToolUse","session_id":"sess-1","tool_name":"Bash","tool_use_id":"tu1"}`)
	feed(t, `{"hook_event_name":"PostToolUse","session_id":"sess-1","tool_name":"Bash","tool_use_id":"tu1","tool_response":"ok"}`)
	feed(t, `{"hook_event_name":"Stop","session_id":"sess-1","model":"claude-sonnet-4-20250514"}`)

	require.Len(t, *spans, 2) // chat + tool (no session span)

	toolSpan := findSpan(*spans, "execute_tool")
	chatSpan := findSpan(*spans, "chat")

	require.NotNil(t, toolSpan)
	require.NotNil(t, chatSpan)

	// Chat span is root (no parent).
	assert.Empty(t, chatSpan.ParentSpanID)
	// Tool span is child of chat span.
	assert.Equal(t, chatSpan.SpanID, toolSpan.ParentSpanID)
	// Both share the same trace ID.
	assert.Equal(t, chatSpan.TraceID, toolSpan.TraceID)
	// Trace ID is random (not derived from session_id).
	assert.NotEqual(t, otlp.TraceIDFromSessionID("sess-1"), chatSpan.TraceID)
}

func TestEachTurnGetsNewTraceID(t *testing.T) {
	dataDir := t.TempDir()
	t.Setenv("CLAUDE_PLUGIN_DATA", dataDir)
	srv, spans, _ := collectingServer(t)
	t.Setenv("DASH0_OTLP_URL", srv.URL)

	feed(t, `{"hook_event_name":"SessionStart","session_id":"sess-multi","model":"claude-sonnet-4-20250514"}`)

	// Turn 1
	feed(t, `{"hook_event_name":"UserPromptSubmit","session_id":"sess-multi","prompt":"first"}`)
	feed(t, `{"hook_event_name":"Stop","session_id":"sess-multi"}`)

	// Turn 2
	feed(t, `{"hook_event_name":"UserPromptSubmit","session_id":"sess-multi","prompt":"second"}`)
	feed(t, `{"hook_event_name":"Stop","session_id":"sess-multi"}`)

	require.Len(t, *spans, 2) // two chat spans

	// Each turn has a different trace ID.
	assert.NotEqual(t, (*spans)[0].TraceID, (*spans)[1].TraceID)
	// Both are roots.
	assert.Empty(t, (*spans)[0].ParentSpanID)
	assert.Empty(t, (*spans)[1].ParentSpanID)
}

func TestSubAgentToolSpansNestUnderAgentSpan(t *testing.T) {
	dataDir := t.TempDir()
	t.Setenv("CLAUDE_PLUGIN_DATA", dataDir)
	srv, spans, _ := collectingServer(t)
	t.Setenv("DASH0_OTLP_URL", srv.URL)

	feed(t, `{"hook_event_name":"SessionStart","session_id":"sess-3","model":"claude-sonnet-4-20250514"}`)
	feed(t, `{"hook_event_name":"UserPromptSubmit","session_id":"sess-3","prompt":"hello"}`)
	// Agent tool call by the main agent — spawns sub-agent "agent-42".
	feed(t, `{"hook_event_name":"PreToolUse","session_id":"sess-3","tool_name":"Agent","tool_use_id":"tu-agent"}`)
	feed(t, `{"hook_event_name":"PostToolUse","session_id":"sess-3","tool_name":"Agent","tool_use_id":"tu-agent","tool_response":"{\"agentId\":\"agent-42\",\"content\":[]}"}`)
	// Tool call inside sub-agent.
	feed(t, `{"hook_event_name":"PreToolUse","session_id":"sess-3","tool_name":"Bash","tool_use_id":"tu-sub","agent_id":"agent-42"}`)
	feed(t, `{"hook_event_name":"PostToolUse","session_id":"sess-3","tool_name":"Bash","tool_use_id":"tu-sub","tool_response":"ok","agent_id":"agent-42"}`)

	require.Len(t, *spans, 2) // Agent tool + sub-agent Bash tool

	agentToolSpan := findSpan(*spans, "execute_tool Agent")
	subToolSpan := findSpan(*spans, "execute_tool Bash")

	require.NotNil(t, agentToolSpan)
	require.NotNil(t, subToolSpan)

	// Sub-agent tool is nested under the Agent tool span.
	expectedParent := otlp.SpanIDFromAgentID("agent-42")
	assert.Equal(t, expectedParent, subToolSpan.ParentSpanID)
	assert.Equal(t, expectedParent, agentToolSpan.SpanID)

	// Agent tool span's parent is the chat span (from trace context).
	ctx, err := otlp.LoadTraceContext(dataDir)
	require.NoError(t, err)
	require.NotNil(t, ctx)
	assert.Equal(t, ctx.SpanID, agentToolSpan.ParentSpanID)
}

func TestSessionStartDoesNotEmitSpan(t *testing.T) {
	dataDir := t.TempDir()
	t.Setenv("CLAUDE_PLUGIN_DATA", dataDir)
	srv, spans, _ := collectingServer(t)
	t.Setenv("DASH0_OTLP_URL", srv.URL)

	feed(t, `{"hook_event_name":"SessionStart","session_id":"sess-no-span","model":"claude-sonnet-4-20250514"}`)

	// No span emitted for SessionStart.
	assert.Empty(t, *spans)

	// But model is saved to trace context.
	ctx, err := otlp.LoadTraceContext(dataDir)
	require.NoError(t, err)
	require.NotNil(t, ctx)
	assert.Equal(t, "claude-sonnet-4-20250514", ctx.Model)
}

func TestNoLogsEmitted(t *testing.T) {
	var logRequests int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/logs" {
			logRequests++
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	dataDir := t.TempDir()
	t.Setenv("CLAUDE_PLUGIN_DATA", dataDir)
	t.Setenv("DASH0_OTLP_URL", srv.URL)

	feed(t, `{"hook_event_name":"SessionStart","session_id":"sess-nolog","model":"claude-sonnet-4-20250514"}`)
	feed(t, `{"hook_event_name":"UserPromptSubmit","session_id":"sess-nolog","prompt":"hi"}`)
	feed(t, `{"hook_event_name":"Stop","session_id":"sess-nolog"}`)

	assert.Equal(t, 0, logRequests, "no log records should be sent")
}

func TestMissingSessionIDUsesRandomTraceID(t *testing.T) {
	dataDir := t.TempDir()
	t.Setenv("CLAUDE_PLUGIN_DATA", dataDir)
	srv, spans, _ := collectingServer(t)
	t.Setenv("DASH0_OTLP_URL", srv.URL)

	// UserPromptSubmit + Stop without session_id.
	feed(t, `{"hook_event_name":"UserPromptSubmit","prompt":"hello"}`)
	feed(t, `{"hook_event_name":"Stop"}`)

	// Chat span should still be emitted.
	require.Len(t, *spans, 1)

	span := (*spans)[0]
	assert.NotEmpty(t, span.TraceID)
	assert.Len(t, span.TraceID, 32)

	// Should have the warning attribute.
	found := false
	for _, a := range span.Attributes {
		if a.Key == "dash0.warning" {
			found = true
			assert.Equal(t, "session_id was missing from hook payload", *a.Value.StringValue)
		}
	}
	assert.True(t, found, "dash0.warning attribute should be present")
}

func TestEnvBool(t *testing.T) {
	for _, tc := range []struct {
		val  string
		want bool
	}{
		{"true", true},
		{"True", true},
		{"TRUE", true},
		{"1", true},
		{"false", false},
		{"0", false},
		{"", false},
		{"yes", false},
	} {
		t.Run(tc.val, func(t *testing.T) {
			t.Setenv("TEST_BOOL", tc.val)
			assert.Equal(t, tc.want, envBool("TEST_BOOL"))
		})
	}
}

func TestOmitIOOmitsContentAttributes(t *testing.T) {
	dataDir := t.TempDir()
	t.Setenv("CLAUDE_PLUGIN_DATA", dataDir)
	srv, spans, _ := collectingServer(t)
	t.Setenv("DASH0_OTLP_URL", srv.URL)
	t.Setenv("DASH0_OMIT_IO", "true")

	feed(t, `{"hook_event_name":"SessionStart","session_id":"sess-omit","model":"claude-sonnet-4-20250514"}`)
	feed(t, `{"hook_event_name":"UserPromptSubmit","session_id":"sess-omit","prompt":"hello"}`)
	feed(t, `{"hook_event_name":"PreToolUse","session_id":"sess-omit","tool_name":"Bash","tool_use_id":"tu-omit"}`)
	feed(t, `{"hook_event_name":"PostToolUse","session_id":"sess-omit","tool_name":"Bash","tool_use_id":"tu-omit","tool_input":"ls","tool_response":"ok"}`)
	feed(t, `{"hook_event_name":"Stop","session_id":"sess-omit","model":"claude-sonnet-4-20250514","last_assistant_message":"done","prompt":"hello"}`)

	toolSpan := findSpan(*spans, "execute_tool")
	chatSpan := findSpan(*spans, "chat")

	require.NotNil(t, toolSpan)
	require.NotNil(t, chatSpan)

	// Tool span should not have input/output content.
	for _, a := range toolSpan.Attributes {
		assert.NotEqual(t, "gen_ai.tool.call.arguments", a.Key)
		assert.NotEqual(t, "gen_ai.tool.call.result", a.Key)
	}

	// Chat span should not have prompt/response content.
	for _, a := range chatSpan.Attributes {
		assert.NotEqual(t, "gen_ai.input.messages", a.Key)
		assert.NotEqual(t, "gen_ai.output.messages", a.Key)
	}
}

func TestUserPromptSubmitStampsChatSpanID(t *testing.T) {
	dataDir := t.TempDir()
	t.Setenv("CLAUDE_PLUGIN_DATA", dataDir)

	feed(t, `{"hook_event_name":"SessionStart","session_id":"sess-stamp","model":"opus"}`)
	feed(t, `{"hook_event_name":"UserPromptSubmit","session_id":"sess-stamp","prompt":"hello"}`)

	lines := readLines(t, filepath.Join(dataDir, "events.jsonl"))
	require.Len(t, lines, 2) // SessionStart + UserPromptSubmit

	var got map[string]any
	require.NoError(t, json.Unmarshal([]byte(lines[1]), &got))

	chatSpanID, ok := got["chat_span_id"].(string)
	require.True(t, ok, "chat_span_id should be stamped on event")
	assert.Len(t, chatSpanID, 16) // 8 bytes = 16 hex chars

	// Trace context should also be saved.
	ctx, err := otlp.LoadTraceContext(dataDir)
	require.NoError(t, err)
	require.NotNil(t, ctx)
	assert.Equal(t, chatSpanID, ctx.SpanID)
	assert.Len(t, ctx.TraceID, 32)
}

func assertIntAttr(t *testing.T, attrs []otlp.Attribute, key string, want int64) {
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

func writeTranscript(t *testing.T, path string, lines []string) {
	t.Helper()
	require.NoError(t, os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o644))
}

func TestTokenUsageOnLLMSpan(t *testing.T) {
	dataDir := t.TempDir()
	t.Setenv("CLAUDE_PLUGIN_DATA", dataDir)
	srv, spans, _ := collectingServer(t)
	t.Setenv("DASH0_OTLP_URL", srv.URL)

	// Create transcript file with usage data.
	transcriptPath := filepath.Join(dataDir, "transcript.jsonl")
	writeTranscript(t, transcriptPath, []string{
		`{"type":"user","message":{"role":"user","content":[{"type":"text","text":"hello"}]}}`,
		`{"type":"assistant","requestId":"req_001","message":{"role":"assistant","content":[{"type":"text","text":"hi"}],"usage":{"input_tokens":100,"output_tokens":50,"cache_creation_input_tokens":200,"cache_read_input_tokens":300}}}`,
	})

	feed(t, `{"hook_event_name":"SessionStart","session_id":"sess-tok","model":"claude-sonnet-4-20250514"}`)
	feed(t, `{"hook_event_name":"UserPromptSubmit","session_id":"sess-tok","prompt":"hello"}`)
	feed(t, fmt.Sprintf(`{"hook_event_name":"Stop","session_id":"sess-tok","model":"claude-sonnet-4-20250514","transcript_path":"%s"}`, transcriptPath))

	chatSpan := findSpan(*spans, "chat")
	require.NotNil(t, chatSpan)

	assertIntAttr(t, chatSpan.Attributes, "gen_ai.usage.input_tokens", 100)
	assertIntAttr(t, chatSpan.Attributes, "gen_ai.usage.output_tokens", 50)
	assertIntAttr(t, chatSpan.Attributes, "gen_ai.usage.cache_creation_input_tokens", 200)
	assertIntAttr(t, chatSpan.Attributes, "gen_ai.usage.cache_read_input_tokens", 300)
}

func TestTokenUsageMissingTranscriptDoesNotBreakSpan(t *testing.T) {
	dataDir := t.TempDir()
	t.Setenv("CLAUDE_PLUGIN_DATA", dataDir)
	srv, spans, _ := collectingServer(t)
	t.Setenv("DASH0_OTLP_URL", srv.URL)

	feed(t, `{"hook_event_name":"SessionStart","session_id":"sess-miss","model":"claude-sonnet-4-20250514"}`)
	feed(t, `{"hook_event_name":"UserPromptSubmit","session_id":"sess-miss","prompt":"hello"}`)
	feed(t, `{"hook_event_name":"Stop","session_id":"sess-miss","model":"claude-sonnet-4-20250514","transcript_path":"/nonexistent/path.jsonl"}`)

	// Span should still be created despite transcript read failure.
	chatSpan := findSpan(*spans, "chat")
	require.NotNil(t, chatSpan)
}

func TestConversationIDOnAllSpans(t *testing.T) {
	dataDir := t.TempDir()
	t.Setenv("CLAUDE_PLUGIN_DATA", dataDir)
	srv, spans, _ := collectingServer(t)
	t.Setenv("DASH0_OTLP_URL", srv.URL)

	feed(t, `{"hook_event_name":"SessionStart","session_id":"sess-conv","model":"claude-sonnet-4-20250514"}`)
	feed(t, `{"hook_event_name":"UserPromptSubmit","session_id":"sess-conv","prompt":"hello"}`)
	feed(t, `{"hook_event_name":"PreToolUse","session_id":"sess-conv","tool_name":"Bash","tool_use_id":"tu1"}`)
	feed(t, `{"hook_event_name":"PostToolUse","session_id":"sess-conv","tool_name":"Bash","tool_use_id":"tu1","tool_response":"ok"}`)
	feed(t, `{"hook_event_name":"Stop","session_id":"sess-conv"}`)

	require.Len(t, *spans, 2)

	// All spans carry gen_ai.conversation.id for session grouping.
	for _, span := range *spans {
		found := false
		for _, a := range span.Attributes {
			if a.Key == "gen_ai.conversation.id" {
				found = true
				assert.Equal(t, "sess-conv", *a.Value.StringValue)
			}
		}
		assert.True(t, found, "span %q should have gen_ai.conversation.id", span.Name)
	}
}
