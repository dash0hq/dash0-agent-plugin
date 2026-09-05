// SPDX-FileCopyrightText: Copyright 2026 Dash0 Inc.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/dash0hq/dash0-agent-plugin/internal/otlp"
)

// binaryPath is the hook binary the tests spawn, built once in TestMain.
var binaryPath string

func TestMain(m *testing.M) {
	// Built once, so a spawned hook is a plain binary.
	//
	// Re-executing this test binary as main() would work and save the build, but
	// this package spawns about thirty hook events and under -race the child would
	// carry the instrumentation too. Measured on the whole package, that is 7.7s
	// against 38.9s, and -race is what the ubuntu leg of build-test runs.
	tmpDir, err := os.MkdirTemp("", "on-event-test-*")
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to create temp dir: %v\n", err)
		os.Exit(1)
	}
	binaryPath = filepath.Join(tmpDir, "on-event")
	if runtime.GOOS == "windows" {
		// Both to build to a name it will run and to name the file the tests exec.
		binaryPath += ".exe"
	}
	build := exec.Command("go", "build", "-o", binaryPath, ".")
	build.Stderr = os.Stderr
	if err := build.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "failed to build binary: %v\n", err)
		os.Exit(1)
	}

	// Point the home and working directories at empty temp directories. Config
	// lookups fall back to .claude/dash0-agent-plugin.local.md in the project and
	// in the user's home, so without this a developer who has the plugin
	// configured runs these against their own endpoint and token.
	home, err := os.MkdirTemp("", "on-event-test-home-*")
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to create temp home: %v\n", err)
		os.Exit(1)
	}
	// os.UserHomeDir reads HOME on Unix and USERPROFILE on Windows.
	os.Setenv("HOME", home)
	os.Setenv("USERPROFILE", home)
	if err := os.Chdir(home); err != nil {
		fmt.Fprintf(os.Stderr, "failed to chdir to temp home: %v\n", err)
		os.Exit(1)
	}

	code := m.Run()
	_ = os.RemoveAll(home)
	_ = os.RemoveAll(tmpDir)
	os.Exit(code)
}

// execHook runs one hook event in its own process, with the given environment.
//
// A process per event, because that is what Claude Code does and what the two
// concurrency tests below are about: run() reads os.Stdin, a process global, so
// two events running at once cannot each have their own payload in-process.
func execHook(t *testing.T, event string, env []string) (stdout, stderr string) {
	t.Helper()
	cmd := exec.Command(binaryPath)
	cmd.Stdin = strings.NewReader(event)
	cmd.Env = env

	var outBuf, errBuf bytes.Buffer
	cmd.Stdout = &outBuf
	cmd.Stderr = &errBuf

	err := cmd.Run()
	if err != nil {
		t.Logf("hook stderr: %s", errBuf.String())
	}
	return outBuf.String(), errBuf.String()
}

// makeEnv builds the environment for one hook process.
func makeEnv(dataDir, otlpURL string) []string {
	return append(os.Environ(),
		"CLAUDE_PLUGIN_DATA="+dataDir,
		"DASH0_OTLP_URL="+otlpURL,
	)
}

