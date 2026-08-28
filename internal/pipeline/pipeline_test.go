// SPDX-FileCopyrightText: Copyright 2026 Dash0 Inc.
// SPDX-License-Identifier: Apache-2.0

package pipeline

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"testing/iotest"
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

// 2b. A session_id containing path-traversal characters (e.g. "../etc") is
//
//	rejected: Process substitutes a random safe ID, logs a warning, and no
//	file is created outside dataDir. This guards MkdirAll, filelog writes,
//	and RemoveAll which all use sessionID as a directory name under dataDir.
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
	s.feed(t, map[string]any{"hook_event_name": "Stop", "session_id": "sess-1", "transcript_path": claudeTranscript(t)})

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

// Claude Code billing mode rides on the chat span. The config is redirected via
// CLAUDE_CONFIG_DIR and the auth variables are pinned empty, so the test reads a
// fixture rather than the developer's real account and cannot be perturbed by a
// key that happens to be exported on the machine running it.
func TestProcess_Stop_EmitsClaudeBillingMode(t *testing.T) {
	pinClaudeAuthEnv(t, `{"claudeMaxTier":"not_max","oauthAccount":{"billingType":"stripe_subscription","seatTier":"team_standard"}}`)

	url, spans, mu := mockOTLPServer(t)
	s := newSetup(t, url)
	s.cfg.HarnessName = "claude-code"

	s.feed(t, map[string]any{"hook_event_name": "SessionStart", "session_id": "sess-1", "model": "opus"})
	s.feed(t, map[string]any{"hook_event_name": "UserPromptSubmit", "session_id": "sess-1", "prompt": "hi"})
	s.feed(t, map[string]any{"hook_event_name": "Stop", "session_id": "sess-1", "transcript_path": claudeTranscript(t)})

	mu.Lock()
	defer mu.Unlock()
	require.Len(t, *spans, 1)
	assert.True(t, hasStringAttr((*spans)[0].Attributes, "dash0.gen_ai.billing_mode", "subscription"))
	assert.True(t, hasStringAttr((*spans)[0].Attributes, "dash0.gen_ai.plan_type", "team_standard"))
}

// The environment decides, not the config file: a subscription account on disk
// plus Bedrock in the environment bills per token at an AWS rate. Emitting
// "subscription" here would tell the customer their real spend is not real spend.
func TestProcess_Stop_ClaudeBedrockOverridesSubscriptionConfig(t *testing.T) {
	pinClaudeAuthEnv(t, `{"oauthAccount":{"billingType":"stripe_subscription","seatTier":"team_standard"}}`)
	t.Setenv("CLAUDE_CODE_USE_BEDROCK", "1")

	url, spans, mu := mockOTLPServer(t)
	s := newSetup(t, url)
	s.cfg.HarnessName = "claude-code"

	s.feed(t, map[string]any{"hook_event_name": "SessionStart", "session_id": "sess-1", "model": "opus"})
	s.feed(t, map[string]any{"hook_event_name": "UserPromptSubmit", "session_id": "sess-1", "prompt": "hi"})
	s.feed(t, map[string]any{"hook_event_name": "Stop", "session_id": "sess-1", "transcript_path": claudeTranscript(t)})

	mu.Lock()
	defer mu.Unlock()
	require.Len(t, *spans, 1)
	assert.True(t, hasStringAttr((*spans)[0].Attributes, "dash0.gen_ai.billing_mode", "metered_external"))
	assert.True(t, hasStringAttr((*spans)[0].Attributes, "dash0.gen_ai.billing_provider", "bedrock"))
	assert.False(t, hasStringAttr((*spans)[0].Attributes, "dash0.gen_ai.billing_mode", "subscription"),
		"the config must not win over the environment")
}

// This read is Claude-specific but sits in the shared LLM-span path, so it must be
// harness-guarded: Codex emits its own billing mode from the rollout, and stamping
// Claude's answer onto a Codex span would silently overwrite it with the wrong
// account's state.
func TestProcess_Stop_BillingModeNotEmittedForOtherHarnesses(t *testing.T) {
	// A perfectly readable Claude config is present; the harness is what excludes it.
	pinClaudeAuthEnv(t, `{"oauthAccount":{"billingType":"stripe_subscription","seatTier":"team_standard"}}`)

	url, spans, mu := mockOTLPServer(t)
	s := newSetup(t, url)
	s.cfg.HarnessName = "codex"

	s.feed(t, map[string]any{"hook_event_name": "SessionStart", "session_id": "sess-1", "model": "gpt-5.5"})
	s.feed(t, map[string]any{"hook_event_name": "UserPromptSubmit", "session_id": "sess-1", "prompt": "hi"})
	s.feed(t, map[string]any{"hook_event_name": "Stop", "session_id": "sess-1", "transcript_path": claudeTranscript(t)})

	mu.Lock()
	defer mu.Unlock()
	require.Len(t, *spans, 1)
	for _, a := range (*spans)[0].Attributes {
		assert.NotEqual(t, "dash0.gen_ai.billing_mode", a.Key, "Claude's read leaked onto a %s span", "codex")
		assert.NotEqual(t, "dash0.gen_ai.plan_type", a.Key)
	}
}

// Billing mode qualifies a cost figure, so with no usage there is nothing to
// qualify and the attributes stay off the span. Keeps a turn that reported no
// tokens from carrying a label about a number it does not have.
func TestProcess_Stop_NoUsageEmitsNoBillingMode(t *testing.T) {
	pinClaudeAuthEnv(t, `{"oauthAccount":{"billingType":"stripe_subscription","seatTier":"team_standard"}}`)

	url, spans, mu := mockOTLPServer(t)
	s := newSetup(t, url)
	s.cfg.HarnessName = "claude-code"

	s.feed(t, map[string]any{"hook_event_name": "SessionStart", "session_id": "sess-1", "model": "opus"})
	s.feed(t, map[string]any{"hook_event_name": "UserPromptSubmit", "session_id": "sess-1", "prompt": "hi"})
	// No transcript, so no token usage — an interrupted turn.
	s.feed(t, map[string]any{"hook_event_name": "Stop", "session_id": "sess-1"})

	mu.Lock()
	defer mu.Unlock()
	require.Len(t, *spans, 1)
	for _, a := range (*spans)[0].Attributes {
		assert.NotEqual(t, "dash0.gen_ai.billing_mode", a.Key, "no cost figure, so nothing to qualify")
		assert.NotEqual(t, "dash0.gen_ai.plan_type", a.Key)
	}
}

// claudeTranscript writes a minimal Claude-format transcript with one complete
// turn, so a Stop event yields token usage. Billing mode is only emitted
// alongside a cost figure, so a transcript is what makes these tests realistic —
// a real Stop always has one.
func claudeTranscript(t *testing.T) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "transcript.jsonl")
	require.NoError(t, os.WriteFile(p, []byte(
		`{"type":"user","message":{"role":"user","content":"hi"}}`+"\n"+
			`{"type":"assistant","requestId":"r1","message":{"role":"assistant","stop_reason":"end_turn","content":[{"type":"text","text":"x"}],"usage":{"input_tokens":5,"output_tokens":6}}}`+"\n"), 0o644))
	return p
}

