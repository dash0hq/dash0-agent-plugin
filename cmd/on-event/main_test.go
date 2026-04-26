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

// sessionPath returns the path to a file inside the session-scoped directory.
func sessionPath(dataDir, sessionID, file string) string {
	return filepath.Join(dataDir, sessionID, file)
}

func TestIntegrationWritesAndTimestamps(t *testing.T) {
	dataDir := t.TempDir()
	t.Setenv("CLAUDE_PLUGIN_DATA", dataDir)

	before := time.Now().UTC()
	feed(t, `{"hook_event_name":"SessionStart","session_id":"abc123"}`)
	after := time.Now().UTC()

	lines := readLines(t, sessionPath(dataDir, "abc123", "events.jsonl"))
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

func TestIntegrationCreatesSessionDirectory(t *testing.T) {
	dataDir := filepath.Join(t.TempDir(), "nested", "path")
	t.Setenv("CLAUDE_PLUGIN_DATA", dataDir)

	feed(t, `{"hook_event_name":"SessionStart","session_id":"sess-create"}`)

	assert.FileExists(t, sessionPath(dataDir, "sess-create", "events.jsonl"))
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
	ctx, err := otlp.LoadTraceContext(filepath.Join(dataDir, "sess-3"))
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

	// But model is saved to trace context in the session directory.
	ctx, err := otlp.LoadTraceContext(filepath.Join(dataDir, "sess-no-span"))
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

func TestMissingSessionIDDoesNotCrash(t *testing.T) {
	dataDir := t.TempDir()
	t.Setenv("CLAUDE_PLUGIN_DATA", dataDir)

	// Events without session_id should not crash. A random session_id is
	// generated, so the event is written to a random session directory.
	feed(t, `{"hook_event_name":"SessionStart","model":"opus"}`)
	feed(t, `{"hook_event_name":"PreToolUse","tool_name":"Bash","tool_use_id":"tu-1"}`)

	// Verify session directories were created (with random names).
	entries, err := os.ReadDir(dataDir)
	require.NoError(t, err)
	assert.GreaterOrEqual(t, len(entries), 1, "at least one session directory should exist")
}

func TestMissingSessionIDSetsWarningAttribute(t *testing.T) {
	dataDir := t.TempDir()
	t.Setenv("CLAUDE_PLUGIN_DATA", dataDir)

	// Full turn without session_id — use the same random ID for all events
	// by feeding SessionStart first (which sets trace context in a random dir),
	// then capture that dir for subsequent events.
	// In practice this can't produce a span since each event gets a different
	// random session_id. But we verify the event log has the warning.
	feed(t, `{"hook_event_name":"SessionStart","model":"opus"}`)

	entries, err := os.ReadDir(dataDir)
	require.NoError(t, err)
	require.Len(t, entries, 1)

	// Read the event log from the random session directory.
	eventsFile := filepath.Join(dataDir, entries[0].Name(), "events.jsonl")
	lines := readLines(t, eventsFile)
	require.Len(t, lines, 1)

	var got map[string]any
	require.NoError(t, json.Unmarshal([]byte(lines[0]), &got))
	assert.Equal(t, "session_id was missing from hook payload", got["dash0.warning"])
}

func TestConcurrentSessionsAreIsolated(t *testing.T) {
	dataDir := t.TempDir()
	t.Setenv("CLAUDE_PLUGIN_DATA", dataDir)
	srv, spans, mu := collectingServer(t)
	t.Setenv("DASH0_OTLP_URL", srv.URL)

	// Session A
	feed(t, `{"hook_event_name":"SessionStart","session_id":"sess-A","model":"opus"}`)
	feed(t, `{"hook_event_name":"UserPromptSubmit","session_id":"sess-A","prompt":"task A"}`)
	// Session B starts while A is still running
	feed(t, `{"hook_event_name":"SessionStart","session_id":"sess-B","model":"sonnet"}`)
	feed(t, `{"hook_event_name":"UserPromptSubmit","session_id":"sess-B","prompt":"task B"}`)
	// Tool calls interleaved
	feed(t, `{"hook_event_name":"PreToolUse","session_id":"sess-A","tool_name":"Read","tool_use_id":"tu-A1"}`)
	feed(t, `{"hook_event_name":"PreToolUse","session_id":"sess-B","tool_name":"Bash","tool_use_id":"tu-B1"}`)
	feed(t, `{"hook_event_name":"PostToolUse","session_id":"sess-B","tool_name":"Bash","tool_use_id":"tu-B1","tool_response":"ok-B"}`)
	feed(t, `{"hook_event_name":"PostToolUse","session_id":"sess-A","tool_name":"Read","tool_use_id":"tu-A1","tool_response":"ok-A"}`)
	// Both stop
	feed(t, `{"hook_event_name":"Stop","session_id":"sess-A"}`)
	feed(t, `{"hook_event_name":"Stop","session_id":"sess-B"}`)

	mu.Lock()
	allSpans := make([]otlp.Span, len(*spans))
	copy(allSpans, *spans)
	mu.Unlock()

	// Should have 4 spans: 2 tool + 2 chat
	require.Len(t, allSpans, 4)

	// Separate spans by conversation ID.
	var spansA, spansB []otlp.Span
	for _, s := range allSpans {
		for _, a := range s.Attributes {
			if a.Key == "gen_ai.conversation.id" {
				if *a.Value.StringValue == "sess-A" {
					spansA = append(spansA, s)
				} else if *a.Value.StringValue == "sess-B" {
					spansB = append(spansB, s)
				}
			}
		}
	}

	require.Len(t, spansA, 2, "session A should have 2 spans (tool + chat)")
	require.Len(t, spansB, 2, "session B should have 2 spans (tool + chat)")

	// Spans within each session share a trace ID.
	assert.Equal(t, spansA[0].TraceID, spansA[1].TraceID, "session A spans should share trace ID")
	assert.Equal(t, spansB[0].TraceID, spansB[1].TraceID, "session B spans should share trace ID")

	// Sessions have different trace IDs.
	assert.NotEqual(t, spansA[0].TraceID, spansB[0].TraceID, "sessions should have different trace IDs")

	// Verify separate session directories exist.
	assert.DirExists(t, filepath.Join(dataDir, "sess-A"))
	assert.DirExists(t, filepath.Join(dataDir, "sess-B"))
}

func TestSessionEndCleansUpDirectory(t *testing.T) {
	dataDir := t.TempDir()
	t.Setenv("CLAUDE_PLUGIN_DATA", dataDir)

	feed(t, `{"hook_event_name":"SessionStart","session_id":"sess-cleanup","model":"opus"}`)
	assert.DirExists(t, filepath.Join(dataDir, "sess-cleanup"))

	feed(t, `{"hook_event_name":"SessionEnd","session_id":"sess-cleanup"}`)
	assert.NoDirExists(t, filepath.Join(dataDir, "sess-cleanup"))
}

func TestSessionEndEmitsChatSpanOnInterrupt(t *testing.T) {
	dataDir := t.TempDir()
	t.Setenv("CLAUDE_PLUGIN_DATA", dataDir)
	srv, spans, _ := collectingServer(t)
	t.Setenv("DASH0_OTLP_URL", srv.URL)

	// User starts a session, submits a prompt, but Ctrl+C before Stop fires.
	feed(t, `{"hook_event_name":"SessionStart","session_id":"sess-interrupt","model":"claude-opus-4-6"}`)
	feed(t, `{"hook_event_name":"UserPromptSubmit","session_id":"sess-interrupt","prompt":"do something"}`)
	feed(t, `{"hook_event_name":"PreToolUse","session_id":"sess-interrupt","tool_name":"Bash","tool_use_id":"tu-int"}`)
	feed(t, `{"hook_event_name":"PostToolUseFailure","session_id":"sess-interrupt","tool_name":"Bash","tool_use_id":"tu-int","error":"interrupted","is_interrupt":true}`)
	// SessionEnd fires (Ctrl+C) — no Stop was received.
	feed(t, `{"hook_event_name":"SessionEnd","session_id":"sess-interrupt"}`)

	// Should have 2 spans: tool (error) + chat (error fallback from SessionEnd).
	require.Len(t, *spans, 2)

	toolSpan := findSpan(*spans, "execute_tool")
	chatSpan := findSpan(*spans, "chat")

	require.NotNil(t, toolSpan)
	require.NotNil(t, chatSpan)

	// Tool span has error status.
	assert.Equal(t, otlp.StatusCodeError, toolSpan.Status.Code)

	// Chat span has error status with message.
	assert.Equal(t, otlp.StatusCodeError, chatSpan.Status.Code)
	assert.Equal(t, "session ended before completion", chatSpan.Status.Message)
	assert.Empty(t, chatSpan.ParentSpanID, "chat span should be root")
}

func TestSessionEndBeforePromptDoesNotEmitSpan(t *testing.T) {
	dataDir := t.TempDir()
	t.Setenv("CLAUDE_PLUGIN_DATA", dataDir)
	srv, spans, _ := collectingServer(t)
	t.Setenv("DASH0_OTLP_URL", srv.URL)

	// User starts a session but exits before submitting any prompt.
	feed(t, `{"hook_event_name":"SessionStart","session_id":"sess-early-exit","model":"opus"}`)
	feed(t, `{"hook_event_name":"SessionEnd","session_id":"sess-early-exit"}`)

	// No spans — no UserPromptSubmit means no trace context, nothing to emit.
	assert.Empty(t, *spans)
}

func TestSessionEndAfterNormalStopDoesNotDuplicate(t *testing.T) {
	dataDir := t.TempDir()
	t.Setenv("CLAUDE_PLUGIN_DATA", dataDir)
	srv, spans, _ := collectingServer(t)
	t.Setenv("DASH0_OTLP_URL", srv.URL)

	// Normal flow followed by SessionEnd.
	feed(t, `{"hook_event_name":"SessionStart","session_id":"sess-normal-end","model":"claude-opus-4-6"}`)
	feed(t, `{"hook_event_name":"UserPromptSubmit","session_id":"sess-normal-end","prompt":"hello"}`)
	feed(t, `{"hook_event_name":"PreToolUse","session_id":"sess-normal-end","tool_name":"Read","tool_use_id":"tu-ne"}`)
	feed(t, `{"hook_event_name":"PostToolUse","session_id":"sess-normal-end","tool_name":"Read","tool_use_id":"tu-ne","tool_response":"ok"}`)
	feed(t, `{"hook_event_name":"Stop","session_id":"sess-normal-end"}`)
	feed(t, `{"hook_event_name":"SessionEnd","session_id":"sess-normal-end"}`)

	// Should have exactly 2 spans (tool + chat). No duplicate chat span from SessionEnd.
	require.Len(t, *spans, 2)
}

func TestResumedSessionPicksUpExistingState(t *testing.T) {
	dataDir := t.TempDir()
	t.Setenv("CLAUDE_PLUGIN_DATA", dataDir)
	srv, spans, _ := collectingServer(t)
	t.Setenv("DASH0_OTLP_URL", srv.URL)

	// First invocation — SessionStart saves model.
	feed(t, `{"hook_event_name":"SessionStart","session_id":"sess-resume","model":"claude-opus-4-6"}`)

	// Second invocation (resumed) — UserPromptSubmit should pick up model.
	feed(t, `{"hook_event_name":"UserPromptSubmit","session_id":"sess-resume","prompt":"continue work"}`)
	feed(t, `{"hook_event_name":"PreToolUse","session_id":"sess-resume","tool_name":"Bash","tool_use_id":"tu-r1"}`)
	feed(t, `{"hook_event_name":"PostToolUse","session_id":"sess-resume","tool_name":"Bash","tool_use_id":"tu-r1","tool_response":"ok"}`)
	feed(t, `{"hook_event_name":"Stop","session_id":"sess-resume"}`)

	chatSpan := findSpan(*spans, "chat")
	require.NotNil(t, chatSpan)

	// Chat span should have the model from SessionStart.
	assert.Contains(t, chatSpan.Name, "claude-opus-4-6")
}

func TestInvalidOTLPUrlDoesNotCrash(t *testing.T) {
	dataDir := t.TempDir()
	t.Setenv("CLAUDE_PLUGIN_DATA", dataDir)
	t.Setenv("DASH0_OTLP_URL", "not-a-url")

	// Should not crash — invalid URL is logged and export is disabled.
	feed(t, `{"hook_event_name":"SessionStart","session_id":"sess-badurl","model":"opus"}`)
	feed(t, `{"hook_event_name":"UserPromptSubmit","session_id":"sess-badurl","prompt":"test"}`)
	feed(t, `{"hook_event_name":"Stop","session_id":"sess-badurl"}`)
}

func TestMissingSchemeInOTLPUrl(t *testing.T) {
	dataDir := t.TempDir()
	t.Setenv("CLAUDE_PLUGIN_DATA", dataDir)
	t.Setenv("DASH0_OTLP_URL", "ingress.dash0.com:4318")

	// Missing scheme — should not crash.
	feed(t, `{"hook_event_name":"SessionStart","session_id":"sess-noscheme","model":"opus"}`)
	feed(t, `{"hook_event_name":"UserPromptSubmit","session_id":"sess-noscheme","prompt":"test"}`)
	feed(t, `{"hook_event_name":"Stop","session_id":"sess-noscheme"}`)
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

	lines := readLines(t, sessionPath(dataDir, "sess-stamp", "events.jsonl"))
	require.Len(t, lines, 2) // SessionStart + UserPromptSubmit

	var got map[string]any
	require.NoError(t, json.Unmarshal([]byte(lines[1]), &got))

	chatSpanID, ok := got["chat_span_id"].(string)
	require.True(t, ok, "chat_span_id should be stamped on event")
	assert.Len(t, chatSpanID, 16) // 8 bytes = 16 hex chars

	// Trace context should also be saved in the session directory.
	ctx, err := otlp.LoadTraceContext(filepath.Join(dataDir, "sess-stamp"))
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