// spansCollector starts an HTTP server that collects OTLP trace spans.
func spansCollector(t *testing.T) (*httptest.Server, *[]otlp.Span, *sync.Mutex) {
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

func TestIntegrationParallelSessionsIsolated(t *testing.T) {
	dataDir := t.TempDir()
	srv, spans, mu := spansCollector(t)
	env := makeEnv(dataDir, srv.URL)

	// Sequence of events for one session turn.
	sessionEvents := func(sessionID, toolName, toolUseID string) []string {
		return []string{
			fmt.Sprintf(`{"hook_event_name":"SessionStart","session_id":"%s","model":"opus"}`, sessionID),
			fmt.Sprintf(`{"hook_event_name":"UserPromptSubmit","session_id":"%s","prompt":"task for %s"}`, sessionID, sessionID),
			fmt.Sprintf(`{"hook_event_name":"PreToolUse","session_id":"%s","tool_name":"%s","tool_use_id":"%s"}`, sessionID, toolName, toolUseID),
			fmt.Sprintf(`{"hook_event_name":"PostToolUse","session_id":"%s","tool_name":"%s","tool_use_id":"%s","tool_response":"done"}`, sessionID, toolName, toolUseID),
			fmt.Sprintf(`{"hook_event_name":"Stop","session_id":"%s"}`, sessionID),
		}
	}

	eventsA := sessionEvents("parallel-sess-A", "Read", "tu-pA")
	eventsB := sessionEvents("parallel-sess-B", "Bash", "tu-pB")

	// Run both sessions in parallel, interleaving events.
	var wg sync.WaitGroup
	wg.Add(2)

	go func() {
		defer wg.Done()
		for _, event := range eventsA {
			execHook(t, event, env)
		}
	}()

	go func() {
		defer wg.Done()
		for _, event := range eventsB {
			execHook(t, event, env)
		}
	}()

	wg.Wait()

	// Collect results.
	mu.Lock()
	allSpans := make([]otlp.Span, len(*spans))
	copy(allSpans, *spans)
	mu.Unlock()

	// Should have 4 spans: 2 per session (tool + chat).
	require.Len(t, allSpans, 4, "expected 4 spans (2 per session)")

	// Separate spans by conversation ID.
	spansBySession := map[string][]otlp.Span{}
	for _, s := range allSpans {
		for _, a := range s.Attributes {
			if a.Key == "gen_ai.conversation.id" && a.Value.StringValue != nil {
				spansBySession[*a.Value.StringValue] = append(spansBySession[*a.Value.StringValue], s)
			}
		}
	}

	require.Contains(t, spansBySession, "parallel-sess-A", "session A spans should exist")
	require.Contains(t, spansBySession, "parallel-sess-B", "session B spans should exist")
	assert.Len(t, spansBySession["parallel-sess-A"], 2, "session A should have 2 spans")
	assert.Len(t, spansBySession["parallel-sess-B"], 2, "session B should have 2 spans")

	// Spans within each session share a trace ID.
	sessASpans := spansBySession["parallel-sess-A"]
	sessBSpans := spansBySession["parallel-sess-B"]
	assert.Equal(t, sessASpans[0].TraceID, sessASpans[1].TraceID, "session A spans should share trace ID")
	assert.Equal(t, sessBSpans[0].TraceID, sessBSpans[1].TraceID, "session B spans should share trace ID")

	// Sessions have different trace IDs.
	assert.NotEqual(t, sessASpans[0].TraceID, sessBSpans[0].TraceID, "sessions should have different trace IDs")

	// Verify separate session directories were created.
	assert.DirExists(t, filepath.Join(dataDir, "parallel-sess-A"))
	assert.DirExists(t, filepath.Join(dataDir, "parallel-sess-B"))

	// Verify each session directory has its own events.
	for _, sid := range []string{"parallel-sess-A", "parallel-sess-B"} {
		eventsFile := filepath.Join(dataDir, sid, "events.jsonl")
		data, err := os.ReadFile(eventsFile)
		require.NoError(t, err)
		lines := strings.Split(strings.TrimSpace(string(data)), "\n")
		// All events in this file should belong to this session.
		for _, line := range lines {
			var e map[string]any
			require.NoError(t, json.Unmarshal([]byte(line), &e))
			assert.Equal(t, sid, e["session_id"], "event in %s dir should belong to that session", sid)
		}
	}
}

func TestIntegrationParallelToolCallsWithinSession(t *testing.T) {
	dataDir := t.TempDir()
	srv, spans, mu := spansCollector(t)
	env := makeEnv(dataDir, srv.URL)

	// Session setup.
	execHook(t, `{"hook_event_name":"SessionStart","session_id":"parallel-tools","model":"opus"}`, env)
	execHook(t, `{"hook_event_name":"UserPromptSubmit","session_id":"parallel-tools","prompt":"do stuff"}`, env)

	// Two PreToolUse events in parallel (simulating Claude calling two tools at once).
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		execHook(t, `{"hook_event_name":"PreToolUse","session_id":"parallel-tools","tool_name":"Read","tool_use_id":"tu-p1"}`, env)
	}()
	go func() {
		defer wg.Done()
		execHook(t, `{"hook_event_name":"PreToolUse","session_id":"parallel-tools","tool_name":"Grep","tool_use_id":"tu-p2"}`, env)
	}()
	wg.Wait()

	// Two PostToolUse events in parallel.
	wg.Add(2)
	go func() {
		defer wg.Done()
		execHook(t, `{"hook_event_name":"PostToolUse","session_id":"parallel-tools","tool_name":"Read","tool_use_id":"tu-p1","tool_response":"file content"}`, env)
	}()
	go func() {
		defer wg.Done()
		execHook(t, `{"hook_event_name":"PostToolUse","session_id":"parallel-tools","tool_name":"Grep","tool_use_id":"tu-p2","tool_response":"grep result"}`, env)
	}()
	wg.Wait()

	execHook(t, `{"hook_event_name":"Stop","session_id":"parallel-tools"}`, env)

	mu.Lock()
	allSpans := make([]otlp.Span, len(*spans))
	copy(allSpans, *spans)
	mu.Unlock()

	// Should have 3 spans: 2 tools + 1 chat.
	require.Len(t, allSpans, 3, "expected 3 spans (2 tools + 1 chat)")

	// All spans should share the same trace ID.
	traceID := allSpans[0].TraceID
	for _, s := range allSpans {
		assert.Equal(t, traceID, s.TraceID, "all spans in session should share trace ID")
	}

	// Find the two tool spans.
	var toolNames []string
	for _, s := range allSpans {
		if strings.HasPrefix(s.Name, "execute_tool") {
			toolNames = append(toolNames, s.Name)
		}
	}
	assert.Len(t, toolNames, 2, "should have 2 tool spans")
	assert.Contains(t, toolNames, "execute_tool Read")
	assert.Contains(t, toolNames, "execute_tool Grep")
}