// pinClaudeAuthEnv points the Claude config lookup at a fixture and pins every
// auth variable empty, so these tests neither read the developer's real account
// nor inherit an exported key from the host.
func pinClaudeAuthEnv(t *testing.T, configJSON string) {
	t.Helper()
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, ".claude.json"), []byte(configJSON), 0o600))
	t.Setenv("CLAUDE_CONFIG_DIR", dir)
	// Every tier, not just the ones these cases exercise: a variable left
	// unpinned is one the host can set, and a higher-ranked credential silently
	// wins. The list must track the precedence table in DEVELOPMENT.md.
	for _, key := range []string{
		"CLAUDE_CODE_USE_BEDROCK", "CLAUDE_CODE_USE_VERTEX", "CLAUDE_CODE_USE_FOUNDRY",
		"ANTHROPIC_AUTH_TOKEN", "ANTHROPIC_API_KEY", "CLAUDE_CODE_OAUTH_TOKEN",
		"ANTHROPIC_PROFILE", "ANTHROPIC_FEDERATION_RULE_ID", "ANTHROPIC_ORGANIZATION_ID",
	} {
		t.Setenv(key, "")
	}
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
		// The missing-team warning rides along on the connected path and has its own
		// test; set a team so this one stays about connectivity.
		s.cfg.TeamName = "platform"
		res := s.feed(t, map[string]any{"hook_event_name": "SessionStart", "session_id": "sess", "model": "opus"})
		require.Len(t, res.Messages, 1)
		// The pipeline (not the source entrypoint) annotates the success message
		// with the plugin version so every coding agent surfaces it. A session
		// link is appended only for recognized Dash0 hosts (see sessionurl); the
		// mock server host isn't one, so no link is expected here.
		assert.Contains(t, res.Messages[0].UserText, "dash0: connected (v")
	})
}

// Telemetry can be live while the option that makes it attributable is unset.
// Users don't know TEAM_NAME exists, so a connected session names the gap and
// tells the calling agent to offer setup.
func TestProcess_SessionStart_NoTeamWarning(t *testing.T) {
	sessionStart := func() map[string]any {
		return map[string]any{"hook_event_name": "SessionStart", "session_id": "sess", "model": "opus"}
	}

	// warningOf returns the missing-team message from a Result, or fails if there
	// is none.
	warningOf := func(t *testing.T, res Result) Message {
		t.Helper()
		require.Len(t, res.Messages, 2, "expected the connected message plus the missing-team warning")
		assert.Contains(t, res.Messages[0].UserText, "dash0: connected")
		return res.Messages[1]
	}

	// The text names the attribute rather than where to set it: every coding agent
	// configures the plugin differently, so the entrypoints add that part.
	t.Run("names the missing attribute", func(t *testing.T) {
		url, _, _ := mockOTLPServer(t)
		s := newSetup(t, url)

		msg := warningOf(t, s.feed(t, sessionStart()))
		assert.Equal(t, "dash0: no team configured — spans carry no dash0.team.name.", msg.UserText)
	})

	// An unset dataset is not a gap: it means the backend picks the dataset named
	// "default", which is what the installers write explicitly.
	t.Run("ignores an unset dataset", func(t *testing.T) {
		url, _, _ := mockOTLPServer(t)
		s := newSetup(t, url)
		s.cfg.TeamName = "platform"
		s.cfg.Dataset = ""

		res := s.feed(t, sessionStart())
		require.Len(t, res.Messages, 1)
		assert.Contains(t, res.Messages[0].UserText, "dash0: connected")
	})

	// The model context is the point of the warning: the agent, not the user, is
	// expected to start the conversation about finishing setup.
	t.Run("tells the agent to offer the configure skill", func(t *testing.T) {
		url, _, _ := mockOTLPServer(t)
		s := newSetup(t, url)

		msg := warningOf(t, s.feed(t, sessionStart()))
		assert.Contains(t, msg.ModelContext, "dash0-configure")
		assert.Contains(t, msg.ModelContext, "TEAM_NAME")
	})

	t.Run("silent when the team name is set", func(t *testing.T) {
		url, _, _ := mockOTLPServer(t)
		s := newSetup(t, url)
		s.cfg.TeamName = "platform"

		res := s.feed(t, sessionStart())
		require.Len(t, res.Messages, 1)
		assert.Contains(t, res.Messages[0].UserText, "dash0: connected")
	})

	// Telemetry being off is the louder problem and already has its own message.
	// Adding the warning there would bury it.
	t.Run("silent when telemetry is not active", func(t *testing.T) {
		s := newSetup(t, "")

		res := s.feed(t, sessionStart())
		require.Len(t, res.Messages, 1)
		assert.Contains(t, res.Messages[0].UserText, "telemetry is not active")
	})

	t.Run("silent when the connectivity check failed", func(t *testing.T) {
		s := newSetup(t, unreachableURL(t))

		res := s.feed(t, sessionStart())
		require.Len(t, res.Messages, 1)
		assert.Contains(t, res.Messages[0].UserText, "connectivity check failed")
	})

	// Resume, compact, and clear re-fire SessionStart. Warning on each would turn
	// a reminder into a nag within one session.
	t.Run("silent on a re-fired SessionStart", func(t *testing.T) {
		url, _, _ := mockOTLPServer(t)
		s := newSetup(t, url)

		warningOf(t, s.feed(t, sessionStart()))
		res := s.feed(t, sessionStart())
		assert.Empty(t, res.Messages)
	})
}

