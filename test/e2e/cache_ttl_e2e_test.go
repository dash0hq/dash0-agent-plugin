// SPDX-FileCopyrightText: Copyright 2026 Dash0 Inc.
// SPDX-License-Identifier: Apache-2.0

//go:build e2e

package e2e

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
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

// TestE2ECacheCreationTTL drives the built on-event binary with a synthetic
// Claude transcript that carries the per-TTL cache-creation split, and asserts
// the Stop chat span exported over OTLP carries the
// dash0.gen_ai.usage.cache_creation.ephemeral_{5m,1h}.input_tokens attributes read
// from that transcript. Hermetic — no Claude CLI, no API key.
func TestE2ECacheCreationTTL(t *testing.T) {
	pluginDir := findPluginDir(t)

	binary := filepath.Join(t.TempDir(), "on-event-cachettl")
	build := exec.Command("go", "build", "-o", binary, "./cmd/on-event")
	build.Dir = pluginDir
	out, err := build.CombinedOutput()
	require.NoError(t, err, "build failed: %s", string(out))

	var (
		mu     sync.Mutex
		bodies [][]byte
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if r.URL.Path == "/v1/traces" {
			mu.Lock()
			bodies = append(bodies, body)
			mu.Unlock()
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	dataDir := t.TempDir()
	const sessionID = "e2e-cache-ttl-session"

	// A minimal Claude transcript: one real user prompt followed by a terminal
	// assistant message whose usage carries the cache-creation TTL split
	// (50 tokens written at the 5m TTL, 200 at the 1h TTL; 250 total).
	transcript := filepath.Join(t.TempDir(), "transcript.jsonl")
	lines := `{"type":"user","message":{"role":"user","content":[{"type":"text","text":"hello"}]}}` + "\n" +
		`{"type":"assistant","requestId":"req_1","message":{"role":"assistant","model":"claude-opus-4-8","stop_reason":"end_turn","content":[{"type":"text","text":"hi"}],"usage":{"input_tokens":100,"output_tokens":50,"cache_read_input_tokens":300,"cache_creation_input_tokens":250,"cache_creation":{"ephemeral_5m_input_tokens":50,"ephemeral_1h_input_tokens":200}}}}` + "\n"
	require.NoError(t, os.WriteFile(transcript, []byte(lines), 0o644))

	// UserPromptSubmit creates the turn's trace context; Stop emits the chat span
	// and reads token usage from the transcript we point it at.
	runBinary(t, binary, fmt.Sprintf(`{"hook_event_name":"UserPromptSubmit","session_id":%q,"prompt":"hello"}`, sessionID), dataDir, srv.URL)
	runBinary(t, binary, fmt.Sprintf(`{"hook_event_name":"Stop","session_id":%q,"model":"claude-opus-4-8","stop_reason":"end_turn","transcript_path":%q}`, sessionID, transcript), dataDir, srv.URL)

	time.Sleep(500 * time.Millisecond)

	mu.Lock()
	defer mu.Unlock()

	spans := collectSpans(t, bodies)
	require.NotEmpty(t, spans, "expected spans exported from the Stop hook")

	var chat *otlp.Span
	for i := range spans {
		if strings.HasPrefix(spans[i].Name, "chat") {
			chat = &spans[i]
			break
		}
	}
	require.NotNil(t, chat, "expected a chat span from Stop")

	assert.Equal(t, int64(50), spanIntAttr(t, *chat, "dash0.gen_ai.usage.cache_creation.ephemeral_5m.input_tokens"))
	assert.Equal(t, int64(200), spanIntAttr(t, *chat, "dash0.gen_ai.usage.cache_creation.ephemeral_1h.input_tokens"))
	// The flat total still rides alongside the split, and the split decomposes it.
	assert.Equal(t, int64(250), spanIntAttr(t, *chat, "gen_ai.usage.cache_creation.input_tokens"))
}

// spanIntAttr returns the int64 value of an attribute on a span, failing the
// test if it is missing or not an int (OTLP encodes int64 attributes as strings).
func spanIntAttr(t *testing.T, s otlp.Span, key string) int64 {
	t.Helper()
	for _, a := range s.Attributes {
		if a.Key != key {
			continue
		}
		require.NotNil(t, a.Value.IntValue, "attribute %s is not an int", key)
		n, err := strconv.ParseInt(*a.Value.IntValue, 10, 64)
		require.NoError(t, err, "attribute %s value %q", key, *a.Value.IntValue)
		return n
	}
	t.Fatalf("attribute %s not found on span %q", key, s.Name)
	return 0
}
