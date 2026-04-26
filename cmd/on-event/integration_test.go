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
	"strings"
	"sync"
	"testing"

	"github.com/dash0hq/dash0-agent-plugin/internal/otlp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// binaryPath holds the path to the compiled on-event binary, built once in TestMain.
var binaryPath string

func TestMain(m *testing.M) {
	// Build the binary once for integration tests.
	tmpDir, err := os.MkdirTemp("", "on-event-test-*")
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to create temp dir: %v\n", err)
		os.Exit(1)
	}
	defer os.RemoveAll(tmpDir)

	binaryPath = filepath.Join(tmpDir, "on-event")
	cmd := exec.Command("go", "build", "-o", binaryPath, ".")
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "failed to build binary: %v\n", err)
		os.Exit(1)
	}

	os.Exit(m.Run())
}

// execBinary runs the compiled binary with the given JSON event on stdin
// and the provided environment variables.
func execBinary(t *testing.T, event string, env []string) (stdout, stderr string) {
	t.Helper()
	cmd := exec.Command(binaryPath)
	cmd.Stdin = strings.NewReader(event)
	cmd.Env = env

	var outBuf, errBuf bytes.Buffer
	cmd.Stdout = &outBuf
	cmd.Stderr = &errBuf

	err := cmd.Run()
	if err != nil {
		t.Logf("binary stderr: %s", errBuf.String())
	}
	return outBuf.String(), errBuf.String()
}

// makeEnv builds the environment for a subprocess invocation.
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
			execBinary(t, event, env)
		}
	}()

	go func() {
		defer wg.Done()
		for _, event := range eventsB {
			execBinary(t, event, env)
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
	execBinary(t, `{"hook_event_name":"SessionStart","session_id":"parallel-tools","model":"opus"}`, env)
	execBinary(t, `{"hook_event_name":"UserPromptSubmit","session_id":"parallel-tools","prompt":"do stuff"}`, env)

	// Two PreToolUse events in parallel (simulating Claude calling two tools at once).
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		execBinary(t, `{"hook_event_name":"PreToolUse","session_id":"parallel-tools","tool_name":"Read","tool_use_id":"tu-p1"}`, env)
	}()
	go func() {
		defer wg.Done()
		execBinary(t, `{"hook_event_name":"PreToolUse","session_id":"parallel-tools","tool_name":"Grep","tool_use_id":"tu-p2"}`, env)
	}()
	wg.Wait()

	// Two PostToolUse events in parallel.
	wg.Add(2)
	go func() {
		defer wg.Done()
		execBinary(t, `{"hook_event_name":"PostToolUse","session_id":"parallel-tools","tool_name":"Read","tool_use_id":"tu-p1","tool_response":"file content"}`, env)
	}()
	go func() {
		defer wg.Done()
		execBinary(t, `{"hook_event_name":"PostToolUse","session_id":"parallel-tools","tool_name":"Grep","tool_use_id":"tu-p2","tool_response":"grep result"}`, env)
	}()
	wg.Wait()

	execBinary(t, `{"hook_event_name":"Stop","session_id":"parallel-tools"}`, env)

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

	_, stderr := execBinary(t, `{"hook_event_name":"SessionStart","session_id":"sess-badurl","model":"opus"}`, env)
	assert.Contains(t, stderr, `DASH0_OTLP_URL is not a valid URL: "not-a-url"`)
}