// Subsequent SessionStart fires (resume, compact, clear) within the same
// session are no-ops: no connectivity check, no messages, no trace context overwrite.
func TestProcess_SessionStart_SubsequentFireIsNoOp(t *testing.T) {
	url, _, _ := mockOTLPServer(t)
	s := newSetup(t, url)
	s.cfg.TeamName = "platform"

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
	s.cfg.TeamName = "platform"

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

// 14. The observed real-world ordering: a subagent's SubagentStop arrives
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

// 15. A SubagentStop that straggles past the NEXT turn's UserPromptSubmit must
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
	s.feed(t, map[string]any{"hook_event_name": "Stop", "session_id": "sess-1", "transcript_path": claudeTranscript(t)})
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

// 16. Without a SubagentStart snapshot (e.g. plugin installed mid-session),
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

// 17. SubagentStop consumes its snapshot: the per-agent file is removed so a
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

// If persisting the consumed marker fails, the snapshot must remain available.
// Otherwise a late tool hook can fall back to a newer turn's live context and
// attach the span to the wrong trace.
func TestProcess_SubagentStopMarkerFailure_RetainsSnapshot(t *testing.T) {
	url, spans, mu := mockOTLPServer(t)
	s := newSetup(t, url)

	s.feed(t, map[string]any{"hook_event_name": "SessionStart", "session_id": "sess-1"})
	s.feed(t, map[string]any{"hook_event_name": "UserPromptSubmit", "session_id": "sess-1", "prompt": "delegate it"})

	spawningCtx, err := otlp.LoadTraceContext(s.sessionDir("sess-1"))
	require.NoError(t, err)
	require.NotNil(t, spawningCtx)

	s.feed(t, map[string]any{"hook_event_name": "SubagentStart", "session_id": "sess-1", "agent_id": "agent1"})
	markerTemp := filepath.Join(s.sessionDir("sess-1"),
		"agent_trace_context_agent1.consumed.tmp."+strconv.Itoa(os.Getpid()))
	require.NoError(t, os.Mkdir(markerTemp, 0o755), "block only the marker's atomic temp write")

	s.feed(t, map[string]any{"hook_event_name": "SubagentStop", "session_id": "sess-1", "agent_id": "agent1"})
	s.feed(t, map[string]any{"hook_event_name": "UserPromptSubmit", "session_id": "sess-1", "prompt": "next turn"})
	s.feed(t, map[string]any{
		"hook_event_name": "PostToolUse",
		"session_id":      "sess-1",
		"agent_id":        "agent1",
		"tool_name":       "Bash",
		"tool_use_id":     "tu-late",
	})

	mu.Lock()
	defer mu.Unlock()
	require.Len(t, *spans, 2, "SubagentStop and the late tool hook both emit spans")
	assert.Equal(t, spawningCtx.TraceID, (*spans)[1].TraceID,
		"the retained snapshot prevents fallback to the next turn")
}

func TestReadEvent(t *testing.T) {
	t.Run("decodes a hook payload", func(t *testing.T) {
		event, err := ReadEvent(strings.NewReader(
			`{"hook_event_name":"PreToolUse","session_id":"s1","tool_name":"Bash"}`))
		require.NoError(t, err)
		assert.Equal(t, "PreToolUse", event["hook_event_name"])
		assert.Equal(t, "s1", event["session_id"])
		assert.Equal(t, "Bash", event["tool_name"])
	})

	t.Run("rejects malformed JSON", func(t *testing.T) {
		_, err := ReadEvent(strings.NewReader(`{"hook_event_name":`))
		require.Error(t, err)
		assert.Contains(t, err.Error(), "parsing JSON from stdin")
	})

	// A PowerShell pipeline into a native command writes whatever encoding the
	// console carries, and Encoding.UTF8 emits a BOM, so a Windows harness can
	// prepend one. json.Unmarshal reports it as `invalid character 'ï'`.
	t.Run("skips a UTF-8 byte-order mark", func(t *testing.T) {
		event, err := ReadEvent(strings.NewReader(
			"\ufeff" + `{"hook_event_name":"SessionStart","session_id":"s1"}`))
		require.NoError(t, err)
		assert.Equal(t, "SessionStart", event["hook_event_name"])
	})

	// Empty stdin is an error, not an empty event: json.Unmarshal rejects "".
	// Every entrypoint turns that into a stderr line and exit 0, so a hook fired
	// with no payload is logged rather than silently treated as a real event.
	t.Run("empty input is an error", func(t *testing.T) {
		_, err := ReadEvent(strings.NewReader(""))
		require.Error(t, err)
		assert.Contains(t, err.Error(), "parsing JSON from stdin")
	})

	// "null" is valid JSON but unmarshals to a nil map, which Process cannot use:
	// it writes the timestamp into the event and would panic. Rejecting it here
	// keeps every ReadEvent success usable by the caller.
	t.Run("literal null is rejected", func(t *testing.T) {
		event, err := ReadEvent(strings.NewReader("null"))
		require.Error(t, err)
		assert.Nil(t, event)
		assert.Contains(t, err.Error(), "JSON null")
	})

	// An empty object is a different case: usable, just without any fields.
	t.Run("an empty object is accepted", func(t *testing.T) {
		event, err := ReadEvent(strings.NewReader("{}"))
		require.NoError(t, err)
		assert.NotNil(t, event)
		assert.Empty(t, event)
	})

	t.Run("a JSON array is rejected: events are objects", func(t *testing.T) {
		_, err := ReadEvent(strings.NewReader(`[{"hook_event_name":"Stop"}]`))
		require.Error(t, err)
		assert.Contains(t, err.Error(), "parsing JSON from stdin")
	})

	t.Run("surfaces a read failure", func(t *testing.T) {
		_, err := ReadEvent(iotest.ErrReader(errors.New("pipe broke")))
		require.Error(t, err)
		assert.Contains(t, err.Error(), "reading stdin")
		assert.Contains(t, err.Error(), "pipe broke")
	})
}

func TestChdirToEventCwd(t *testing.T) {
	t.Run("changes to the event cwd", func(t *testing.T) {
		original, err := filepath.Abs(".")
		require.NoError(t, err)

		// Create the directory BEFORE registering the chdir back. Cleanups run in
		// reverse, so this order returns to the original directory first and then
		// deletes the temp one — Windows refuses to remove a directory that is any
		// process's working directory.
		target := t.TempDir()
		t.Cleanup(func() { require.NoError(t, os.Chdir(original)) })
		ChdirToEventCwd(map[string]any{"cwd": target})

		got, err := filepath.Abs(".")
		require.NoError(t, err)
		// macOS temp dirs are symlinked (/var -> /private/var), so compare
		// resolved paths.
		wantResolved, err := filepath.EvalSymlinks(target)
		require.NoError(t, err)
		gotResolved, err := filepath.EvalSymlinks(got)
		require.NoError(t, err)
		assert.Equal(t, wantResolved, gotResolved)
	})

	t.Run("ignores a missing, blank, or non-string cwd", func(t *testing.T) {
		before, err := filepath.Abs(".")
		require.NoError(t, err)

		for _, event := range []map[string]any{
			{},
			{"cwd": ""},
			{"cwd": 42},
			{"cwd": nil},
		} {
			ChdirToEventCwd(event)
			after, err := filepath.Abs(".")
			require.NoError(t, err)
			assert.Equal(t, before, after, "event %v should not have moved us", event)
		}
	})

	t.Run("ignores a cwd that does not exist", func(t *testing.T) {
		before, err := filepath.Abs(".")
		require.NoError(t, err)

		ChdirToEventCwd(map[string]any{"cwd": filepath.Join(t.TempDir(), "nope")})

		after, err := filepath.Abs(".")
		require.NoError(t, err)
		assert.Equal(t, before, after)
	})
}

// A sub-agent's tool call routinely lands after the spawning turn's Stop has
// cleared the session trace context, because the Task tool returns as soon as
// the agent is launched. Before the fix every such span was dropped, which
// meant the more work a session delegated, the less of it was observable.
func TestProcess_PostToolUseAfterStop_UsesAgentSnapshotContext(t *testing.T) {
	url, spans, mu := mockOTLPServer(t)
	s := newSetup(t, url)

	base := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	s.feedAt(t, map[string]any{"hook_event_name": "SessionStart", "session_id": "sess-1"}, base)
	s.feedAt(t, map[string]any{"hook_event_name": "UserPromptSubmit", "session_id": "sess-1", "prompt": "delegate it"}, base.Add(time.Second))

	turnCtx, err := otlp.LoadTraceContext(s.sessionDir("sess-1"))
	require.NoError(t, err)
	require.NotNil(t, turnCtx)

	s.feedAt(t, map[string]any{"hook_event_name": "SubagentStart", "session_id": "sess-1", "agent_id": "agent1"}, base.Add(2*time.Second))
	s.feedAt(t, map[string]any{"hook_event_name": "Stop", "session_id": "sess-1"}, base.Add(3*time.Second))

	cleared, err := otlp.LoadTraceContext(s.sessionDir("sess-1"))
	require.NoError(t, err)
	require.Nil(t, cleared, "Stop clears the session context — the snapshot is the only way back")

	s.feedAt(t, map[string]any{
		"hook_event_name": "PostToolUse",
		"session_id":      "sess-1",
		"agent_id":        "agent1",
		"agent_type":      "general-purpose",
		"tool_name":       "Bash",
		"tool_use_id":     "tu1",
		"duration_ms":     float64(1000),
	}, base.Add(10*time.Second))

	mu.Lock()
	defer mu.Unlock()
	require.Len(t, *spans, 2, "chat span from Stop AND the sub-agent's tool span")
	tool := (*spans)[1]
	assert.Equal(t, "execute_tool Bash", tool.Name)
	assert.Equal(t, turnCtx.TraceID, tool.TraceID, "tool span must join the spawning turn's trace")
	assert.Equal(t, otlp.SpanIDFromAgentID("agent1"), tool.ParentSpanID, "parented under the Agent tool span, not the chat span")
}

// Once SubagentStop has cleared the snapshot there is no context left to attach
// to, so the span is dropped rather than attached to whatever turn is current.
// This pins that the fix did not turn into a fallback onto the next turn.
func TestProcess_PostToolUseAfterSubagentStop_IsNotReparented(t *testing.T) {
	url, spans, mu := mockOTLPServer(t)
	s := newSetup(t, url)

	s.feed(t, map[string]any{"hook_event_name": "SessionStart", "session_id": "sess-1"})
	s.feed(t, map[string]any{"hook_event_name": "UserPromptSubmit", "session_id": "sess-1", "prompt": "delegate it"})
	s.feed(t, map[string]any{"hook_event_name": "SubagentStart", "session_id": "sess-1", "agent_id": "agent1"})
	s.feed(t, map[string]any{"hook_event_name": "Stop", "session_id": "sess-1"})
	s.feed(t, map[string]any{"hook_event_name": "SubagentStop", "session_id": "sess-1", "agent_id": "agent1"})
	s.feed(t, map[string]any{"hook_event_name": "UserPromptSubmit", "session_id": "sess-1", "prompt": "next turn"})
	// Agent IDs are invocation-unique. Even if an unexpected reuse publishes a
	// replacement snapshot, the consumed tombstone keeps a stale hook fail-closed.
	s.feed(t, map[string]any{"hook_event_name": "SubagentStart", "session_id": "sess-1", "agent_id": "agent1"})

	before := len(*spans)
	s.feed(t, map[string]any{
		"hook_event_name": "PostToolUse",
		"session_id":      "sess-1",
		"agent_id":        "agent1",
		"tool_name":       "Bash",
		"tool_use_id":     "tu-late",
	})

	mu.Lock()
	defer mu.Unlock()
	assert.Len(t, *spans, before, "no context, no span — and no guessing at a parent")
}

// The model back-fill on a tool span used to race the transcript flush, so it
// landed on some of a session's tool spans and not others. Resolving it once and
// remembering it on the trace context is what makes it deterministic.
func TestProcess_PostToolUse_RemembersResolvedModel(t *testing.T) {
	url, spans, mu := mockOTLPServer(t)
	s := newSetup(t, url)

	transcriptPath := writeAgentTranscript(t, 10, 10)

	s.feed(t, map[string]any{"hook_event_name": "SessionStart", "session_id": "sess-1"})
	s.feed(t, map[string]any{"hook_event_name": "UserPromptSubmit", "session_id": "sess-1", "prompt": "do thing"})
	s.feed(t, map[string]any{
		"hook_event_name": "PostToolUse",
		"session_id":      "sess-1",
		"tool_name":       "Bash",
		"tool_use_id":     "tu1",
		"transcript_path": transcriptPath,
	})

	ctx, err := otlp.LoadTraceContext(s.sessionDir("sess-1"))
	require.NoError(t, err)
	require.NotNil(t, ctx)
	cachedModel, err := otlp.LoadToolModel(s.sessionDir("sess-1"), ctx.TraceID, "")
	require.NoError(t, err)
	assert.Equal(t, "claude-haiku-4-5-20251001", cachedModel, "resolved once, then remembered")
	assert.Empty(t, ctx.Model, "turn model stays out of the session-level Model field")

	// Second call cannot read the transcript at all. It must still report the
	// model, which is the whole point of remembering it.
	require.NoError(t, os.Remove(transcriptPath))
	s.feed(t, map[string]any{
		"hook_event_name": "PostToolUse",
		"session_id":      "sess-1",
		"tool_name":       "Read",
		"tool_use_id":     "tu2",
		"transcript_path": transcriptPath,
	})

	mu.Lock()
	defer mu.Unlock()
	require.Len(t, *spans, 2)
	for _, span := range *spans {
		assert.True(t, hasStringAttr(span.Attributes, "gen_ai.request.model", "claude-haiku-4-5-20251001"),
			"every tool span of the session carries the same model: %s", span.Name)
	}
}

func TestRememberModelDoesNotCacheAnInactiveTrace(t *testing.T) {
	dir := t.TempDir()
	inactiveTraceID := "aaaabbbbccccddddaaaabbbbccccdddd"
	active := otlp.TraceContext{
		TraceID:   "11112222333344441111222233334444",
		SpanID:    "1111222233334444",
		SessionID: "sess-1",
	}
	require.NoError(t, otlp.SaveTraceContext(active, dir))

	rememberModel(dir, inactiveTraceID, "", "claude-opus-4-8")

	cached, err := otlp.LoadToolModel(dir, inactiveTraceID, "")
	require.NoError(t, err)
	assert.Empty(t, cached, "a late hook must not leave a cache for a completed trace")

	current, err := otlp.LoadTraceContext(dir)
	require.NoError(t, err)
	require.NotNil(t, current)
	assert.Equal(t, active, *current, "remembering a model never rewrites the active trace context")
}

func TestProcess_PostToolUse_CachesModelsPerTraceActor(t *testing.T) {
	for _, tc := range []struct {
		name       string
		agentFirst bool
	}{
		{name: "parent first"},
		{name: "subagent first", agentFirst: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			url, spans, mu := mockOTLPServer(t)
			s := newSetup(t, url)
			dir := t.TempDir()
			parentTranscript := writeModelTranscript(t, dir, "parent.jsonl", "claude-sonnet-4-6")
			writeSubagentTranscript(t, dir, "sess-1", "agent1", "claude-opus-4-8")

			s.feed(t, map[string]any{"hook_event_name": "SessionStart", "session_id": "sess-1"})
			s.feed(t, map[string]any{"hook_event_name": "UserPromptSubmit", "session_id": "sess-1", "prompt": "delegate"})
			s.feed(t, map[string]any{"hook_event_name": "SubagentStart", "session_id": "sess-1", "agent_id": "agent1"})

			parentTool := map[string]any{
				"hook_event_name": "PostToolUse", "session_id": "sess-1",
				"tool_name": "Read", "tool_use_id": "tu-parent", "transcript_path": parentTranscript,
			}
			// Both actors are handed the same transcript_path, because that is what
			// Claude Code sends: a sub-agent's PostToolUse names the main session's
			// file and nothing else. The sub-agent's own transcript is derived.
			agentTool := map[string]any{
				"hook_event_name": "PostToolUse", "session_id": "sess-1", "agent_id": "agent1",
				"tool_name": "Grep", "tool_use_id": "tu-agent", "transcript_path": parentTranscript,
			}
			if tc.agentFirst {
				s.feed(t, agentTool)
				s.feed(t, parentTool)
			} else {
				s.feed(t, parentTool)
				s.feed(t, agentTool)
			}

			mu.Lock()
			defer mu.Unlock()
			require.Len(t, *spans, 2)
			models := map[string]string{}
			for _, span := range *spans {
				models[span.Name] = stringAttrOf(t, span, "gen_ai.request.model")
			}
			assert.Equal(t, "claude-sonnet-4-6", models["execute_tool Read"])
			assert.Equal(t, "claude-opus-4-8", models["execute_tool Grep"])
		})
	}
}

