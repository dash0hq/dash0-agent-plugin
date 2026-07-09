// SPDX-FileCopyrightText: Copyright 2026 Dash0 Inc.
// SPDX-License-Identifier: Apache-2.0

package pipeline

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/dash0hq/dash0-agent-plugin/internal/otlp"
)

// setup bundles the per-test scratch dir and OTLP config so individual
// tests stay short. dataDir is a t.TempDir(), so each test gets a fresh
// filesystem; the mock OTLP server (if any) is also per-test.
type setup struct {
	dataDir string
	cfg     otlp.Config
}

func newSetup(t *testing.T, otlpURL string) *setup {
	t.Helper()
	return &setup{
		dataDir: t.TempDir(),
		cfg: otlp.Config{
			OTLPUrl:   otlpURL,
			AuthToken: "test-token",
			AgentName: "test",
		},
	}
}

// feed drives Process for the given event with a fresh timestamp and
// fails the test on any error returned (telemetry-export failures are
// swallowed by Process itself, so errors here indicate fatal local
// issues — filesystem / data-dir problems).
func (s *setup) feed(t *testing.T, event map[string]any) Result {
	t.Helper()
	res, err := Process(event, s.cfg, s.dataDir, time.Now().UTC())
	require.NoError(t, err)
	return res
}

func (s *setup) sessionDir(sessionID string) string {
	return filepath.Join(s.dataDir, sessionID)
}

// mockOTLPServer captures spans posted to /v1/traces so tests can assert
// on what the pipeline emitted. Empty ResourceSpans requests (e.g. the
// SessionStart connectivity check) contribute nothing to the slice.
func mockOTLPServer(t *testing.T) (url string, spans *[]otlp.Span, mu *sync.Mutex) {
	t.Helper()
	var captured []otlp.Span
	var lock sync.Mutex
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/traces" {
			body, _ := io.ReadAll(r.Body)
			var req otlp.ExportTracesRequest
			if err := json.Unmarshal(body, &req); err == nil {
				lock.Lock()
				for _, rs := range req.ResourceSpans {
					for _, ss := range rs.ScopeSpans {
						captured = append(captured, ss.Spans...)
					}
				}
				lock.Unlock()
			}
		}
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)
	return srv.URL, &captured, &lock
}

// unreachableURL returns a URL whose port is guaranteed not to accept
// connections — we spin up an httptest server then immediately close it.
// Used for the "connectivity check failed" branch of SessionStart.
func unreachableURL(t *testing.T) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	addr := srv.URL
	srv.Close()
	return addr
}

// hasStringAttr returns true when attrs contains key=value as a string attribute.
func hasStringAttr(attrs []otlp.Attribute, key, value string) bool {
	for _, a := range attrs {
		if a.Key == key && a.Value.StringValue != nil && *a.Value.StringValue == value {
			return true
		}
	}
	return false
}

//  1. SessionStart records the model into the per-session trace context so
//     later turns can pick it up. No span is emitted yet.
func TestProcess_SessionStart_SavesModelToContext(t *testing.T) {
	url, spans, mu := mockOTLPServer(t)
	s := newSetup(t, url)

	s.feed(t, map[string]any{
		"hook_event_name": "SessionStart",
		"session_id":      "sess-1",
		"model":           "claude-opus-4-7",
	})

	ctx, err := otlp.LoadTraceContext(s.sessionDir("sess-1"))
	require.NoError(t, err)
	require.NotNil(t, ctx)
	assert.Equal(t, "sess-1", ctx.SessionID)
	assert.Equal(t, "claude-opus-4-7", ctx.Model)
	assert.Empty(t, ctx.TraceID, "trace_id is created at UserPromptSubmit, not SessionStart")
	assert.Empty(t, ctx.SpanID)

	mu.Lock()
	assert.Empty(t, *spans, "SessionStart does not emit a span")
	mu.Unlock()
}

