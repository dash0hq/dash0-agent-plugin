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
	"strconv"
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

func (s *setup) feedAt(t *testing.T, event map[string]any, ts time.Time) Result {
	t.Helper()
	res, err := Process(event, s.cfg, s.dataDir, ts)
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

// SessionStart records the model into the per-session trace context so
// later turns can pick it up. No span is emitted yet.
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

// A missing session_id must not crash: Process generates a random ID,
// creates a session directory under that name, and stamps a
// dash0.warning attribute on the event in events.jsonl.
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

// A session_id containing path-traversal characters (e.g. "../etc") is
//
// rejected: Process substitutes a random safe ID, logs a warning, and no
// file is created outside dataDir. This guards MkdirAll, filelog writes,
// and RemoveAll which all use sessionID as a directory name under dataDir.
func TestProcess_InvalidSessionID_FallsBackToRandom(t *testing.T) {
	s := newSetup(t, "")

	for _, badID := range []string{"../escape", "a/b", "a.b", "has space", "with\x00null"} {
		s.feed(t, map[string]any{
			"hook_event_name": "SessionStart",
			"session_id":      badID,
			"model":           "opus",
		})
	}

	entries, err := os.ReadDir(s.dataDir)
	require.NoError(t, err)

	// Each rejected ID must have produced a safe replacement dir, and nothing
	// must have been written at the raw unsafe path.
	require.Len(t, entries, 5, "one session dir per call, all with safe names")
	for _, e := range entries {
		assert.Regexp(t, `^[A-Za-z0-9_-]+$`, e.Name(), "session dir name must be filename-safe")
	}

	// Confirm the warning attribute is set in the logged event.
	for _, e := range entries {
		data, err := os.ReadFile(filepath.Join(s.dataDir, e.Name(), "events.jsonl"))
		require.NoError(t, err)
		var ev map[string]any
		require.NoError(t, json.Unmarshal(bytes.TrimSpace(data), &ev))
		assert.Equal(t, "session_id from hook payload was not a safe path segment", ev["dash0.warning"])
	}

	// The parent directory must contain exactly the dataDir itself — no file
	// escaped above it via path traversal.
	parentEntries, err := os.ReadDir(filepath.Dir(s.dataDir))
	require.NoError(t, err)
	names := make([]string, 0, len(parentEntries))
	for _, e := range parentEntries {
		names = append(names, e.Name())
	}
	assert.Contains(t, names, filepath.Base(s.dataDir), "dataDir itself must exist")
	assert.Len(t, parentEntries, 1, "nothing written outside dataDir")
}

// UserPromptSubmit creates a fresh trace_id and chat_span_id for the
// turn and preserves the model previously set at SessionStart.
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

// Reordered startup: some runtimes (e.g. Copilot) deliver UserPromptSubmit
// BEFORE SessionStart. SessionStart must merge into the existing context, not
// blank the trace_id/chat_span_id the prompt already established.
func TestProcess_SessionStartAfterUserPromptSubmit_PreservesTurnContext(t *testing.T) {
	s := newSetup(t, "")

	// UserPromptSubmit arrives first and mints the turn's IDs.
	s.feed(t, map[string]any{"hook_event_name": "UserPromptSubmit", "session_id": "sess-1", "prompt": "hi"})
	before, err := otlp.LoadTraceContext(s.sessionDir("sess-1"))
	require.NoError(t, err)
	require.NotNil(t, before)
	require.NotEmpty(t, before.TraceID)
	require.NotEmpty(t, before.SpanID)

	// SessionStart arrives late — it must preserve those IDs and only fill in
	// the fields it owns (SessionID, Model).
	s.feed(t, map[string]any{"hook_event_name": "SessionStart", "session_id": "sess-1", "model": "opus"})
	after, err := otlp.LoadTraceContext(s.sessionDir("sess-1"))
	require.NoError(t, err)
	require.NotNil(t, after)
	assert.Equal(t, before.TraceID, after.TraceID, "late SessionStart must not blank the turn's trace_id")
	assert.Equal(t, before.SpanID, after.SpanID, "late SessionStart must not blank the chat span")
	assert.Equal(t, "opus", after.Model, "SessionStart's model should merge into the existing context")
	assert.Equal(t, "sess-1", after.SessionID)
}

// A Stop whose loaded trace context has a blank TraceID (a context that somehow
// lost its IDs) must NOT emit a chat span with an empty trace id — sendLLMTrace
// refuses it, the same way the SessionEnd fallback guards on ctx.TraceID != "".
// This is what lets the Copilot entrypoint defer such a turn rather than drop it.
func TestProcess_Stop_BlankTraceIDEmitsNoSpan(t *testing.T) {
	url, spans, mu := mockOTLPServer(t)
	s := newSetup(t, url)

	sessionDir := s.sessionDir("sess-1")
	require.NoError(t, os.MkdirAll(sessionDir, 0o755))
	require.NoError(t, otlp.SaveTraceContext(otlp.TraceContext{SessionID: "sess-1"}, sessionDir))

	s.feed(t, map[string]any{
		"hook_event_name": "Stop",
		"session_id":      "sess-1",
	})

	mu.Lock()
	defer mu.Unlock()
	assert.Empty(t, *spans, "a Stop with a blank-TraceID context must emit no span")
}

// A UserPromptSubmit whose agent_id is set belongs to a sub-agent and
// must NOT clobber the main turn's trace context — sub-agent activity
// needs to nest under the in-flight main turn.
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

// PostToolUse emits a tool span parented under the chat span, with
// GenAI conventional attributes populated from the event payload.
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

// PostToolUseFailure emits a span with status.code = Error and the
// error message surfaced as both status.message and the exception.message
// semantic attribute.
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

// Stop emits the chat span and clears the trace context so a later
// SessionEnd does not emit a duplicate fallback.
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

// Agents that inject gen_ai.usage.* upstream (Codex, Cursor) must not go through
// the Claude-format transcript read at Stop — it would clobber their counts and
// burn the flush-wait budget parsing a non-Claude rollout. When usage is already
// present, the transcript is left untouched.
func TestProcess_Stop_PreservesUpstreamInjectedUsage(t *testing.T) {
	url, spans, mu := mockOTLPServer(t)
	s := newSetup(t, url)

	// A readable Claude-format transcript carrying DIFFERENT usage; it must be
	// ignored because the event already has usage from the upstream normalizer.
	tp := filepath.Join(t.TempDir(), "transcript.jsonl")
	require.NoError(t, os.WriteFile(tp, []byte(
		`{"type":"user","message":{"role":"user","content":"hi"}}`+"\n"+
			`{"type":"assistant","requestId":"r1","message":{"role":"assistant","stop_reason":"end_turn","content":[{"type":"text","text":"x"}],"usage":{"input_tokens":5,"output_tokens":6}}}`+"\n"), 0o644))

	s.feed(t, map[string]any{"hook_event_name": "SessionStart", "session_id": "sess-1", "model": "opus"})
	s.feed(t, map[string]any{"hook_event_name": "UserPromptSubmit", "session_id": "sess-1", "prompt": "hi"})
	s.feed(t, map[string]any{
		"hook_event_name":            "Stop",
		"session_id":                 "sess-1",
		"transcript_path":            tp,
		"gen_ai.usage.input_tokens":  int64(999),
		"gen_ai.usage.output_tokens": int64(111),
	})

	mu.Lock()
	defer mu.Unlock()
	require.Len(t, *spans, 1)
	assert.Equal(t, "999", intAttr(t, (*spans)[0], "gen_ai.usage.input_tokens"),
		"upstream-injected usage preserved; Claude transcript not read")
	assert.Equal(t, "111", intAttr(t, (*spans)[0], "gen_ai.usage.output_tokens"))
}

// intAttr returns the intValue of the named span attribute, failing the test if
// it is absent or not an integer attribute.
func intAttr(t *testing.T, span otlp.Span, key string) string {
	t.Helper()
	for _, a := range span.Attributes {
		if a.Key == key {
			require.NotNil(t, a.Value.IntValue, "attribute %s is not an int", key)
			return *a.Value.IntValue
		}
	}
	t.Fatalf("attribute %s not found on span", key)
	return ""
}

// If the user interrupts (Ctrl+C) so Stop never fires, SessionEnd must
// emit a fallback chat span with error status so any orphan tool
// spans still have a parent in the trace.
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

// SessionEnd removes the per-session scratch directory so events.jsonl,
// trace_context.json, and any source-specific stash files don't leak.
func TestProcess_SessionEnd_CleansUpSessionDir(t *testing.T) {
	s := newSetup(t, "")

	s.feed(t, map[string]any{"hook_event_name": "SessionStart", "session_id": "sess-1", "model": "opus"})
	sessionDir := s.sessionDir("sess-1")
	require.DirExists(t, sessionDir)

	s.feed(t, map[string]any{"hook_event_name": "SessionEnd", "session_id": "sess-1"})
	assert.NoDirExists(t, sessionDir)
}

// SessionStart surfaces one of three user-visible status messages
// depending on OTLP URL state and connectivity result. This is the
// plugin's main observability into its own health.
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
		// The pipeline (not the source entrypoint) annotates the success message
		// with the plugin version so every coding agent surfaces it. A session
		// link is appended only for recognized Dash0 hosts (see sessionurl); the
		// mock server host isn't one, so no link is expected here.
		assert.Contains(t, res.Messages[0].UserText, "dash0: connected (v")
	})
}