// The session's first tool call can fire before the assistant entry that
// requested it has been flushed. Waiting for it is what stops that one span from
// being the odd one out.
func TestProcess_PostToolUse_WaitsForTheFirstAssistantEntry(t *testing.T) {
	url, spans, mu := mockOTLPServer(t)
	s := newSetup(t, url)

	transcriptPath := filepath.Join(t.TempDir(), "transcript.jsonl")
	require.NoError(t, os.WriteFile(transcriptPath,
		[]byte(`{"type":"user","message":{"role":"user","content":"do thing"}}`+"\n"), 0o644))

	s.feed(t, map[string]any{"hook_event_name": "SessionStart", "session_id": "sess-1"})
	s.feed(t, map[string]any{"hook_event_name": "UserPromptSubmit", "session_id": "sess-1", "prompt": "do thing"})

	// Flush the assistant entry while the hook is already waiting.
	done := make(chan struct{})
	go func() {
		defer close(done)
		time.Sleep(150 * time.Millisecond)
		f, err := os.OpenFile(transcriptPath, os.O_APPEND|os.O_WRONLY, 0o644)
		if err != nil {
			return
		}
		_, _ = f.WriteString(`{"type":"assistant","message":{"id":"m1","role":"assistant","model":"claude-opus-5","content":[{"type":"text","text":"x"}],"usage":{"input_tokens":1,"output_tokens":1}}}` + "\n")
		_ = f.Close()
	}()

	s.feed(t, map[string]any{
		"hook_event_name": "PostToolUse",
		"session_id":      "sess-1",
		"tool_name":       "Bash",
		"tool_use_id":     "tu1",
		"transcript_path": transcriptPath,
	})
	<-done

	mu.Lock()
	defer mu.Unlock()
	require.Len(t, *spans, 1)
	assert.True(t, hasStringAttr((*spans)[0].Attributes, "gen_ai.request.model", "claude-opus-5"))
}