//  2. A missing session_id must not crash: Process generates a random ID,
//     creates a session directory under that name, and stamps a
//     dash0.warning attribute on the event in events.jsonl.
func TestProcess_MissingSessionID_FallsBackToRandom(t *testing.T) {
	s := newSetup(t, "")

	s.feed(t, map[string]any{
		"hook_event_name": "SessionStart",
		"model":           "opus",
	})

	entries, err := os.ReadDir(s.dataDir)
	require.NoError(t, err)
	require.Len(t, entries, 1, "exactly one session dir should be created")
	sessionID := entries[0].Name()
	require.NotEmpty(t, sessionID)

	data, err := os.ReadFile(filepath.Join(s.dataDir, sessionID, "events.jsonl"))
	require.NoError(t, err)
	var ev map[string]any
	require.NoError(t, json.Unmarshal(bytes.TrimSpace(data), &ev))
	assert.Equal(t, "session_id was missing from hook payload", ev["dash0.warning"])
}

//  3. UserPromptSubmit creates a fresh trace_id and chat_span_id for the
//     turn and preserves the model previously set at SessionStart.
func TestProcess_UserPromptSubmit_GeneratesFreshTraceID(t *testing.T) {
	s := newSetup(t, "")

	s.feed(t, map[string]any{"hook_event_name": "SessionStart", "session_id": "sess-1", "model": "opus"})
	s.feed(t, map[string]any{"hook_event_name": "UserPromptSubmit", "session_id": "sess-1", "prompt": "hi"})

	ctx, err := otlp.LoadTraceContext(s.sessionDir("sess-1"))
	require.NoError(t, err)
	require.NotNil(t, ctx)
	assert.NotEmpty(t, ctx.TraceID, "UserPromptSubmit should mint a trace_id")
	assert.NotEmpty(t, ctx.SpanID, "UserPromptSubmit should mint a chat_span_id")
	assert.Equal(t, "opus", ctx.Model, "model from SessionStart should carry forward")
}

//  4. A UserPromptSubmit whose agent_id is set belongs to a sub-agent and
//     must NOT clobber the main turn's trace context — sub-agent activity
//     needs to nest under the in-flight main turn.
func TestProcess_UserPromptSubmitWithAgentID_PreservesContext(t *testing.T) {
	s := newSetup(t, "")

	s.feed(t, map[string]any{"hook_event_name": "SessionStart", "session_id": "sess-1", "model": "opus"})
	s.feed(t, map[string]any{"hook_event_name": "UserPromptSubmit", "session_id": "sess-1", "prompt": "main"})

	before, err := otlp.LoadTraceContext(s.sessionDir("sess-1"))
	require.NoError(t, err)
	require.NotNil(t, before)

	s.feed(t, map[string]any{
		"hook_event_name": "UserPromptSubmit",
		"session_id":      "sess-1",
		"prompt":          "subagent",
		"agent_id":        "subagent-1",
	})

	after, err := otlp.LoadTraceContext(s.sessionDir("sess-1"))
	require.NoError(t, err)
	require.NotNil(t, after)
	assert.Equal(t, before.TraceID, after.TraceID, "subagent prompt must not regenerate the main trace_id")
	assert.Equal(t, before.SpanID, after.SpanID, "subagent prompt must not overwrite the chat span")
}

//  5. PostToolUse emits a tool span parented under the chat span, with
//     GenAI conventional attributes populated from the event payload.
func TestProcess_PostToolUse_EmitsToolSpan(t *testing.T) {
	url, spans, mu := mockOTLPServer(t)
	s := newSetup(t, url)

	s.feed(t, map[string]any{"hook_event_name": "SessionStart", "session_id": "sess-1", "model": "opus"})
	s.feed(t, map[string]any{"hook_event_name": "UserPromptSubmit", "session_id": "sess-1", "prompt": "do thing"})

	ctx, _ := otlp.LoadTraceContext(s.sessionDir("sess-1"))
	require.NotNil(t, ctx)

	s.feed(t, map[string]any{
		"hook_event_name": "PostToolUse",
		"session_id":      "sess-1",
		"tool_name":       "Bash",
		"tool_use_id":     "tu1",
		"tool_input":      "ls",
		"tool_response":   "file.txt",
	})

	mu.Lock()
	defer mu.Unlock()
	require.Len(t, *spans, 1)
	span := (*spans)[0]
	assert.Equal(t, "execute_tool Bash", span.Name)
	assert.Equal(t, ctx.TraceID, span.TraceID)
	assert.Equal(t, ctx.SpanID, span.ParentSpanID, "tool span parents under the chat span")
	assert.NotEqual(t, ctx.SpanID, span.SpanID, "tool span has its own span_id")
	assert.Equal(t, otlp.StatusCodeUnset, span.Status.Code)

	assert.True(t, hasStringAttr(span.Attributes, "gen_ai.tool.name", "Bash"))
	assert.True(t, hasStringAttr(span.Attributes, "gen_ai.tool.call.id", "tu1"))
	assert.True(t, hasStringAttr(span.Attributes, "gen_ai.conversation.id", "sess-1"))
}