// Subsequent SessionStart fires (resume, compact, clear) within the same
// session are no-ops: no connectivity check, no messages, no trace context overwrite.
func TestProcess_SessionStart_SubsequentFireIsNoOp(t *testing.T) {
	url, _, _ := mockOTLPServer(t)
	s := newSetup(t, url)

	res := s.feed(t, map[string]any{"hook_event_name": "SessionStart", "session_id": "sess-1", "model": "opus"})
	require.Len(t, res.Messages, 1)
	assert.Contains(t, res.Messages[0].UserText, "dash0: connected")

	ctx, err := otlp.LoadTraceContext(s.sessionDir("sess-1"))
	require.NoError(t, err)
	assert.Equal(t, "opus", ctx.Model)

	res = s.feed(t, map[string]any{"hook_event_name": "SessionStart", "session_id": "sess-1", "model": "sonnet"})
	assert.Empty(t, res.Messages, "subsequent SessionStart should not produce messages")

	ctx, err = otlp.LoadTraceContext(s.sessionDir("sess-1"))
	require.NoError(t, err)
	assert.Equal(t, "opus", ctx.Model, "trace context model must not be overwritten by re-fire")
}

// A re-fired SessionStart still logs the event to filelog.
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

// After SessionEnd cleans up sessionDir, a new SessionStart re-initializes.
func TestProcess_SessionStart_ReInitializesAfterSessionEnd(t *testing.T) {
	url, _, _ := mockOTLPServer(t)
	s := newSetup(t, url)

	s.feed(t, map[string]any{"hook_event_name": "SessionStart", "session_id": "sess-1", "model": "opus"})
	s.feed(t, map[string]any{"hook_event_name": "SessionEnd", "session_id": "sess-1"})

	res := s.feed(t, map[string]any{"hook_event_name": "SessionStart", "session_id": "sess-1", "model": "sonnet"})
	require.Len(t, res.Messages, 1)
	assert.Contains(t, res.Messages[0].UserText, "dash0: connected")

	ctx, err := otlp.LoadTraceContext(s.sessionDir("sess-1"))
	require.NoError(t, err)
	assert.Equal(t, "sonnet", ctx.Model)
}