// A later turn must not reuse the preceding turn's model while its own
// assistant entry is still being flushed. The tool hook should wait for the
// current turn instead of treating any earlier assistant entry as ready.
func TestProcess_PostToolUse_WaitsForTheCurrentTurnAssistantEntry(t *testing.T) {
	url, spans, mu := mockOTLPServer(t)
	s := newSetup(t, url)

	transcriptPath := filepath.Join(t.TempDir(), "transcript.jsonl")
	record := strings.Join([]string{
		`{"type":"user","message":{"role":"user","content":"first turn"}}`,
		`{"type":"assistant","message":{"id":"m1","role":"assistant","model":"claude-haiku-4-5","content":[{"type":"text","text":"done"}]}}`,
		`{"type":"user","message":{"role":"user","content":"second turn"}}`,
	}, "\n") + "\n"
	require.NoError(t, os.WriteFile(transcriptPath, []byte(record), 0o644))

	s.feed(t, map[string]any{"hook_event_name": "SessionStart", "session_id": "sess-1"})
	s.feed(t, map[string]any{"hook_event_name": "UserPromptSubmit", "session_id": "sess-1", "prompt": "second turn"})

	done := make(chan struct{})
	go func() {
		defer close(done)
		time.Sleep(150 * time.Millisecond)
		f, err := os.OpenFile(transcriptPath, os.O_APPEND|os.O_WRONLY, 0o644)
		if err != nil {
			return
		}
		_, _ = f.WriteString(`{"type":"assistant","message":{"id":"m2","role":"assistant","model":"claude-opus-4-8","content":[{"type":"tool_use","name":"Bash"}]}}` + "\n")
		_ = f.Close()
	}()

	s.feed(t, map[string]any{
		"hook_event_name": "PostToolUse", "session_id": "sess-1",
		"tool_name": "Bash", "tool_use_id": "tu-current", "transcript_path": transcriptPath,
	})
	<-done

	mu.Lock()
	defer mu.Unlock()
	require.Len(t, *spans, 1)
	assert.Equal(t, "claude-opus-4-8", stringAttrOf(t, (*spans)[0], "gen_ai.request.model"))
}

// A transcript that records assistant entries without a model belongs to a
// source that does not report one. Waiting on it would add the full budget to
// every tool call and change nothing.
func TestProcess_PostToolUse_DoesNotWaitWhenTranscriptNamesNoModel(t *testing.T) {
	url, spans, mu := mockOTLPServer(t)
	s := newSetup(t, url)

	transcriptPath := filepath.Join(t.TempDir(), "transcript.jsonl")
	require.NoError(t, os.WriteFile(transcriptPath, []byte(strings.Join([]string{
		`{"type":"user","message":{"role":"user","content":"do thing"}}`,
		`{"type":"assistant","message":{"id":"m1","role":"assistant","content":[{"type":"text","text":"x"}],"usage":{"input_tokens":1,"output_tokens":1}}}`,
	}, "\n")+"\n"), 0o644))

	s.feed(t, map[string]any{"hook_event_name": "SessionStart", "session_id": "sess-1"})
	s.feed(t, map[string]any{"hook_event_name": "UserPromptSubmit", "session_id": "sess-1", "prompt": "do thing"})

	start := time.Now()
	s.feed(t, map[string]any{
		"hook_event_name": "PostToolUse",
		"session_id":      "sess-1",
		"tool_name":       "Bash",
		"tool_use_id":     "tu1",
		"transcript_path": transcriptPath,
	})
	assert.Less(t, time.Since(start), modelWaitBudget, "no assistant entry is pending, so there is nothing to wait for")

	mu.Lock()
	defer mu.Unlock()
	require.Len(t, *spans, 1)
	for _, a := range (*spans)[0].Attributes {
		assert.NotEqual(t, "gen_ai.request.model", a.Key, "a model that is not on record is not invented")
	}
}

// A source that puts the model on the event itself (Cursor, Copilot) is taken at
// its word, with no transcript read and no wait.
func TestProcess_PostToolUse_KeepsModelSuppliedByTheSource(t *testing.T) {
	url, spans, mu := mockOTLPServer(t)
	s := newSetup(t, url)

	s.feed(t, map[string]any{
		"hook_event_name": "SessionStart", "session_id": "sess-1", "model": "claude-sonnet-4-6",
	})
	s.feed(t, map[string]any{"hook_event_name": "UserPromptSubmit", "session_id": "sess-1", "prompt": "do thing"})
	s.feed(t, map[string]any{
		"hook_event_name": "PostToolUse",
		"session_id":      "sess-1",
		"tool_name":       "Bash",
		"tool_use_id":     "tu1",
		"model":           "cursor-auto",
	})

	mu.Lock()
	defer mu.Unlock()
	require.Len(t, *spans, 1)
	assert.True(t, hasStringAttr((*spans)[0].Attributes, "gen_ai.request.model", "cursor-auto"))
}

