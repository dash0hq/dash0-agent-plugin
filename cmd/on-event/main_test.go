// SPDX-FileCopyrightText: Copyright 2026 Dash0 Inc.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"compress/zlib"
	"encoding/base64"
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

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/dash0hq/dash0-agent-plugin/internal/otlp"
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

func TestToolSpanUsesDurationMsFallback(t *testing.T) {
	dataDir := t.TempDir()
	t.Setenv("CLAUDE_PLUGIN_DATA", dataDir)
	srv, spans, _ := collectingServer(t)
	t.Setenv("DASH0_OTLP_URL", srv.URL)

	feed(t, `{"hook_event_name":"SessionStart","session_id":"sess-dur","model":"opus"}`)
	feed(t, `{"hook_event_name":"UserPromptSubmit","session_id":"sess-dur","prompt":"test"}`)
	// PostToolUse with a different tool_use_id than any PreToolUse (simulates mismatched IDs).
	// The fallback should use duration_ms to compute start time.
	feed(t, `{"hook_event_name":"PostToolUse","session_id":"sess-dur","tool_name":"Read","tool_use_id":"mismatched-id","tool_response":"ok","duration_ms":500}`)

	require.Len(t, *spans, 1)
	toolSpan := (*spans)[0]

	// Start time should be ~500ms before end time (not equal).
	start, _ := strconv.ParseInt(toolSpan.StartTimeUnixNano, 10, 64)
	end, _ := strconv.ParseInt(toolSpan.EndTimeUnixNano, 10, 64)
	durationNs := end - start
	// 500ms = 500_000_000 ns. Allow some tolerance for timestamp precision.
	assert.InDelta(t, 500_000_000, durationNs, 10_000_000, "duration should be ~500ms from duration_ms fallback")
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
				switch *a.Value.StringValue {
				case "sess-A":
					spansA = append(spansA, s)
				case "sess-B":
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

func TestPluginOption(t *testing.T) {
	t.Run("prefers CLAUDE_PLUGIN_OPTION over DASH0", func(t *testing.T) {
		t.Setenv("CLAUDE_PLUGIN_OPTION_TEST_KEY", "from-user-config")
		t.Setenv("DASH0_TEST_KEY", "from-env-var")
		assert.Equal(t, "from-user-config", pluginOption("TEST_KEY"))
	})

	t.Run("falls back to DASH0 when CLAUDE_PLUGIN_OPTION unset", func(t *testing.T) {
		t.Setenv("DASH0_TEST_KEY", "from-env-var")
		assert.Equal(t, "from-env-var", pluginOption("TEST_KEY"))
	})

	t.Run("falls back to DASH0 when CLAUDE_PLUGIN_OPTION empty", func(t *testing.T) {
		// userConfig may set an empty string when the user skips an optional
		// field; that must fall through, not shadow, the env-var fallback.
		t.Setenv("CLAUDE_PLUGIN_OPTION_TEST_KEY", "")
		t.Setenv("DASH0_TEST_KEY", "from-env-var")
		assert.Equal(t, "from-env-var", pluginOption("TEST_KEY"))
	})

	t.Run("returns empty when neither set", func(t *testing.T) {
		assert.Equal(t, "", pluginOption("TEST_KEY"))
	})
}

func TestPluginOptionSecure(t *testing.T) {
	t.Run("reads only from CLAUDE_PLUGIN_OPTION", func(t *testing.T) {
		t.Setenv("CLAUDE_PLUGIN_OPTION_AUTH_TOKEN", "secure-token")
		t.Setenv("DASH0_AUTH_TOKEN", "leaked-token")
		assert.Equal(t, "secure-token", pluginOptionSecure("AUTH_TOKEN"))
	})

	t.Run("does NOT fall back to DASH0 env var", func(t *testing.T) {
		t.Setenv("CLAUDE_PLUGIN_OPTION_AUTH_TOKEN", "")
		t.Setenv("DASH0_AUTH_TOKEN", "leaked-token")
		assert.Equal(t, "", pluginOptionSecure("AUTH_TOKEN"))
	})

	t.Run("returns empty when nothing set", func(t *testing.T) {
		assert.Equal(t, "", pluginOptionSecure("AUTH_TOKEN"))
	})
}

func TestPluginOptionBool(t *testing.T) {
	t.Run("prefers CLAUDE_PLUGIN_OPTION over DASH0", func(t *testing.T) {
		t.Setenv("CLAUDE_PLUGIN_OPTION_TEST_BOOL", "true")
		t.Setenv("DASH0_TEST_BOOL", "false")
		assert.True(t, pluginOptionBool("TEST_BOOL"))
	})

	t.Run("falls back to DASH0 when CLAUDE_PLUGIN_OPTION empty", func(t *testing.T) {
		t.Setenv("CLAUDE_PLUGIN_OPTION_TEST_BOOL", "")
		t.Setenv("DASH0_TEST_BOOL", "1")
		assert.True(t, pluginOptionBool("TEST_BOOL"))
	})
}

func TestPluginOptionBoolDefault(t *testing.T) {
	t.Run("returns default when env is unset", func(t *testing.T) {
		t.Setenv("CLAUDE_PLUGIN_OPTION_MY_FLAG", "")
		t.Setenv("DASH0_MY_FLAG", "")
		assert.True(t, pluginOptionBoolDefault("MY_FLAG", true))
		assert.False(t, pluginOptionBoolDefault("MY_FLAG", false))
	})

	t.Run("explicit false overrides default true", func(t *testing.T) {
		t.Setenv("CLAUDE_PLUGIN_OPTION_MY_FLAG", "false")
		assert.False(t, pluginOptionBoolDefault("MY_FLAG", true))
	})

	t.Run("explicit true overrides default false", func(t *testing.T) {
		t.Setenv("CLAUDE_PLUGIN_OPTION_MY_FLAG", "true")
		assert.True(t, pluginOptionBoolDefault("MY_FLAG", false))
	})
}

func TestDeriveAppURL(t *testing.T) {
	tests := []struct {
		name    string
		otlpURL string
		want    string
	}{
		{"dash0 prod us1", "https://ingress.us1.dash0.com:4318", "https://app.dash0.com"},
		{"dash0 prod eu1", "https://ingress.eu1.dash0.com:4318", "https://app.dash0.com"},
		{"dash0 dev", "https://ingress.eu-west-1.aws.dash0-dev.com:4318", "https://app.dash0-dev.com"},
		{"dash0 dev no port", "https://ingress.eu-west-1.aws.dash0-dev.com", "https://app.dash0-dev.com"},
		{"unknown endpoint", "https://otel.example.com:4318", ""},
		{"empty", "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, deriveAppURL(tt.otlpURL))
		})
	}
}

func TestBuildSessionURL(t *testing.T) {
	u := buildSessionURL("https://app.dash0.com", "sess-abc123")
	assert.Contains(t, u, "https://app.dash0.com/coding-agents?s=")
	assert.NotContains(t, u, "agent-monitoring")

	// Round-trip: decode the ?s= param and verify the state structure matches
	// what the Dash0 UI url-state library expects.
	parts := strings.SplitN(u, "?s=", 2)
	require.Len(t, parts, 2)
	compressed, err := base64.URLEncoding.WithPadding(base64.NoPadding).DecodeString(parts[1])
	require.NoError(t, err)
	r, err := zlib.NewReader(bytes.NewReader(compressed))
	require.NoError(t, err)
	decoded, err := io.ReadAll(r)
	require.NoError(t, err)

	var state map[string]any
	require.NoError(t, json.Unmarshal(decoded, &state))
	page, ok := state["/coding-agents"].(map[string]any)
	require.True(t, ok, "state must be keyed by pathname")
	// agentSession drives the detail sidebar; the sessions tab must be selected.
	assert.Equal(t, "sess-abc123", page["agentSession"])
	tab, ok := page["tab"].(map[string]any)
	require.True(t, ok, "coding-agents state must carry a tab selection")
	assert.Equal(t, "sessions", tab["pageTab"])
}

func TestSessionStartHintWhenNotConfigured(t *testing.T) {
	dataDir := t.TempDir()
	env := append(os.Environ(),
		"CLAUDE_PLUGIN_DATA="+dataDir,
		// No OTLP_URL via either mechanism. Hint should fire on SessionStart.
		"DASH0_OTLP_URL=",
		"CLAUDE_PLUGIN_OPTION_OTLP_URL=",
	)
	stdout, _ := execBinary(t, `{"hook_event_name":"SessionStart","session_id":"sess-unconfigured","model":"opus"}`, env)
	assert.Contains(t, stdout, `"systemMessage"`)
	assert.Contains(t, stdout, "telemetry is not active")
	assert.Contains(t, stdout, "/reload-plugins")
}

func TestSessionStartHintSuppressedWhenConfigured(t *testing.T) {
	dataDir := t.TempDir()
	srv, _, _ := collectingServer(t)
	env := append(os.Environ(),
		"CLAUDE_PLUGIN_DATA="+dataDir,
		"DASH0_OTLP_URL="+srv.URL,
	)
	stdout, _ := execBinary(t, `{"hook_event_name":"SessionStart","session_id":"sess-configured","model":"opus"}`, env)
	assert.NotContains(t, stdout, "telemetry is not active")
	assert.Contains(t, stdout, `"systemMessage"`)
	assert.Contains(t, stdout, "dash0: connected")
}

func TestSessionStartConnectivityFailure(t *testing.T) {
	dataDir := t.TempDir()
	env := append(os.Environ(),
		"CLAUDE_PLUGIN_DATA="+dataDir,
		"DASH0_OTLP_URL=http://localhost:1", // unreachable port
		"CLAUDE_PLUGIN_OPTION_OTLP_URL=",
	)
	stdout, _ := execBinary(t, `{"hook_event_name":"SessionStart","session_id":"sess-connfail","model":"opus"}`, env)
	assert.Contains(t, stdout, "connectivity check failed")
}

func TestHintNotEmittedOnNonSessionStartEvents(t *testing.T) {
	// Only fires on SessionStart so we don't spam every tool call.
	dataDir := t.TempDir()
	env := append(os.Environ(),
		"CLAUDE_PLUGIN_DATA="+dataDir,
		"DASH0_OTLP_URL=",
	)
	stdout, _ := execBinary(t, `{"hook_event_name":"PreToolUse","session_id":"sess-x","tool_name":"Bash","tool_use_id":"tu1"}`, env)
	assert.NotContains(t, stdout, "systemMessage")
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

	// Tool span should have redacted input/output content.
	assertStringAttr(t, toolSpan.Attributes, "gen_ai.tool.call.arguments", "<REDACTED>")
	assertStringAttr(t, toolSpan.Attributes, "gen_ai.tool.call.result", "<REDACTED>")

	// Chat span should have redacted prompt/response content but preserve JSON structure.
	assertAttrContains(t, chatSpan.Attributes, "gen_ai.input.messages", `"role":"user"`)
	assertAttrContains(t, chatSpan.Attributes, "gen_ai.input.messages", `REDACTED`)
	assertAttrContains(t, chatSpan.Attributes, "gen_ai.output.messages", `"role":"assistant"`)
	assertAttrContains(t, chatSpan.Attributes, "gen_ai.output.messages", `REDACTED`)
}

func TestTeamNameOnAllSpans(t *testing.T) {
	dataDir := t.TempDir()
	t.Setenv("CLAUDE_PLUGIN_DATA", dataDir)
	srv, spans, _ := collectingServer(t)
	t.Setenv("DASH0_OTLP_URL", srv.URL)
	t.Setenv("DASH0_TEAM_NAME", "platform")

	feed(t, `{"hook_event_name":"SessionStart","session_id":"sess-team","model":"claude-sonnet-4-20250514"}`)
	feed(t, `{"hook_event_name":"UserPromptSubmit","session_id":"sess-team","prompt":"hello"}`)
	feed(t, `{"hook_event_name":"PreToolUse","session_id":"sess-team","tool_name":"Bash","tool_use_id":"tu-team"}`)
	feed(t, `{"hook_event_name":"PostToolUse","session_id":"sess-team","tool_name":"Bash","tool_use_id":"tu-team","tool_input":"ls","tool_response":"ok"}`)
	feed(t, `{"hook_event_name":"Stop","session_id":"sess-team","model":"claude-sonnet-4-20250514","last_assistant_message":"done"}`)

	toolSpan := findSpan(*spans, "execute_tool")
	chatSpan := findSpan(*spans, "chat")

	require.NotNil(t, toolSpan)
	require.NotNil(t, chatSpan)

	assertStringAttr(t, toolSpan.Attributes, "dash0.team.name", "platform")
	assertStringAttr(t, chatSpan.Attributes, "dash0.team.name", "platform")
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
	assertIntAttr(t, chatSpan.Attributes, "gen_ai.usage.cache_creation.input_tokens", 200)
	assertIntAttr(t, chatSpan.Attributes, "gen_ai.usage.cache_read.input_tokens", 300)
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

func TestModelOnToolSpanFromTranscriptWhenSessionStartOmitsModel(t *testing.T) {
	dataDir := t.TempDir()
	t.Setenv("CLAUDE_PLUGIN_DATA", dataDir)
	srv, spans, _ := collectingServer(t)
	t.Setenv("DASH0_OTLP_URL", srv.URL)

	// Create transcript with model info.
	transcriptPath := filepath.Join(dataDir, "transcript.jsonl")
	writeTranscript(t, transcriptPath, []string{
		`{"type":"user","message":{"role":"user","content":[{"type":"text","text":"hello"}]}}`,
		`{"type":"assistant","requestId":"req_001","message":{"role":"assistant","model":"claude-opus-4-7","content":[{"type":"text","text":"hi"}],"usage":{"input_tokens":10,"output_tokens":5}}}`,
	})

	// SessionStart WITHOUT model — simulates the real-world bug.
	feed(t, `{"hook_event_name":"SessionStart","session_id":"sess-no-model"}`)
	feed(t, `{"hook_event_name":"UserPromptSubmit","session_id":"sess-no-model","prompt":"hello"}`)
	feed(t, fmt.Sprintf(`{"hook_event_name":"PostToolUse","session_id":"sess-no-model","tool_name":"Bash","tool_use_id":"tu1","tool_response":"ok","transcript_path":"%s"}`, transcriptPath))
	feed(t, fmt.Sprintf(`{"hook_event_name":"Stop","session_id":"sess-no-model","transcript_path":"%s"}`, transcriptPath))

	require.Len(t, *spans, 2, "expected tool span + chat span")

	// Tool span should have model from transcript fallback.
	toolSpan := findSpan(*spans, "execute_tool")
	require.NotNil(t, toolSpan, "tool span should exist")
	assertStringAttr(t, toolSpan.Attributes, "gen_ai.request.model", "claude-opus-4-7")

	// Chat span should also have model from transcript.
	chatSpan := findSpan(*spans, "chat")
	require.NotNil(t, chatSpan, "chat span should exist")
	assertStringAttr(t, chatSpan.Attributes, "gen_ai.request.model", "claude-opus-4-7")
}

func TestPRURLSurvivesOmitIO(t *testing.T) {
	dataDir := t.TempDir()
	t.Setenv("CLAUDE_PLUGIN_DATA", dataDir)
	srv, spans, _ := collectingServer(t)
	t.Setenv("DASH0_OTLP_URL", srv.URL)
	t.Setenv("DASH0_OMIT_IO", "true")

	feed(t, `{"hook_event_name":"SessionStart","session_id":"sess-pr","model":"claude-sonnet-4-20250514"}`)
	feed(t, `{"hook_event_name":"UserPromptSubmit","session_id":"sess-pr","prompt":"create PR"}`)
	feed(t, `{"hook_event_name":"PreToolUse","session_id":"sess-pr","tool_name":"Bash","tool_use_id":"tu-pr"}`)
	feed(t, `{"hook_event_name":"PostToolUse","session_id":"sess-pr","tool_name":"Bash","tool_use_id":"tu-pr","tool_input":"gh pr create","tool_response":"Creating pull request...\nhttps://github.com/dash0hq/dash0-agent-plugin/pull/94\n"}`)
	feed(t, `{"hook_event_name":"Stop","session_id":"sess-pr"}`)

	toolSpan := findSpan(*spans, "execute_tool")
	require.NotNil(t, toolSpan)

	// Tool I/O is redacted.
	assertStringAttr(t, toolSpan.Attributes, "gen_ai.tool.call.arguments", "<REDACTED>")
	assertStringAttr(t, toolSpan.Attributes, "gen_ai.tool.call.result", "<REDACTED>")

	// But the PR URL is extracted as a dedicated attribute.
	assertStringAttr(t, toolSpan.Attributes, "dash0.gen_ai.vcs.pull_request.url", "https://github.com/dash0hq/dash0-agent-plugin/pull/94")
}

func TestCommitSHAExtractedOnToolSpan(t *testing.T) {
	dataDir := t.TempDir()
	t.Setenv("CLAUDE_PLUGIN_DATA", dataDir)
	srv, spans, _ := collectingServer(t)
	t.Setenv("DASH0_OTLP_URL", srv.URL)
	t.Setenv("DASH0_OMIT_IO", "true")

	feed(t, `{"hook_event_name":"SessionStart","session_id":"sess-sha","model":"claude-sonnet-4-20250514"}`)
	feed(t, `{"hook_event_name":"UserPromptSubmit","session_id":"sess-sha","prompt":"commit"}`)
	feed(t, `{"hook_event_name":"PreToolUse","session_id":"sess-sha","tool_name":"Bash","tool_use_id":"tu-sha"}`)
	feed(t, `{"hook_event_name":"PostToolUse","session_id":"sess-sha","tool_name":"Bash","tool_use_id":"tu-sha","tool_input":"git commit","tool_response":{"stdout":"[feat/my-branch 82717dc] feat: add feature\n 3 files changed","stderr":""}}`)

	toolSpan := findSpan(*spans, "execute_tool")
	require.NotNil(t, toolSpan)

	assertStringAttr(t, toolSpan.Attributes, "dash0.gen_ai.vcs.commit.sha", "82717dc")
	// Tool I/O still redacted.
	assertStringAttr(t, toolSpan.Attributes, "gen_ai.tool.call.result", "<REDACTED>")
}

func TestIssueURLExtractedOnToolSpan(t *testing.T) {
	dataDir := t.TempDir()
	t.Setenv("CLAUDE_PLUGIN_DATA", dataDir)
	srv, spans, _ := collectingServer(t)
	t.Setenv("DASH0_OTLP_URL", srv.URL)

	feed(t, `{"hook_event_name":"SessionStart","session_id":"sess-issue","model":"claude-sonnet-4-20250514"}`)
	feed(t, `{"hook_event_name":"UserPromptSubmit","session_id":"sess-issue","prompt":"create issue"}`)
	feed(t, `{"hook_event_name":"PreToolUse","session_id":"sess-issue","tool_name":"Bash","tool_use_id":"tu-issue"}`)
	feed(t, `{"hook_event_name":"PostToolUse","session_id":"sess-issue","tool_name":"Bash","tool_use_id":"tu-issue","tool_response":{"stdout":"https://github.com/dash0hq/dash0-agent-plugin/issues/93","stderr":""}}`)

	toolSpan := findSpan(*spans, "execute_tool")
	require.NotNil(t, toolSpan)

	assertStringAttr(t, toolSpan.Attributes, "dash0.gen_ai.vcs.issue.url", "https://github.com/dash0hq/dash0-agent-plugin/issues/93")
}

func TestPRURLNotPresentWhenNoMatch(t *testing.T) {
	dataDir := t.TempDir()
	t.Setenv("CLAUDE_PLUGIN_DATA", dataDir)
	srv, spans, _ := collectingServer(t)
	t.Setenv("DASH0_OTLP_URL", srv.URL)

	feed(t, `{"hook_event_name":"SessionStart","session_id":"sess-nopr","model":"claude-sonnet-4-20250514"}`)
	feed(t, `{"hook_event_name":"UserPromptSubmit","session_id":"sess-nopr","prompt":"list files"}`)
	feed(t, `{"hook_event_name":"PreToolUse","session_id":"sess-nopr","tool_name":"Bash","tool_use_id":"tu-nopr"}`)
	feed(t, `{"hook_event_name":"PostToolUse","session_id":"sess-nopr","tool_name":"Bash","tool_use_id":"tu-nopr","tool_response":"file1.go\nfile2.go"}`)

	toolSpan := findSpan(*spans, "execute_tool")
	require.NotNil(t, toolSpan)

	// No PR URL attribute should be present.
	for _, a := range toolSpan.Attributes {
		assert.NotEqual(t, "dash0.gen_ai.vcs.pull_request.url", a.Key, "PR URL attribute should not be present when no PR URL in response")
	}
}

func assertAttrAbsent(t *testing.T, attrs []otlp.Attribute, key string) {
	t.Helper()
	for _, a := range attrs {
		if a.Key == key {
			t.Errorf("attribute %q should not be present", key)
			return
		}
	}
}

func TestLinesOfCodeSurvivesOmitIO(t *testing.T) {
	dataDir := t.TempDir()
	t.Setenv("CLAUDE_PLUGIN_DATA", dataDir)
	srv, spans, _ := collectingServer(t)
	t.Setenv("DASH0_OTLP_URL", srv.URL)
	t.Setenv("DASH0_OMIT_IO", "true")

	patchJSON := `{"structuredPatch":[{"filePath":"main.go","lines":[" ctx","- old1","- old2","+new1","+new2","+new3"," end"]}]}`

	feed(t, `{"hook_event_name":"SessionStart","session_id":"sess-loc","model":"claude-sonnet-4-20250514"}`)
	feed(t, `{"hook_event_name":"UserPromptSubmit","session_id":"sess-loc","prompt":"edit file"}`)
	feed(t, `{"hook_event_name":"PreToolUse","session_id":"sess-loc","tool_name":"Edit","tool_use_id":"tu-loc"}`)
	feed(t, fmt.Sprintf(`{"hook_event_name":"PostToolUse","session_id":"sess-loc","tool_name":"Edit","tool_use_id":"tu-loc","tool_input":"edit main.go","tool_response":%s}`, patchJSON))
	feed(t, `{"hook_event_name":"Stop","session_id":"sess-loc"}`)

	toolSpan := findSpan(*spans, "execute_tool")
	require.NotNil(t, toolSpan)

	// Tool I/O is redacted.
	assertStringAttr(t, toolSpan.Attributes, "gen_ai.tool.call.arguments", "<REDACTED>")
	assertStringAttr(t, toolSpan.Attributes, "gen_ai.tool.call.result", "<REDACTED>")

	// But lines-of-code counts survive as dedicated int attributes.
	assertIntAttr(t, toolSpan.Attributes, "dash0.gen_ai.code.lines_added", 3)
	assertIntAttr(t, toolSpan.Attributes, "dash0.gen_ai.code.lines_removed", 2)
}

func TestBashCommandFamilySurvivesOmitIO(t *testing.T) {
	dataDir := t.TempDir()
	t.Setenv("CLAUDE_PLUGIN_DATA", dataDir)
	srv, spans, _ := collectingServer(t)
	t.Setenv("DASH0_OTLP_URL", srv.URL)
	t.Setenv("DASH0_OMIT_IO", "true")

	feed(t, `{"hook_event_name":"SessionStart","session_id":"sess-bash","model":"claude-sonnet-4-20250514"}`)
	feed(t, `{"hook_event_name":"UserPromptSubmit","session_id":"sess-bash","prompt":"run git"}`)
	feed(t, `{"hook_event_name":"PreToolUse","session_id":"sess-bash","tool_name":"Bash","tool_use_id":"tu-bash"}`)
	feed(t, `{"hook_event_name":"PostToolUse","session_id":"sess-bash","tool_name":"Bash","tool_use_id":"tu-bash","tool_input":{"command":"git status","description":"Show status"},"tool_response":"on branch main"}`)

	toolSpan := findSpan(*spans, "execute_tool")
	require.NotNil(t, toolSpan)

	assertStringAttr(t, toolSpan.Attributes, "gen_ai.tool.call.arguments", "<REDACTED>")
	assertStringAttr(t, toolSpan.Attributes, "dash0.gen_ai.tool.bash.command_family", "git")
}

func TestSkillNameSurvivesOmitIO(t *testing.T) {
	dataDir := t.TempDir()
	t.Setenv("CLAUDE_PLUGIN_DATA", dataDir)
	srv, spans, _ := collectingServer(t)
	t.Setenv("DASH0_OTLP_URL", srv.URL)
	t.Setenv("DASH0_OMIT_IO", "true")

	feed(t, `{"hook_event_name":"SessionStart","session_id":"sess-skill","model":"claude-sonnet-4-20250514"}`)
	feed(t, `{"hook_event_name":"UserPromptSubmit","session_id":"sess-skill","prompt":"run skill"}`)
	feed(t, `{"hook_event_name":"PreToolUse","session_id":"sess-skill","tool_name":"Skill","tool_use_id":"tu-skill"}`)
	feed(t, `{"hook_event_name":"PostToolUse","session_id":"sess-skill","tool_name":"Skill","tool_use_id":"tu-skill","tool_input":{"skill":"translation-updater","args":"Add entries"},"tool_response":"done"}`)

	toolSpan := findSpan(*spans, "execute_tool")
	require.NotNil(t, toolSpan)

	assertStringAttr(t, toolSpan.Attributes, "gen_ai.tool.call.arguments", "<REDACTED>")
	assertStringAttr(t, toolSpan.Attributes, "dash0.gen_ai.tool.skill.name", "translation-updater")
}

func TestMCPServerExtracted(t *testing.T) {
	dataDir := t.TempDir()
	t.Setenv("CLAUDE_PLUGIN_DATA", dataDir)
	srv, spans, _ := collectingServer(t)
	t.Setenv("DASH0_OTLP_URL", srv.URL)

	feed(t, `{"hook_event_name":"SessionStart","session_id":"sess-mcp","model":"claude-sonnet-4-20250514"}`)
	feed(t, `{"hook_event_name":"UserPromptSubmit","session_id":"sess-mcp","prompt":"list issues"}`)
	feed(t, `{"hook_event_name":"PreToolUse","session_id":"sess-mcp","tool_name":"mcp__linear-server__list_issues","tool_use_id":"tu-mcp"}`)
	feed(t, `{"hook_event_name":"PostToolUse","session_id":"sess-mcp","tool_name":"mcp__linear-server__list_issues","tool_use_id":"tu-mcp","tool_response":"issues list"}`)

	toolSpan := findSpan(*spans, "execute_tool")
	require.NotNil(t, toolSpan)

	assertStringAttr(t, toolSpan.Attributes, "dash0.gen_ai.tool.mcp_server", "linear-server")
	assertStringAttr(t, toolSpan.Attributes, "gen_ai.tool.name", "list_issues")
	assert.Contains(t, toolSpan.Name, "execute_tool list_issues")
}

func TestMCPUUIDServerOmitted(t *testing.T) {
	dataDir := t.TempDir()
	t.Setenv("CLAUDE_PLUGIN_DATA", dataDir)
	srv, spans, _ := collectingServer(t)
	t.Setenv("DASH0_OTLP_URL", srv.URL)

	feed(t, `{"hook_event_name":"SessionStart","session_id":"sess-mcp-uuid","model":"claude-sonnet-4-20250514"}`)
	feed(t, `{"hook_event_name":"UserPromptSubmit","session_id":"sess-mcp-uuid","prompt":"read thread"}`)
	feed(t, `{"hook_event_name":"PreToolUse","session_id":"sess-mcp-uuid","tool_name":"mcp__1a66ca22-a5b4-4d91-b577-b64d7f7bc86c__slack_read_thread","tool_use_id":"tu-mcp-uuid"}`)
	feed(t, `{"hook_event_name":"PostToolUse","session_id":"sess-mcp-uuid","tool_name":"mcp__1a66ca22-a5b4-4d91-b577-b64d7f7bc86c__slack_read_thread","tool_use_id":"tu-mcp-uuid","tool_response":"thread content"}`)

	toolSpan := findSpan(*spans, "execute_tool")
	require.NotNil(t, toolSpan)

	assertAttrAbsent(t, toolSpan.Attributes, "dash0.gen_ai.tool.mcp_server")
	assertStringAttr(t, toolSpan.Attributes, "gen_ai.tool.name", "slack_read_thread")
	assert.Contains(t, toolSpan.Name, "execute_tool slack_read_thread")
}

func TestLinesOfCodeNotPresentOnNonEditTools(t *testing.T) {
	dataDir := t.TempDir()
	t.Setenv("CLAUDE_PLUGIN_DATA", dataDir)
	srv, spans, _ := collectingServer(t)
	t.Setenv("DASH0_OTLP_URL", srv.URL)

	feed(t, `{"hook_event_name":"SessionStart","session_id":"sess-noloc","model":"claude-sonnet-4-20250514"}`)
	feed(t, `{"hook_event_name":"UserPromptSubmit","session_id":"sess-noloc","prompt":"list files"}`)
	feed(t, `{"hook_event_name":"PreToolUse","session_id":"sess-noloc","tool_name":"Bash","tool_use_id":"tu-noloc"}`)
	feed(t, `{"hook_event_name":"PostToolUse","session_id":"sess-noloc","tool_name":"Bash","tool_use_id":"tu-noloc","tool_response":"file1.go\nfile2.go"}`)

	toolSpan := findSpan(*spans, "execute_tool")
	require.NotNil(t, toolSpan)

	for _, a := range toolSpan.Attributes {
		assert.NotEqual(t, "dash0.gen_ai.code.lines_added", a.Key, "lines_added should not be present on Bash tool spans")
		assert.NotEqual(t, "dash0.gen_ai.code.lines_removed", a.Key, "lines_removed should not be present on Bash tool spans")
	}
}

func TestNoMetadataOnUnrelatedTools(t *testing.T) {
	dataDir := t.TempDir()
	t.Setenv("CLAUDE_PLUGIN_DATA", dataDir)
	srv, spans, _ := collectingServer(t)
	t.Setenv("DASH0_OTLP_URL", srv.URL)

	feed(t, `{"hook_event_name":"SessionStart","session_id":"sess-read","model":"claude-sonnet-4-20250514"}`)
	feed(t, `{"hook_event_name":"UserPromptSubmit","session_id":"sess-read","prompt":"read file"}`)
	feed(t, `{"hook_event_name":"PreToolUse","session_id":"sess-read","tool_name":"Read","tool_use_id":"tu-read"}`)
	feed(t, `{"hook_event_name":"PostToolUse","session_id":"sess-read","tool_name":"Read","tool_use_id":"tu-read","tool_response":"file content"}`)

	toolSpan := findSpan(*spans, "execute_tool")
	require.NotNil(t, toolSpan)

	assertAttrAbsent(t, toolSpan.Attributes, "dash0.gen_ai.tool.bash.command_family")
	assertAttrAbsent(t, toolSpan.Attributes, "dash0.gen_ai.tool.skill.name")
	assertAttrAbsent(t, toolSpan.Attributes, "dash0.gen_ai.tool.mcp_server")
}

func TestSubagentStopEmitsChatSpanWithTokens(t *testing.T) {
	dataDir := t.TempDir()
	t.Setenv("CLAUDE_PLUGIN_DATA", dataDir)
	srv, spans, _ := collectingServer(t)
	t.Setenv("DASH0_OTLP_URL", srv.URL)

	// Create sub-agent transcript with token usage.
	agentTranscriptPath := filepath.Join(dataDir, "agent-sub1.jsonl")
	writeTranscript(t, agentTranscriptPath, []string{
		`{"type":"user","message":{"role":"user","content":[{"type":"text","text":"search for X"}]}}`,
		`{"type":"assistant","requestId":"req_sub_001","message":{"role":"assistant","content":[{"type":"text","text":"found it"}],"usage":{"input_tokens":500,"output_tokens":200,"cache_creation_input_tokens":1000,"cache_read_input_tokens":3000}}}`,
	})

	// Main agent session setup.
	feed(t, `{"hook_event_name":"SessionStart","session_id":"sess-substop","model":"claude-opus-4-7"}`)
	feed(t, `{"hook_event_name":"UserPromptSubmit","session_id":"sess-substop","prompt":"explore the code"}`)

	// Agent tool call spawns sub-agent.
	feed(t, `{"hook_event_name":"PreToolUse","session_id":"sess-substop","tool_name":"Agent","tool_use_id":"tu-agent"}`)
	feed(t, `{"hook_event_name":"PostToolUse","session_id":"sess-substop","tool_name":"Agent","tool_use_id":"tu-agent","tool_response":"{\"agentId\":\"sub1\",\"content\":[]}"}`)

	// Sub-agent does its work (tool calls inside sub-agent omitted for brevity).
	// SubagentStop fires when sub-agent finishes.
	feed(t, fmt.Sprintf(`{"hook_event_name":"SubagentStop","session_id":"sess-substop","agent_id":"sub1","agent_type":"Explore","agent_transcript_path":"%s"}`, agentTranscriptPath))

	// Main agent Stop.
	feed(t, `{"hook_event_name":"Stop","session_id":"sess-substop"}`)

	// Should have 3 spans: Agent tool + sub-agent chat + main chat.
	require.Len(t, *spans, 3, "expected Agent tool span + sub-agent chat span + main chat span")

	// Find the sub-agent chat span (invoke_agent).
	subagentSpan := findSpan(*spans, "invoke_agent")
	require.NotNil(t, subagentSpan, "SubagentStop should emit an invoke_agent span")

	// Sub-agent span should carry token usage from its transcript.
	assertIntAttr(t, subagentSpan.Attributes, "gen_ai.usage.input_tokens", 500)
	assertIntAttr(t, subagentSpan.Attributes, "gen_ai.usage.output_tokens", 200)
	assertIntAttr(t, subagentSpan.Attributes, "gen_ai.usage.cache_creation.input_tokens", 1000)
	assertIntAttr(t, subagentSpan.Attributes, "gen_ai.usage.cache_read.input_tokens", 3000)

	// Sub-agent span should be nested under the Agent tool span.
	expectedParent := otlp.SpanIDFromAgentID("sub1")
	assert.Equal(t, expectedParent, subagentSpan.ParentSpanID)

	// Sub-agent span should have the agent type attribute.
	assertStringAttr(t, subagentSpan.Attributes, "gen_ai.agent.name", "Explore")

	count := 0
	for _, a := range subagentSpan.Attributes {
		if a.Key == "gen_ai.agent.name" {
			count++
		}
	}
	assert.Equal(t, 1, count, "gen_ai.agent.name should appear exactly once")
}

func TestAgentNameDefaultsToClaudeCode(t *testing.T) {
	dataDir := t.TempDir()
	t.Setenv("CLAUDE_PLUGIN_DATA", dataDir)
	srv, spans, _ := collectingServer(t)
	t.Setenv("DASH0_OTLP_URL", srv.URL)
	// No AGENT_NAME / DASH0_AGENT_NAME configured.

	feed(t, `{"hook_event_name":"SessionStart","session_id":"sess-name","model":"claude-opus-4-8"}`)
	feed(t, `{"hook_event_name":"UserPromptSubmit","session_id":"sess-name","prompt":"hi"}`)
	feed(t, `{"hook_event_name":"Stop","session_id":"sess-name","model":"claude-opus-4-8"}`)

	// The main chat span should carry the default agent name.
	chatSpan := findSpan(*spans, "chat")
	require.NotNil(t, chatSpan)
	assertStringAttr(t, chatSpan.Attributes, "gen_ai.agent.name", "claude-code")
}

func TestAgentNameOverrideIsRespected(t *testing.T) {
	dataDir := t.TempDir()
	t.Setenv("CLAUDE_PLUGIN_DATA", dataDir)
	srv, spans, _ := collectingServer(t)
	t.Setenv("DASH0_OTLP_URL", srv.URL)
	t.Setenv("DASH0_AGENT_NAME", "my-agent")

	feed(t, `{"hook_event_name":"SessionStart","session_id":"sess-name2","model":"claude-opus-4-8"}`)
	feed(t, `{"hook_event_name":"UserPromptSubmit","session_id":"sess-name2","prompt":"hi"}`)
	feed(t, `{"hook_event_name":"Stop","session_id":"sess-name2","model":"claude-opus-4-8"}`)

	// A configured name must not be overridden by the default.
	chatSpan := findSpan(*spans, "chat")
	require.NotNil(t, chatSpan)
	assertStringAttr(t, chatSpan.Attributes, "gen_ai.agent.name", "my-agent")
}

func assertStringAttr(t *testing.T, attrs []otlp.Attribute, key, want string) {
	t.Helper()
	for _, a := range attrs {
		if a.Key == key {
			require.NotNil(t, a.Value.StringValue, "attribute %q should have string value", key)
			assert.Equal(t, want, *a.Value.StringValue)
			return
		}
	}
	t.Errorf("attribute %q not found", key)
}

func assertAttrContains(t *testing.T, attrs []otlp.Attribute, key, substr string) {
	t.Helper()
	for _, a := range attrs {
		if a.Key == key {
			require.NotNil(t, a.Value.StringValue, "attribute %q should have string value", key)
			assert.Contains(t, *a.Value.StringValue, substr, "attribute %q should contain %q", key, substr)
			return
		}
	}
	t.Errorf("attribute %q not found", key)
}