// writeAgentTranscript creates a minimal subagent transcript (prompt + one
// assistant call with usage) and returns its path.
func writeAgentTranscript(t *testing.T, inputTokens, outputTokens int) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "agent-transcript.jsonl")
	lines := []string{
		`{"type":"user","message":{"role":"user","content":"agent prompt"}}`,
		`{"type":"assistant","requestId":"req_agent_1","message":{"role":"assistant","model":"claude-haiku-4-5-20251001","content":[{"type":"text","text":"done"}],"usage":{"input_tokens":` +
			strconv.Itoa(inputTokens) + `,"output_tokens":` + strconv.Itoa(outputTokens) + `,"cache_creation_input_tokens":0,"cache_read_input_tokens":0}}}`,
	}
	require.NoError(t, os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o644))
	return path
}

// The observed real-world ordering: a subagent's SubagentStop arrives
// AFTER the turn's Stop has already cleared the session trace context.
// The snapshot taken at SubagentStart must keep the subagent span — and its
// token usage — attached to the spawning turn's trace instead of dropping it.
// The span's start time must reflect the SubagentStart hook time, not the
// SubagentStop time (which may be in a later turn).
func TestProcess_SubagentStopAfterStop_UsesSnapshotContext(t *testing.T) {
	url, spans, mu := mockOTLPServer(t)
	s := newSetup(t, url)
	agentTranscript := writeAgentTranscript(t, 2393, 2172)

	base := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)

	s.feedAt(t, map[string]any{"hook_event_name": "SessionStart", "session_id": "sess-1", "model": "opus"}, base)
	s.feedAt(t, map[string]any{"hook_event_name": "UserPromptSubmit", "session_id": "sess-1", "prompt": "do it"}, base.Add(time.Second))

	turnCtx, err := otlp.LoadTraceContext(s.sessionDir("sess-1"))
	require.NoError(t, err)
	require.NotNil(t, turnCtx)

	subagentStartTime := base.Add(2 * time.Second)
	s.feedAt(t, map[string]any{"hook_event_name": "SubagentStart", "session_id": "sess-1", "agent_id": "agent1"}, subagentStartTime)
	s.feedAt(t, map[string]any{"hook_event_name": "Stop", "session_id": "sess-1"}, base.Add(3*time.Second))

	// Session context is gone — before the fix this dropped the span.
	cleared, err := otlp.LoadTraceContext(s.sessionDir("sess-1"))
	require.NoError(t, err)
	require.Nil(t, cleared)

	subagentStopTime := base.Add(4 * time.Second)
	s.feedAt(t, map[string]any{
		"hook_event_name":       "SubagentStop",
		"session_id":            "sess-1",
		"agent_id":              "agent1",
		"agent_transcript_path": agentTranscript,
	}, subagentStopTime)

	mu.Lock()
	defer mu.Unlock()
	require.Len(t, *spans, 2, "chat span from Stop AND subagent span from SubagentStop")
	sub := (*spans)[1]
	assert.Equal(t, turnCtx.TraceID, sub.TraceID, "subagent span must join the spawning turn's trace")
	assert.Equal(t, otlp.SpanIDFromAgentID("agent1"), sub.ParentSpanID, "parented under the Agent tool span")
	assert.Equal(t, "2393", intAttr(t, sub, "gen_ai.usage.input_tokens"))
	assert.Equal(t, "2172", intAttr(t, sub, "gen_ai.usage.output_tokens"), "subagent token usage must survive the Stop ordering")
	// Start time must be anchored to SubagentStart, not SubagentStop.
	assert.Equal(t, strconv.FormatInt(subagentStartTime.UnixNano(), 10), sub.StartTimeUnixNano, "subagent span start must reflect SubagentStart hook time")
	assert.Equal(t, strconv.FormatInt(subagentStopTime.UnixNano(), 10), sub.EndTimeUnixNano, "subagent span end reflects SubagentStop hook time")
}