//  6. PostToolUseFailure emits a span with status.code = Error and the
//     error message surfaced as both status.message and the exception.message
//     semantic attribute.
func TestProcess_PostToolUseFailure_EmitsErrorStatus(t *testing.T) {
	url, spans, mu := mockOTLPServer(t)
	s := newSetup(t, url)

	s.feed(t, map[string]any{"hook_event_name": "SessionStart", "session_id": "sess-1", "model": "opus"})
	s.feed(t, map[string]any{"hook_event_name": "UserPromptSubmit", "session_id": "sess-1", "prompt": "x"})
	s.feed(t, map[string]any{
		"hook_event_name": "PostToolUseFailure",
		"session_id":      "sess-1",
		"tool_name":       "Bash",
		"tool_use_id":     "tu1",
		"error":           "command not found",
	})

	mu.Lock()
	defer mu.Unlock()
	require.Len(t, *spans, 1)
	span := (*spans)[0]
	assert.Equal(t, otlp.StatusCodeError, span.Status.Code)
	assert.Equal(t, "command not found", span.Status.Message)
	assert.True(t, hasStringAttr(span.Attributes, "exception.message", "command not found"))
}

//  7. Stop emits the chat span and clears the trace context so a later
//     SessionEnd does not emit a duplicate fallback.
func TestProcess_Stop_EmitsChatSpanAndClearsContext(t *testing.T) {
	url, spans, mu := mockOTLPServer(t)
	s := newSetup(t, url)

	s.feed(t, map[string]any{"hook_event_name": "SessionStart", "session_id": "sess-1", "model": "opus"})
	s.feed(t, map[string]any{"hook_event_name": "UserPromptSubmit", "session_id": "sess-1", "prompt": "hi"})
	s.feed(t, map[string]any{"hook_event_name": "Stop", "session_id": "sess-1"})

	mu.Lock()
	require.Len(t, *spans, 1)
	span := (*spans)[0]
	mu.Unlock()
	assert.Contains(t, span.Name, "chat", "chat span name starts with 'chat'")
	assert.Empty(t, span.ParentSpanID, "chat span is the root of the turn")

	ctx, err := otlp.LoadTraceContext(s.sessionDir("sess-1"))
	require.NoError(t, err)
	assert.Nil(t, ctx, "Stop must clear trace context so SessionEnd does not duplicate")
}

//  8. If the user interrupts (Ctrl+C) so Stop never fires, SessionEnd must
//     emit a fallback chat span with error status so any orphan tool
//     spans still have a parent in the trace.
func TestProcess_SessionEnd_EmitsFallbackWhenContextLingers(t *testing.T) {
	url, spans, mu := mockOTLPServer(t)
	s := newSetup(t, url)

	s.feed(t, map[string]any{"hook_event_name": "SessionStart", "session_id": "sess-1", "model": "opus"})
	s.feed(t, map[string]any{"hook_event_name": "UserPromptSubmit", "session_id": "sess-1", "prompt": "x"})
	s.feed(t, map[string]any{"hook_event_name": "SessionEnd", "session_id": "sess-1"})

	mu.Lock()
	defer mu.Unlock()
	require.Len(t, *spans, 1)
	span := (*spans)[0]
	assert.Equal(t, otlp.StatusCodeError, span.Status.Code)
	assert.Equal(t, "session ended before completion", span.Status.Message)
}

//  9. SessionEnd removes the per-session scratch directory so events.jsonl,
//     trace_context.json, and any source-specific stash files don't leak.
func TestProcess_SessionEnd_CleansUpSessionDir(t *testing.T) {
	s := newSetup(t, "")

	s.feed(t, map[string]any{"hook_event_name": "SessionStart", "session_id": "sess-1", "model": "opus"})
	sessionDir := s.sessionDir("sess-1")
	require.DirExists(t, sessionDir)

	s.feed(t, map[string]any{"hook_event_name": "SessionEnd", "session_id": "sess-1"})
	assert.NoDirExists(t, sessionDir)
}