// A skill invoked by its slash command fires no tool hook, so the invocation is
// reported on the turn's chat span. Without this, every deliberate invocation
// was missing and the skill counts in Dash0 were the model's choices only.
func TestProcess_Stop_CountsSkillInvokedBySlashCommand(t *testing.T) {
	url, spans, mu := mockOTLPServer(t)
	s := newSetup(t, url)

	path := filepath.Join(t.TempDir(), "transcript.jsonl")
	lines := []string{
		`{"type":"user","message":{"role":"user","content":"<command-message>writing:unslop</command-message>\n<command-name>/writing:unslop</command-name>\n<command-args>some text</command-args>"}}`,
		`{"type":"user","isMeta":true,"message":{"role":"user","content":[{"type":"text","text":"Base directory for this skill: /home/me/.claude/plugins/cache/mp/writing/0.3.0/skills/unslop\n\n# Unslop\n"}]}}`,
		`{"type":"assistant","message":{"id":"msg_1","role":"assistant","model":"claude-haiku-4-5-20251001","stop_reason":"end_turn","content":[{"type":"text","text":"done"}],"usage":{"input_tokens":10,"output_tokens":20,"cache_creation_input_tokens":0,"cache_read_input_tokens":0}}}`,
	}
	require.NoError(t, os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o644))

	s.feed(t, map[string]any{"hook_event_name": "SessionStart", "session_id": "sess-1"})
	s.feed(t, map[string]any{"hook_event_name": "UserPromptSubmit", "session_id": "sess-1", "prompt": "/writing:unslop some text"})
	s.feed(t, map[string]any{"hook_event_name": "Stop", "session_id": "sess-1", "transcript_path": path})

	mu.Lock()
	defer mu.Unlock()
	require.Len(t, *spans, 1)
	chat := (*spans)[0]
	assert.True(t, hasStringAttr(chat.Attributes, "dash0.gen_ai.tool.skill.name", "writing:unslop"),
		"same key as the Skill tool, so one query counts both routes")
	assert.True(t, hasStringAttr(chat.Attributes, "dash0.gen_ai.tool.skill.source", "command"),
		"and the route stays distinguishable")
}

// A built-in slash command writes the same <command-name> tag but loads no
// skill. Counting it would inflate skill usage with /compact and /plugin, so the
// skill-instructions relay is required as well.
func TestProcess_Stop_IgnoresBuiltinSlashCommand(t *testing.T) {
	url, spans, mu := mockOTLPServer(t)
	s := newSetup(t, url)

	path := filepath.Join(t.TempDir(), "transcript.jsonl")
	lines := []string{
		`{"type":"user","message":{"role":"user","content":"<command-name>/compact</command-name>\n<command-message>compact</command-message>\n<command-args></command-args>"}}`,
		`{"type":"assistant","message":{"id":"msg_1","role":"assistant","model":"claude-haiku-4-5-20251001","stop_reason":"end_turn","content":[{"type":"text","text":"done"}],"usage":{"input_tokens":10,"output_tokens":20,"cache_creation_input_tokens":0,"cache_read_input_tokens":0}}}`,
	}
	require.NoError(t, os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o644))

	s.feed(t, map[string]any{"hook_event_name": "SessionStart", "session_id": "sess-1"})
	s.feed(t, map[string]any{"hook_event_name": "UserPromptSubmit", "session_id": "sess-1", "prompt": "/compact"})
	s.feed(t, map[string]any{"hook_event_name": "Stop", "session_id": "sess-1", "transcript_path": path})

	mu.Lock()
	defer mu.Unlock()
	require.Len(t, *spans, 1)
	for _, a := range (*spans)[0].Attributes {
		assert.NotEqual(t, "dash0.gen_ai.tool.skill.name", a.Key)
		assert.NotEqual(t, "dash0.gen_ai.tool.skill.source", a.Key)
	}
}

// Thinking tokens are already inside output_tokens, so cost is unaffected — but
// without the split a long deliberation reads as a long answer, and the effort
// level is the one thing a user can act on.
func TestProcess_EmitsThinkingTokenSplit(t *testing.T) {
	url, spans, mu := mockOTLPServer(t)
	s := newSetup(t, url)

	path := filepath.Join(t.TempDir(), "transcript.jsonl")
	lines := []string{
		`{"type":"user","message":{"role":"user","content":"think about it"}}`,
		`{"type":"assistant","message":{"id":"msg_1","role":"assistant","model":"claude-haiku-4-5-20251001","stop_reason":"end_turn","content":[{"type":"text","text":"done"}],"usage":{"input_tokens":10,"output_tokens":116,"cache_creation_input_tokens":0,"cache_read_input_tokens":0,"output_tokens_details":{"thinking_tokens":104}}}}`,
	}
	require.NoError(t, os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o644))

	s.feed(t, map[string]any{"hook_event_name": "SessionStart", "session_id": "sess-1"})
	s.feed(t, map[string]any{"hook_event_name": "UserPromptSubmit", "session_id": "sess-1", "prompt": "think about it"})
	s.feed(t, map[string]any{"hook_event_name": "Stop", "session_id": "sess-1", "transcript_path": path})

	mu.Lock()
	defer mu.Unlock()
	require.Len(t, *spans, 1)
	chat := (*spans)[0]
	assert.Equal(t, "116", intAttr(t, chat, "gen_ai.usage.output_tokens"))
	assert.Equal(t, "104", intAttr(t, chat, "gen_ai.usage.reasoning.output_tokens"),
		"a subset of output_tokens, not an addition to it")
}

// A sub-agent that spawns another sub-agent: the inner Agent tool span keeps its
// derived span id, so the inner agent's own spans still find it, and it parents
// under the outer agent instead of being flattened onto the turn's chat span.
func TestProcess_PostToolUse_NestedAgentKeepsItsNesting(t *testing.T) {
	url, spans, mu := mockOTLPServer(t)
	s := newSetup(t, url)

	s.feed(t, map[string]any{"hook_event_name": "SessionStart", "session_id": "sess-1"})
	s.feed(t, map[string]any{"hook_event_name": "UserPromptSubmit", "session_id": "sess-1", "prompt": "delegate it"})
	s.feed(t, map[string]any{"hook_event_name": "SubagentStart", "session_id": "sess-1", "agent_id": "outer"})
	s.feed(t, map[string]any{
		"hook_event_name": "PostToolUse",
		"session_id":      "sess-1",
		"agent_id":        "outer",
		"tool_name":       "Agent",
		"tool_use_id":     "tu-nested",
		"tool_response":   map[string]any{"agentId": "inner"},
	})

	mu.Lock()
	defer mu.Unlock()
	require.Len(t, *spans, 1)
	span := (*spans)[0]
	assert.Equal(t, otlp.SpanIDFromAgentID("inner"), span.SpanID, "the inner agent's spans name this as their parent")
	assert.Equal(t, otlp.SpanIDFromAgentID("outer"), span.ParentSpanID, "and it hangs off the agent that spawned it")
}