// The cache-creation TTL split has no OTel semconv attribute, so it is emitted
// under dash0.gen_ai.usage.* — but only when the transcript actually carried the
// breakdown. This locks the exact attribute keys and the gating.
func TestProcess_EmitsCacheCreationTTLBreakdown(t *testing.T) {
	url, spans, mu := mockOTLPServer(t)
	s := newSetup(t, url)

	path := filepath.Join(t.TempDir(), "agent-transcript.jsonl")
	lines := []string{
		`{"type":"user","message":{"role":"user","content":"agent prompt"}}`,
		`{"type":"assistant","requestId":"req_agent_1","message":{"role":"assistant","model":"claude-haiku-4-5-20251001","content":[{"type":"text","text":"done"}],"usage":{"input_tokens":100,"output_tokens":50,"cache_creation_input_tokens":300,"cache_read_input_tokens":0,"cache_creation":{"ephemeral_5m_input_tokens":50,"ephemeral_1h_input_tokens":250}}}}`,
	}
	require.NoError(t, os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o644))

	s.feed(t, map[string]any{"hook_event_name": "SessionStart", "session_id": "sess-1", "model": "opus"})
	s.feed(t, map[string]any{"hook_event_name": "UserPromptSubmit", "session_id": "sess-1", "prompt": "do it"})
	s.feed(t, map[string]any{"hook_event_name": "SubagentStart", "session_id": "sess-1", "agent_id": "agent1"})
	s.feed(t, map[string]any{
		"hook_event_name":       "SubagentStop",
		"session_id":            "sess-1",
		"agent_id":              "agent1",
		"agent_transcript_path": path,
	})

	mu.Lock()
	defer mu.Unlock()
	require.Len(t, *spans, 1)
	sub := (*spans)[0]
	assert.Equal(t, "300", intAttr(t, sub, "gen_ai.usage.cache_creation.input_tokens"))
	assert.Equal(t, "50", intAttr(t, sub, "dash0.gen_ai.usage.cache_creation.ephemeral_5m.input_tokens"))
	assert.Equal(t, "250", intAttr(t, sub, "dash0.gen_ai.usage.cache_creation.ephemeral_1h.input_tokens"))
}