func TestIntegrationInvalidOTLPUrlLogsWarning(t *testing.T) {
	dataDir := t.TempDir()
	env := append(os.Environ(),
		"CLAUDE_PLUGIN_DATA="+dataDir,
		"DASH0_OTLP_URL=not-a-url",
	)

	_, stderr := execHook(t, `{"hook_event_name":"SessionStart","session_id":"sess-badurl","model":"opus"}`, env)
	assert.Contains(t, stderr, `OTLP URL is not valid: "not-a-url"`)
}

// Billing mode is verified against the real binary rather than only in-process,
// because it depends on the environment the hook INHERITS — which unit tests fake.
//
// It needs no particular account: the reader derives mode from a config file and
// env credentials, so a synthetic config isolated by CLAUDE_CONFIG_DIR reproduces
// any customer's shape exactly. `usage_based` in particular is a Claude Console
// account, which nobody here has, and which would otherwise be reported as a
// subscription — telling that customer their real spend is not real spend.
func TestIntegrationClaudeBillingModeFromConfig(t *testing.T) {
	cases := []struct {
		name        string
		billingType string
		extraEnv    []string
		want        string
		wantProv    string
	}{
		{"console account bills per token", "usage_based", nil, "api", ""},
		{"team subscription", "stripe_subscription", nil, "subscription", ""},
		{"unrecognised type is not assumed", "paddle_subscription", nil, "unknown", ""},
		// The environment outranks the config: a subscription on disk plus Bedrock
		// in the env is per-token at an AWS rate.
		{"bedrock env beats a subscription config", "stripe_subscription",
			[]string{"CLAUDE_CODE_USE_BEDROCK=1"}, "metered_external", "bedrock"},
		{"bearer token is a gateway", "stripe_subscription",
			[]string{"ANTHROPIC_AUTH_TOKEN=tok"}, "metered_external", "gateway"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfgDir := t.TempDir()
			cfg := fmt.Sprintf(`{"claudeMaxTier":"not_max","oauthAccount":{"billingType":%q,"seatTier":"team_standard"}}`, tc.billingType)
			require.NoError(t, os.WriteFile(filepath.Join(cfgDir, ".claude.json"), []byte(cfg), 0o600))

			srv, spans, mu := spansCollector(t)
			env := append(makeEnv(t.TempDir(), srv.URL),
				"CLAUDE_CONFIG_DIR="+cfgDir,
				// Pinned empty so a credential exported on the host machine cannot
				// change the result under us.
				"ANTHROPIC_API_KEY=", "ANTHROPIC_AUTH_TOKEN=",
				"CLAUDE_CODE_USE_BEDROCK=", "CLAUDE_CODE_USE_VERTEX=",
				"CLAUDE_CODE_USE_FOUNDRY=", "CLAUDE_CODE_OAUTH_TOKEN=",
				"ANTHROPIC_PROFILE=",
				"ANTHROPIC_FEDERATION_RULE_ID=", "ANTHROPIC_ORGANIZATION_ID=",
			)
			env = append(env, tc.extraEnv...)

			// Billing mode only rides alongside a cost figure, so the Stop event
			// needs a transcript with a complete turn — which is what a real Stop
			// always has.
			tp := filepath.Join(t.TempDir(), "transcript.jsonl")
			require.NoError(t, os.WriteFile(tp, []byte(
				`{"type":"user","message":{"role":"user","content":"hi"}}`+"\n"+
					`{"type":"assistant","requestId":"r1","message":{"role":"assistant","stop_reason":"end_turn","content":[{"type":"text","text":"x"}],"usage":{"input_tokens":5,"output_tokens":6}}}`+"\n"), 0o644))

			for _, ev := range []string{
				`{"hook_event_name":"SessionStart","session_id":"bill-1","model":"opus"}`,
				`{"hook_event_name":"UserPromptSubmit","session_id":"bill-1","prompt":"hi"}`,
				fmt.Sprintf(`{"hook_event_name":"Stop","session_id":"bill-1","transcript_path":%q}`, tp),
			} {
				execHook(t, ev, env)
			}

			mu.Lock()
			defer mu.Unlock()
			require.Len(t, *spans, 1)
			assert.True(t, hasStringAttr((*spans)[0].Attributes, "dash0.gen_ai.billing_mode", tc.want),
				"want billing_mode=%s, attrs: %v", tc.want, (*spans)[0].Attributes)
			if tc.wantProv != "" {
				assert.True(t, hasStringAttr((*spans)[0].Attributes, "dash0.gen_ai.billing_provider", tc.wantProv))
			} else {
				for _, a := range (*spans)[0].Attributes {
					assert.NotEqual(t, "dash0.gen_ai.billing_provider", a.Key,
						"no intermediary, so no provider")
				}
			}
		})
	}
}

// hasStringAttr reports whether attrs carries key=value as a string attribute.
func hasStringAttr(attrs []otlp.Attribute, key, value string) bool {
	for _, a := range attrs {
		if a.Key == key && a.Value.StringValue != nil && *a.Value.StringValue == value {
			return true
		}
	}
	return false
}