// A top-level Agent call carries no agent_id — the launched agent's id arrives
// in the response — so it must still parent under the turn's chat span.
func TestProcess_PostToolUse_TopLevelAgentParentsUnderTheChatSpan(t *testing.T) {
	url, spans, mu := mockOTLPServer(t)
	s := newSetup(t, url)

	s.feed(t, map[string]any{"hook_event_name": "SessionStart", "session_id": "sess-1"})
	s.feed(t, map[string]any{"hook_event_name": "UserPromptSubmit", "session_id": "sess-1", "prompt": "delegate it"})

	ctx, err := otlp.LoadTraceContext(s.sessionDir("sess-1"))
	require.NoError(t, err)
	require.NotNil(t, ctx)

	s.feed(t, map[string]any{
		"hook_event_name": "PostToolUse",
		"session_id":      "sess-1",
		"tool_name":       "Agent",
		"tool_use_id":     "tu-top",
		"tool_response":   map[string]any{"agentId": "agent1"},
	})

	mu.Lock()
	defer mu.Unlock()
	require.Len(t, *spans, 1)
	span := (*spans)[0]
	assert.Equal(t, otlp.SpanIDFromAgentID("agent1"), span.SpanID)
	assert.Equal(t, ctx.SpanID, span.ParentSpanID)
}

// writeSubagentTranscript writes a sub-agent's transcript where Claude Code puts
// it, so a test resolves it the way the pipeline has to: by derivation from the
// main transcript's directory, the session ID and the agent ID. No hook payload
// names this file.
func writeSubagentTranscript(t *testing.T, sessionTranscriptDir, sessionID, agentID, model string) string {
	t.Helper()
	dir := filepath.Join(sessionTranscriptDir, sessionID, "subagents")
	require.NoError(t, os.MkdirAll(dir, 0o755))
	return writeModelTranscript(t, dir, "agent-"+agentID+".jsonl", model)
}

// writeModelTranscript writes a one-turn transcript naming a specific model, so
// a test can change the model between turns.
func writeModelTranscript(t *testing.T, dir, name, model string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	lines := []string{
		`{"type":"user","message":{"role":"user","content":"do thing"}}`,
		`{"type":"assistant","message":{"id":"msg_` + model + `","role":"assistant","model":"` + model +
			`","stop_reason":"end_turn","content":[{"type":"text","text":"done"}],"usage":{"input_tokens":10,"output_tokens":20,"cache_creation_input_tokens":0,"cache_read_input_tokens":0}}}`,
	}
	require.NoError(t, os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o644))
	return path
}

// A model resolved from the transcript is cached for the turn that resolved it
// and no longer.
//
// Caching it as the session Model instead leaked across turns, because
// UserPromptSubmit copies Model into the new turn's context. Most turns are
// unaffected: Stop clears the context first, so the carry-forward finds nothing.
// The turn that skips Stop is the one that matters — the user interrupts, the
// next prompt inherits the old value, and a /model switch or an Opus-to-Sonnet
// fallback keeps reporting the model the session started with. So this test
// deliberately omits Stop between the two turns.
func TestProcess_ToolSpanFollowsAMidSessionModelSwitch(t *testing.T) {
	url, spans, mu := mockOTLPServer(t)
	s := newSetup(t, url)
	dir := t.TempDir()

	first := writeModelTranscript(t, dir, "turn1.jsonl", "claude-haiku-4-5")
	s.feed(t, map[string]any{
		"hook_event_name": "SessionStart", "session_id": "sess-1", "model": "claude-sonnet-4-6",
	})
	s.feed(t, map[string]any{"hook_event_name": "UserPromptSubmit", "session_id": "sess-1", "prompt": "one"})
	s.feed(t, map[string]any{
		"hook_event_name": "PostToolUse", "session_id": "sess-1",
		"tool_name": "Bash", "tool_use_id": "tu1", "transcript_path": first,
	})

	// No Stop: the turn was interrupted, so the context survives into the next one.
	// The user then switches model, so the next turn's transcript names another.
	second := writeModelTranscript(t, dir, "turn2.jsonl", "claude-opus-4-8")
	s.feed(t, map[string]any{"hook_event_name": "UserPromptSubmit", "session_id": "sess-1", "prompt": "two"})
	s.feed(t, map[string]any{
		"hook_event_name": "PostToolUse", "session_id": "sess-1",
		"tool_name": "Bash", "tool_use_id": "tu2", "transcript_path": second,
	})
	s.feed(t, map[string]any{
		"hook_event_name": "Stop", "session_id": "sess-1", "transcript_path": second,
	})

	mu.Lock()
	defer mu.Unlock()
	var tools []otlp.Span
	var chats []otlp.Span
	for _, sp := range *spans {
		if strings.HasPrefix(sp.Name, "execute_tool") {
			tools = append(tools, sp)
		} else if strings.HasPrefix(sp.Name, "chat ") {
			chats = append(chats, sp)
		}
	}
	require.Len(t, tools, 2)
	require.Len(t, chats, 1)
	assert.Equal(t, "claude-haiku-4-5", stringAttrOf(t, tools[0], "gen_ai.request.model"))
	assert.Equal(t, "claude-opus-4-8", stringAttrOf(t, tools[1], "gen_ai.request.model"),
		"the second turn reports the model it actually ran, not the first turn's")
	assert.Equal(t, "claude-opus-4-8", stringAttrOf(t, chats[0], "gen_ai.request.model"),
		"the chat span agrees with the tool span for the switched turn")
}

// A transcript_path that names no file is not a flush in progress — nothing is
// coming. Polling it anyway spent the whole model-wait budget on every tool call
// of such a session, measured at ~1.6s per call, because a failed resolution
// caches nothing and so never converges.
func TestProcess_PostToolUse_DoesNotWaitForATranscriptThatDoesNotExist(t *testing.T) {
	url, spans, mu := mockOTLPServer(t)
	s := newSetup(t, url)
	missing := filepath.Join(t.TempDir(), "never-written.jsonl")

	s.feed(t, map[string]any{"hook_event_name": "SessionStart", "session_id": "sess-1"})
	s.feed(t, map[string]any{"hook_event_name": "UserPromptSubmit", "session_id": "sess-1", "prompt": "do thing"})

	start := time.Now()
	for _, id := range []string{"tu1", "tu2", "tu3"} {
		s.feed(t, map[string]any{
			"hook_event_name": "PostToolUse", "session_id": "sess-1",
			"tool_name": "Bash", "tool_use_id": id, "transcript_path": missing,
		})
	}
	elapsed := time.Since(start)

	// The Windows runner comes in just over the budget (1.10s measured) doing the
	// work these three calls do without waiting at all, so it gets headroom. The
	// property still holds: paying the wait even once puts this past 2s, and
	// paying it per call puts it past 3s.
	budget := modelWaitBudget
	if runtime.GOOS == "windows" {
		budget = 2 * modelWaitBudget
	}
	assert.Less(t, elapsed, budget,
		"three tool calls must not each pay the wait; before the existence check this took ~3x the budget")

	mu.Lock()
	defer mu.Unlock()
	assert.Len(t, *spans, 3, "the spans are still exported, just without a model")
}