// Fast mode and non-default service tiers bill off their own rate tables, so a
// turn that mixes them is only priceable with the per-mode breakdown alongside
// the totals. This locks the attribute key, the JSON shape, and that the
// entries sum back to the flat counters.
func TestProcess_EmitsUsageBreakdown(t *testing.T) {
	url, spans, mu := mockOTLPServer(t)
	s := newSetup(t, url)

	path := filepath.Join(t.TempDir(), "agent-transcript.jsonl")
	lines := []string{
		`{"type":"user","message":{"role":"user","content":"agent prompt"}}`,
		`{"type":"assistant","requestId":"req_agent_1","message":{"role":"assistant","model":"claude-opus-5","content":[{"type":"text","text":"a"}],"usage":{"input_tokens":10,"output_tokens":5,"cache_creation_input_tokens":200,"cache_read_input_tokens":1000,"cache_creation":{"ephemeral_5m_input_tokens":50,"ephemeral_1h_input_tokens":150},"speed":"fast"}}}`,
		`{"type":"assistant","requestId":"req_agent_2","message":{"role":"assistant","model":"claude-opus-5","content":[{"type":"text","text":"b"}],"usage":{"input_tokens":20,"output_tokens":7,"cache_creation_input_tokens":100,"cache_read_input_tokens":2000,"cache_creation":{"ephemeral_5m_input_tokens":100,"ephemeral_1h_input_tokens":0},"speed":"standard"}}}`,
	}
	require.NoError(t, os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o644))

	s.feed(t, map[string]any{"hook_event_name": "SessionStart", "session_id": "sess-1", "model": "opus"})
	s.feed(t, map[string]any{"hook_event_name": "UserPromptSubmit", "session_id": "sess-1", "prompt": "do it"})
	s.feed(t, map[string]any{"hook_event_name": "SubagentStart", "session_id": "sess-1", "agent_id": "agent1"})
	s.feed(t, map[string]any{
		"hook_event_name":       "SubagentStop",
		"session_id":            "sess-1",
		"agent_id":              "agent1",
		"agent_transcript_path": path,
	})

	mu.Lock()
	defer mu.Unlock()
	require.Len(t, *spans, 1)
	sub := (*spans)[0]
	// Totals cover both calls and are untouched by the breakdown.
	assert.Equal(t, "30", intAttr(t, sub, "gen_ai.usage.input_tokens"))
	assert.Equal(t, "12", intAttr(t, sub, "gen_ai.usage.output_tokens"))

	// The breakdown carries each mode's share, defaults omitted from the entry.
	assert.JSONEq(t, `[
		{"speed":"fast","input_tokens":10,"output_tokens":5,
		 "cache_creation_input_tokens":200,"cache_read_input_tokens":1000,
		 "cache_creation_5m_input_tokens":50,"cache_creation_1h_input_tokens":150},
		{"input_tokens":20,"output_tokens":7,
		 "cache_creation_input_tokens":100,"cache_read_input_tokens":2000,
		 "cache_creation_5m_input_tokens":100,"cache_creation_1h_input_tokens":0}
	]`, strAttr(t, sub, "dash0.gen_ai.usage.breakdown"))
}

