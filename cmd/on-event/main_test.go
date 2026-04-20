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

func TestMissingSessionIDUsesRandomTraceID(t *testing.T) {
	dataDir := t.TempDir()
	t.Setenv("CLAUDE_PLUGIN_DATA", dataDir)
	srv, spans, _ := collectingServer(t)
	t.Setenv("DASH0_OTLP_URL", srv.URL)

	// Event without session_id.
	feed(t, `{"hook_event_name":"SessionStart","model":"claude-sonnet-4-20250514"}`)

	// Span should still be emitted (not skipped).
	require.Len(t, *spans, 1)

	span := (*spans)[0]
	// Should have a non-empty trace ID (random, not hash of empty string).
	assert.NotEmpty(t, span.TraceID)
	assert.Len(t, span.TraceID, 32) // 16 bytes hex

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

func TestTwoMissingSessionIDEventsGetDifferentTraceIDs(t *testing.T) {
	dataDir := t.TempDir()
	t.Setenv("CLAUDE_PLUGIN_DATA", dataDir)
	srv, spans, _ := collectingServer(t)
	t.Setenv("DASH0_OTLP_URL", srv.URL)

	feed(t, `{"hook_event_name":"SessionStart","model":"claude-sonnet-4-20250514"}`)
	feed(t, `{"hook_event_name":"SessionStart","model":"claude-sonnet-4-20250514"}`)

	require.Len(t, *spans, 2)
	// Each should get a different trace ID (not merging).
	assert.NotEqual(t, (*spans)[0].TraceID, (*spans)[1].TraceID)
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

func TestToolSpansNestedUnderChatSpan(t *testing.T) {
	dataDir := t.TempDir()
	t.Setenv("CLAUDE_PLUGIN_DATA", dataDir)
	srv, spans, _ := collectingServer(t)
	t.Setenv("DASH0_OTLP_URL", srv.URL)

	feed(t, `{"hook_event_name":"SessionStart","session_id":"sess-1","model":"claude-sonnet-4-20250514"}`)
	feed(t, `{"hook_event_name":"UserPromptSubmit","session_id":"sess-1","prompt":"hello"}`)
	feed(t, `{"hook_event_name":"PreToolUse","session_id":"sess-1","tool_name":"Bash","tool_use_id":"tu1"}`)
	feed(t, `{"hook_event_name":"PostToolUse","session_id":"sess-1","tool_name":"Bash","tool_use_id":"tu1","tool_response":"ok"}`)
	feed(t, `{"hook_event_name":"Stop","session_id":"sess-1","model":"claude-sonnet-4-20250514"}`)

	require.Len(t, *spans, 3) // session + tool + chat

	sessionSpan := findSpan(*spans, "session_start")
	toolSpan := findSpan(*spans, "execute_tool")
	chatSpan := findSpan(*spans, "chat")

	require.NotNil(t, sessionSpan)
	require.NotNil(t, toolSpan)
	require.NotNil(t, chatSpan)

	// Chat span is child of session.
	assert.Equal(t, sessionSpan.SpanID, chatSpan.ParentSpanID)
	// Tool span is child of chat span.
	assert.Equal(t, chatSpan.SpanID, toolSpan.ParentSpanID)
	// Tool span is NOT child of session directly.
	assert.NotEqual(t, sessionSpan.SpanID, toolSpan.ParentSpanID)
}

func TestToolSpansFallBackToSessionWithoutUserPromptSubmit(t *testing.T) {
	dataDir := t.TempDir()
	t.Setenv("CLAUDE_PLUGIN_DATA", dataDir)
	srv, spans, _ := collectingServer(t)
	t.Setenv("DASH0_OTLP_URL", srv.URL)

	feed(t, `{"hook_event_name":"SessionStart","session_id":"sess-2","model":"claude-sonnet-4-20250514"}`)
	// No UserPromptSubmit — simulate missing hook event.
	feed(t, `{"hook_event_name":"PreToolUse","session_id":"sess-2","tool_name":"Read","tool_use_id":"tu2"}`)
	feed(t, `{"hook_event_name":"PostToolUse","session_id":"sess-2","tool_name":"Read","tool_use_id":"tu2","tool_response":"ok"}`)

	require.Len(t, *spans, 2) // session + tool

	sessionSpan := findSpan(*spans, "session_start")
	toolSpan := findSpan(*spans, "execute_tool")

	require.NotNil(t, sessionSpan)
	require.NotNil(t, toolSpan)

	// Without a chat span ID, tool falls back to session as parent.
	assert.Equal(t, sessionSpan.SpanID, toolSpan.ParentSpanID)
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

	require.Len(t, *spans, 3) // session + Agent tool + sub-agent Bash tool

	agentToolSpan := findSpan(*spans, "execute_tool Agent")
	subToolSpan := findSpan(*spans, "execute_tool Bash")

	require.NotNil(t, agentToolSpan)
	require.NotNil(t, subToolSpan)

	// Sub-agent tool is nested under the Agent tool span.
	expectedParent := otlp.SpanIDFromAgentID("agent-42")
	assert.Equal(t, expectedParent, subToolSpan.ParentSpanID)
	assert.Equal(t, expectedParent, agentToolSpan.SpanID)

	// Verify Agent tool span parent is the chat span (not session directly).
	sessionSpanID := otlp.SpanIDFromSessionID("sess-3")
	assert.NotEqual(t, sessionSpanID, agentToolSpan.ParentSpanID)
	assert.NotEmpty(t, agentToolSpan.ParentSpanID)
}

func TestSessionStartMidTurnDoesNotBreakNesting(t *testing.T) {
	dataDir := t.TempDir()
	t.Setenv("CLAUDE_PLUGIN_DATA", dataDir)
	srv, spans, _ := collectingServer(t)
	t.Setenv("DASH0_OTLP_URL", srv.URL)

	// Reproduce the real-world sequence: SessionStart fires mid-turn
	// (e.g. session restart or sub-agent lifecycle). Because chat_span_id
	// lives in the event log (not trace_context.json), the SessionStart
	// cannot stomp on it.
	feed(t, `{"hook_event_name":"SessionStart","session_id":"sess-mid","model":"claude-sonnet-4-20250514"}`)
	feed(t, `{"hook_event_name":"UserPromptSubmit","session_id":"sess-mid","prompt":"hello"}`)

	// Tool call before mid-turn SessionStart.
	feed(t, `{"hook_event_name":"PreToolUse","session_id":"sess-mid","tool_name":"Bash","tool_use_id":"tu-early"}`)
	feed(t, `{"hook_event_name":"PostToolUse","session_id":"sess-mid","tool_name":"Bash","tool_use_id":"tu-early","tool_response":"ok"}`)

	// Another SessionStart mid-turn — must NOT break nesting.
	feed(t, `{"hook_event_name":"SessionStart","session_id":"sess-mid","model":"claude-sonnet-4-20250514"}`)

	// Tool call after the mid-turn SessionStart.
	feed(t, `{"hook_event_name":"PreToolUse","session_id":"sess-mid","tool_name":"Read","tool_use_id":"tu-late"}`)
	feed(t, `{"hook_event_name":"PostToolUse","session_id":"sess-mid","tool_name":"Read","tool_use_id":"tu-late","tool_response":"ok"}`)
	feed(t, `{"hook_event_name":"Stop","session_id":"sess-mid","model":"claude-sonnet-4-20250514"}`)

	// session(2) + Bash + Read + chat = 5 spans
	require.Len(t, *spans, 5)

	chatSpan := findSpan(*spans, "chat")
	require.NotNil(t, chatSpan)

	// Both tool spans must be children of the same chat span.
	var toolSpans []otlp.Span
	for _, s := range *spans {
		if strings.HasPrefix(s.Name, "execute_tool") {
			toolSpans = append(toolSpans, s)
		}
	}
	require.Len(t, toolSpans, 2)

	for _, ts := range toolSpans {
		assert.Equal(t, chatSpan.SpanID, ts.ParentSpanID,
			"tool span %q should be child of chat span", ts.Name)
	}
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

	feed(t, `{"hook_event_name":"UserPromptSubmit","session_id":"sess-stamp","prompt":"hello"}`)

	lines := readLines(t, filepath.Join(dataDir, "events.jsonl"))
	require.Len(t, lines, 1)

	var got map[string]any
	require.NoError(t, json.Unmarshal([]byte(lines[0]), &got))

	chatSpanID, ok := got["chat_span_id"].(string)
	require.True(t, ok, "chat_span_id should be stamped on event")
	assert.Len(t, chatSpanID, 16) // 8 bytes = 16 hex chars
}

func TestUserPromptSubmitSubAgentDoesNotStampChatSpanID(t *testing.T) {
	dataDir := t.TempDir()
	t.Setenv("CLAUDE_PLUGIN_DATA", dataDir)

	// Sub-agent UserPromptSubmit (if it ever fires) should NOT get a chat_span_id.
	feed(t, `{"hook_event_name":"UserPromptSubmit","session_id":"sess-sub","prompt":"hi","agent_id":"agent-99"}`)

	lines := readLines(t, filepath.Join(dataDir, "events.jsonl"))
	require.Len(t, lines, 1)

	var got map[string]any
	require.NoError(t, json.Unmarshal([]byte(lines[0]), &got))

	_, hasChatSpanID := got["chat_span_id"]
	assert.False(t, hasChatSpanID, "sub-agent event should not get chat_span_id")
}

func TestLookupChatSpanID(t *testing.T) {
	dataDir := t.TempDir()
	t.Setenv("CLAUDE_PLUGIN_DATA", dataDir)

	// No events — returns empty.
	assert.Empty(t, lookupChatSpanID(dataDir))

	// After a main-agent UserPromptSubmit — returns the stamped ID.
	feed(t, `{"hook_event_name":"UserPromptSubmit","session_id":"sess-lu","prompt":"hello"}`)
	id := lookupChatSpanID(dataDir)
	assert.Len(t, id, 16)

	// Survives a SessionStart (event log is append-only, not overwritten).
	feed(t, `{"hook_event_name":"SessionStart","session_id":"sess-lu"}`)
	assert.Equal(t, id, lookupChatSpanID(dataDir))
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

func TestTokenUsageAggregatesMultipleIterations(t *testing.T) {
	dataDir := t.TempDir()
	t.Setenv("CLAUDE_PLUGIN_DATA", dataDir)
	srv, spans, _ := collectingServer(t)
	t.Setenv("DASH0_OTLP_URL", srv.URL)

	// Transcript with two iterations (agentic loop).
	transcriptPath := filepath.Join(dataDir, "transcript.jsonl")
	writeTranscript(t, transcriptPath, []string{
		`{"type":"user","message":{"role":"user","content":[{"type":"text","text":"do it"}]}}`,
		`{"type":"assistant","requestId":"req_001","message":{"role":"assistant","content":[{"type":"text","text":"thinking"}],"usage":{"input_tokens":100,"output_tokens":50,"cache_creation_input_tokens":0,"cache_read_input_tokens":0}}}`,
		`{"type":"assistant","requestId":"req_002","message":{"role":"assistant","content":[{"type":"text","text":"done"}],"usage":{"input_tokens":200,"output_tokens":80,"cache_creation_input_tokens":0,"cache_read_input_tokens":0}}}`,
	})

	feed(t, `{"hook_event_name":"SessionStart","session_id":"sess-agg","model":"claude-sonnet-4-20250514"}`)
	feed(t, `{"hook_event_name":"UserPromptSubmit","session_id":"sess-agg","prompt":"do it"}`)
	feed(t, fmt.Sprintf(`{"hook_event_name":"Stop","session_id":"sess-agg","model":"claude-sonnet-4-20250514","transcript_path":"%s"}`, transcriptPath))

	chatSpan := findSpan(*spans, "chat")
	require.NotNil(t, chatSpan)

	assertIntAttr(t, chatSpan.Attributes, "gen_ai.usage.input_tokens", 300)
	assertIntAttr(t, chatSpan.Attributes, "gen_ai.usage.output_tokens", 130)
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