// Thinking tokens are emitted only when the turn did some thinking, matching the
// Copilot source's emission of the same key. A key that is unconditional in one
// runtime and conditional in another cannot be queried across both.
func TestProcess_OmitsTheThinkingSplitWhenTheTurnDidNotThink(t *testing.T) {
	url, spans, mu := mockOTLPServer(t)
	s := newSetup(t, url)

	path := writeModelTranscript(t, t.TempDir(), "transcript.jsonl", "claude-haiku-4-5")
	s.feed(t, map[string]any{"hook_event_name": "SessionStart", "session_id": "sess-1"})
	s.feed(t, map[string]any{"hook_event_name": "UserPromptSubmit", "session_id": "sess-1", "prompt": "hi"})
	s.feed(t, map[string]any{"hook_event_name": "Stop", "session_id": "sess-1", "transcript_path": path})

	mu.Lock()
	defer mu.Unlock()
	require.Len(t, *spans, 1)
	assert.Equal(t, "20", intAttr(t, (*spans)[0], "gen_ai.usage.output_tokens"))
	for _, a := range (*spans)[0].Attributes {
		assert.NotEqual(t, "gen_ai.usage.reasoning.output_tokens", a.Key,
			"absent rather than zero, so the key means the same thing in every runtime")
	}
}

// Hooks are separate OS processes and the recorded QA runs show them overlapping,
// so the session's trace context is written and read concurrently. A truncating
// write let a reader see a half-written file and drop its span; the write is a
// rename over a temp file instead. This drives the same shape in-process.
func TestProcess_ConcurrentToolCallsAllProduceSpans(t *testing.T) {
	url, spans, mu := mockOTLPServer(t)
	s := newSetup(t, url)
	path := writeModelTranscript(t, t.TempDir(), "transcript.jsonl", "claude-haiku-4-5")

	s.feed(t, map[string]any{"hook_event_name": "SessionStart", "session_id": "sess-1"})
	s.feed(t, map[string]any{"hook_event_name": "UserPromptSubmit", "session_id": "sess-1", "prompt": "do thing"})

	const parallel = 8
	var wg sync.WaitGroup
	for i := range parallel {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			s.feed(t, map[string]any{
				"hook_event_name": "PostToolUse", "session_id": "sess-1",
				"tool_name": "Bash", "tool_use_id": "tu" + strconv.Itoa(i),
				"transcript_path": path,
			})
		}(i)
	}
	wg.Wait()

	mu.Lock()
	defer mu.Unlock()
	require.Len(t, *spans, parallel, "no span is lost to a torn read of the trace context")
	for _, sp := range *spans {
		assert.Equal(t, "claude-haiku-4-5", stringAttrOf(t, sp, "gen_ai.request.model"))
	}
}

// stringAttrOf returns a span attribute's string value, failing when it is absent
// or not a string.
func stringAttrOf(t *testing.T, span otlp.Span, key string) string {
	t.Helper()
	for _, a := range span.Attributes {
		if a.Key == key {
			require.NotNil(t, a.Value.StringValue, "attribute %s is not a string", key)
			return *a.Value.StringValue
		}
	}
	t.Fatalf("attribute %s not found on span %q", key, span.Name)
	return ""
}

// A sub-agent's tool span reports the sub-agent's model, not the parent's.
//
// The three spans of one delegating turn used to disagree: the invoke_agent span
// read the sub-agent's transcript and reported haiku, while the execute_tool span
// beneath it read the main session's transcript — the only one the payload names
// — and reported the parent's opus.
func TestProcess_SubAgentToolSpanUsesTheSubAgentsModel(t *testing.T) {
	run := func(t *testing.T, withAgentTranscript bool) []otlp.Span {
		t.Helper()
		url, spans, mu := mockOTLPServer(t)
		s := newSetup(t, url)
		dir := t.TempDir()
		mainTranscript := writeModelTranscript(t, dir, "main.jsonl", "claude-opus-5")
		agentTranscript := ""
		if withAgentTranscript {
			agentTranscript = writeSubagentTranscript(t, dir, "sess-1", "agent1", "claude-haiku-4-5")
		}

		s.feed(t, map[string]any{"hook_event_name": "SessionStart", "session_id": "sess-1", "model": "claude-opus-5"})
		s.feed(t, map[string]any{"hook_event_name": "UserPromptSubmit", "session_id": "sess-1",
			"prompt": "delegate", "transcript_path": mainTranscript})
		s.feed(t, map[string]any{"hook_event_name": "SubagentStart", "session_id": "sess-1", "agent_id": "agent1"})
		s.feed(t, map[string]any{"hook_event_name": "Stop", "session_id": "sess-1", "transcript_path": mainTranscript})
		s.feed(t, map[string]any{
			"hook_event_name": "PostToolUse", "session_id": "sess-1", "agent_id": "agent1",
			"tool_name": "Bash", "tool_use_id": "tu1", "transcript_path": mainTranscript,
		})
		if withAgentTranscript {
			s.feed(t, map[string]any{
				"hook_event_name": "SubagentStop", "session_id": "sess-1", "agent_id": "agent1",
				"agent_type": "general-purpose", "transcript_path": mainTranscript,
				"agent_transcript_path": agentTranscript,
			})
		}
		mu.Lock()
		defer mu.Unlock()
		return append([]otlp.Span(nil), *spans...)
	}

	t.Run("every span in the trace agrees", func(t *testing.T) {
		models := map[string]string{}
		for _, span := range run(t, true) {
			models[span.Name] = stringAttrOf(t, span, "gen_ai.request.model")
		}
		assert.Equal(t, "claude-opus-5", models["chat claude-opus-5"])
		assert.Equal(t, "claude-haiku-4-5", models["execute_tool Bash"],
			"the tool ran inside the sub-agent, so it ran on the sub-agent's model")
		assert.Equal(t, "claude-haiku-4-5", models["invoke_agent general-purpose"])
	})

	// A wrong value that contradicts the parent span is worse than none: absence
	// is visible to a consumer, a confident mislabel is not.
	t.Run("no sub-agent transcript yet means no model, not the parent's", func(t *testing.T) {
		for _, span := range run(t, false) {
			if span.Name != "execute_tool Bash" {
				continue
			}
			for _, a := range span.Attributes {
				assert.NotEqual(t, "gen_ai.request.model", a.Key)
			}
			return
		}
		t.Fatal("the span is still emitted; only the model is withheld")
	})
}

// TestIsSafeSessionID pins the guard cmd/copilot-on-event needs before it joins
// a path or removes a directory with a session id from a hook payload. Process
// substitutes a random id for an unsafe one, but only once the event reaches it.
func TestIsSafeSessionID(t *testing.T) {
	for _, ok := range []string{"abc", "8e58c3d0-5b41-4c8a-8d70-e9dcde56c66c", "call_KpW525rG"} {
		assert.True(t, IsSafeSessionID(ok), "%q is a safe path segment", ok)
	}
	// An empty id resolves SessionDir to dataDir itself; the rest escape it.
	for _, bad := range []string{"", "..", "../victim", "a/b", "a.b", "x\x00y"} {
		assert.False(t, IsSafeSessionID(bad), "%q must not reach filepath.Join", bad)
	}
}