// The common turn bills entirely at default rates, so it must carry no
// breakdown at all — its absence is what says "price the totals as standard".
func TestProcess_OmitsUsageBreakdownAtDefaultRates(t *testing.T) {
	url, spans, mu := mockOTLPServer(t)
	s := newSetup(t, url)

	path := filepath.Join(t.TempDir(), "agent-transcript.jsonl")
	lines := []string{
		`{"type":"user","message":{"role":"user","content":"agent prompt"}}`,
		`{"type":"assistant","requestId":"req_agent_1","message":{"role":"assistant","model":"claude-opus-5","content":[{"type":"text","text":"a"}],"usage":{"input_tokens":10,"output_tokens":5,"speed":"standard"}}}`,
	}
	require.NoError(t, os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o644))

	s.feed(t, map[string]any{"hook_event_name": "SessionStart", "session_id": "sess-1", "model": "opus"})
	s.feed(t, map[string]any{"hook_event_name": "UserPromptSubmit", "session_id": "sess-1", "prompt": "do it"})
	s.feed(t, map[string]any{"hook_event_name": "SubagentStart", "session_id": "sess-1", "agent_id": "agent1"})
	s.feed(t, map[string]any{
		"hook_event_name":       "SubagentStop",
		"session_id":            "sess-1",
		"agent_id":              "agent1",
		"agent_transcript_path": path,
	})

	mu.Lock()
	defer mu.Unlock()
	require.Len(t, *spans, 1)
	for _, a := range (*spans)[0].Attributes {
		assert.NotEqual(t, "dash0.gen_ai.usage.breakdown", a.Key)
	}
}

// strAttr returns the stringValue of the named span attribute, failing the test
// if it is absent or not a string attribute.
func strAttr(t *testing.T, span otlp.Span, key string) string {
	t.Helper()
	for _, a := range span.Attributes {
		if a.Key == key {
			require.NotNil(t, a.Value.StringValue, "attribute %s is not a string", key)
			return *a.Value.StringValue
		}
	}
	t.Fatalf("attribute %s not found on span", key)
	return ""
}