//  10. SessionStart surfaces one of three user-visible status messages
//     depending on OTLP URL state and connectivity result. This is the
//     plugin's main observability into its own health.
func TestProcess_SessionStart_ConnectivityMessages(t *testing.T) {
	t.Run("not active when OTLP URL is empty", func(t *testing.T) {
		s := newSetup(t, "")
		res := s.feed(t, map[string]any{"hook_event_name": "SessionStart", "session_id": "sess", "model": "opus"})
		require.Len(t, res.Messages, 1)
		assert.Contains(t, res.Messages[0].UserText, "telemetry is not active")
	})

	t.Run("connectivity check failed when endpoint unreachable", func(t *testing.T) {
		s := newSetup(t, unreachableURL(t))
		res := s.feed(t, map[string]any{"hook_event_name": "SessionStart", "session_id": "sess", "model": "opus"})
		require.Len(t, res.Messages, 1)
		assert.Contains(t, res.Messages[0].UserText, "connectivity check failed")
	})

	t.Run("connected when endpoint accepts the empty trace request", func(t *testing.T) {
		url, _, _ := mockOTLPServer(t)
		s := newSetup(t, url)
		res := s.feed(t, map[string]any{"hook_event_name": "SessionStart", "session_id": "sess", "model": "opus"})
		require.Len(t, res.Messages, 1)
		assert.Equal(t, "dash0: connected", res.Messages[0].UserText)
	})
}

//  11. Subsequent SessionStart fires (resume, compact, clear) within the same
//     session are no-ops: no connectivity check, no messages, no trace context overwrite.
func TestProcess_SessionStart_SubsequentFireIsNoOp(t *testing.T) {
	url, _, _ := mockOTLPServer(t)
	s := newSetup(t, url)

	res := s.feed(t, map[string]any{"hook_event_name": "SessionStart", "session_id": "sess-1", "model": "opus"})
	require.Len(t, res.Messages, 1)
	assert.Equal(t, "dash0: connected", res.Messages[0].UserText)

	ctx, err := otlp.LoadTraceContext(s.sessionDir("sess-1"))
	require.NoError(t, err)
	assert.Equal(t, "opus", ctx.Model)

	res = s.feed(t, map[string]any{"hook_event_name": "SessionStart", "session_id": "sess-1", "model": "sonnet"})
	assert.Empty(t, res.Messages, "subsequent SessionStart should not produce messages")

	ctx, err = otlp.LoadTraceContext(s.sessionDir("sess-1"))
	require.NoError(t, err)
	assert.Equal(t, "opus", ctx.Model, "trace context model must not be overwritten by re-fire")
}

// 12. A re-fired SessionStart still logs the event to filelog.
func TestProcess_SessionStart_ReFireStillLogsEvent(t *testing.T) {
	url, _, _ := mockOTLPServer(t)
	s := newSetup(t, url)

	s.feed(t, map[string]any{"hook_event_name": "SessionStart", "session_id": "sess-1", "model": "opus"})
	s.feed(t, map[string]any{"hook_event_name": "SessionStart", "session_id": "sess-1", "model": "sonnet", "source": "resume"})

	data, err := os.ReadFile(filepath.Join(s.sessionDir("sess-1"), "events.jsonl"))
	require.NoError(t, err)
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	assert.Len(t, lines, 2, "both SessionStart events should be logged")
}

// 13. After SessionEnd cleans up sessionDir, a new SessionStart re-initializes.
func TestProcess_SessionStart_ReInitializesAfterSessionEnd(t *testing.T) {
	url, _, _ := mockOTLPServer(t)
	s := newSetup(t, url)

	s.feed(t, map[string]any{"hook_event_name": "SessionStart", "session_id": "sess-1", "model": "opus"})
	s.feed(t, map[string]any{"hook_event_name": "SessionEnd", "session_id": "sess-1"})

	res := s.feed(t, map[string]any{"hook_event_name": "SessionStart", "session_id": "sess-1", "model": "sonnet"})
	require.Len(t, res.Messages, 1)
	assert.Equal(t, "dash0: connected", res.Messages[0].UserText)

	ctx, err := otlp.LoadTraceContext(s.sessionDir("sess-1"))
	require.NoError(t, err)
	assert.Equal(t, "sonnet", ctx.Model)
}