// A SubagentStop that straggles past the NEXT turn's UserPromptSubmit must
// still attach to the turn that spawned it, not to the new turn's trace.
func TestProcess_SubagentStopAfterNextPrompt_KeepsSpawningTrace(t *testing.T) {
	url, spans, mu := mockOTLPServer(t)
	s := newSetup(t, url)
	agentTranscript := writeAgentTranscript(t, 100, 50)

	s.feed(t, map[string]any{"hook_event_name": "SessionStart", "session_id": "sess-1", "model": "opus"})
	s.feed(t, map[string]any{"hook_event_name": "UserPromptSubmit", "session_id": "sess-1", "prompt": "turn 1"})

	turn1Ctx, err := otlp.LoadTraceContext(s.sessionDir("sess-1"))
	require.NoError(t, err)
	require.NotNil(t, turn1Ctx)

	s.feed(t, map[string]any{"hook_event_name": "SubagentStart", "session_id": "sess-1", "agent_id": "agent1"})
	s.feed(t, map[string]any{"hook_event_name": "Stop", "session_id": "sess-1"})
	s.feed(t, map[string]any{"hook_event_name": "UserPromptSubmit", "session_id": "sess-1", "prompt": "turn 2"})

	turn2Ctx, err := otlp.LoadTraceContext(s.sessionDir("sess-1"))
	require.NoError(t, err)
	require.NotNil(t, turn2Ctx)
	require.NotEqual(t, turn1Ctx.TraceID, turn2Ctx.TraceID)

	s.feed(t, map[string]any{
		"hook_event_name":       "SubagentStop",
		"session_id":            "sess-1",
		"agent_id":              "agent1",
		"agent_transcript_path": agentTranscript,
	})

	mu.Lock()
	defer mu.Unlock()
	require.Len(t, *spans, 2)
	sub := (*spans)[1]
	assert.Equal(t, turn1Ctx.TraceID, sub.TraceID, "late subagent span belongs to turn 1, not turn 2")
}

// Without a SubagentStart snapshot (e.g. plugin installed mid-session),
// SubagentStop falls back to the live session context as before.
func TestProcess_SubagentStopWithoutSnapshot_FallsBackToSessionContext(t *testing.T) {
	url, spans, mu := mockOTLPServer(t)
	s := newSetup(t, url)
	agentTranscript := writeAgentTranscript(t, 100, 50)

	s.feed(t, map[string]any{"hook_event_name": "SessionStart", "session_id": "sess-1", "model": "opus"})
	s.feed(t, map[string]any{"hook_event_name": "UserPromptSubmit", "session_id": "sess-1", "prompt": "do it"})

	turnCtx, err := otlp.LoadTraceContext(s.sessionDir("sess-1"))
	require.NoError(t, err)
	require.NotNil(t, turnCtx)

	// No SubagentStart — straight to SubagentStop while the turn is live.
	s.feed(t, map[string]any{
		"hook_event_name":       "SubagentStop",
		"session_id":            "sess-1",
		"agent_id":              "agent1",
		"agent_transcript_path": agentTranscript,
	})

	mu.Lock()
	defer mu.Unlock()
	require.Len(t, *spans, 1)
	assert.Equal(t, turnCtx.TraceID, (*spans)[0].TraceID)
}

// SubagentStop consumes its snapshot: the per-agent file is removed so a
// long-lived session does not accumulate stale agent contexts.
func TestProcess_SubagentStop_CleansUpSnapshot(t *testing.T) {
	url, _, _ := mockOTLPServer(t)
	s := newSetup(t, url)

	s.feed(t, map[string]any{"hook_event_name": "SessionStart", "session_id": "sess-1", "model": "opus"})
	s.feed(t, map[string]any{"hook_event_name": "UserPromptSubmit", "session_id": "sess-1", "prompt": "do it"})
	s.feed(t, map[string]any{"hook_event_name": "SubagentStart", "session_id": "sess-1", "agent_id": "agent1"})

	snap, err := otlp.LoadAgentTraceContext(s.sessionDir("sess-1"), "agent1")
	require.NoError(t, err)
	require.NotNil(t, snap, "SubagentStart must persist a snapshot")

	s.feed(t, map[string]any{"hook_event_name": "SubagentStop", "session_id": "sess-1", "agent_id": "agent1"})

	snap, err = otlp.LoadAgentTraceContext(s.sessionDir("sess-1"), "agent1")
	require.NoError(t, err)
	assert.Nil(t, snap, "snapshot must be removed after SubagentStop")
}
