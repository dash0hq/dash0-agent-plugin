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
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/dash0hq/dash0-agent-plugin/internal/otlp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fastModeAssistantEntry is a verbatim fast-mode assistant entry captured from a
// Claude Code 2.1.228 transcript: usage.speed is "fast" while service_tier stays
// "standard", and the single iteration carries no speed of its own.
const fastModeAssistantEntry = `{"parentUuid":"2190e11c-b106-4575-bea8-763930ba38b8","isSidechain":false,"message":{"model":"claude-opus-5","id":"msg_011Cdy1sjUv8GY1HkTc7P9VA","type":"message","role":"assistant","content":[{"type":"text","text":"Hey! What can I help you with today?"}],"stop_reason":"end_turn","stop_sequence":null,"stop_details":null,"usage":{"input_tokens":2,"cache_creation_input_tokens":14079,"cache_read_input_tokens":22468,"output_tokens":14,"output_tokens_details":{"thinking_tokens":0},"server_tool_use":{"web_search_requests":0,"web_fetch_requests":0},"service_tier":"standard","cache_creation":{"ephemeral_1h_input_tokens":14079,"ephemeral_5m_input_tokens":0},"inference_geo":"not_available","iterations":[{"input_tokens":2,"output_tokens":14,"cache_read_input_tokens":22468,"cache_creation_input_tokens":14079,"cache_creation":{"ephemeral_5m_input_tokens":0,"ephemeral_1h_input_tokens":14079},"type":"message"}],"speed":"fast"},"diagnostics":null},"requestId":"req_011Cdy1sicaTJgKgbtVzEuSp","type":"assistant","uuid":"607ad17d-ef13-4493-8fbd-c196d153cb53","timestamp":"2026-08-12T14:31:15.834Z","effort":"high","session_id":"f7e275e6-f9a7-4a7b-95ec-dc7021d82b6b","userType":"external","entrypoint":"cli","cwd":"/Users/norbert/dash0/investigations","sessionId":"f7e275e6-f9a7-4a7b-95ec-dc7021d82b6b","version":"2.1.228","gitBranch":"HEAD"}`

// TestE2EUsageBreakdown drives the built on-event binary with a Claude
// transcript whose turn mixes a fast-mode call with a standard one, and asserts
// the Stop chat span exported over OTLP carries the per-mode usage breakdown
// alongside the unchanged totals. Hermetic — no Claude CLI, no API key.
func TestE2EUsageBreakdown(t *testing.T) {
	pluginDir := findPluginDir(t)

	binary := filepath.Join(t.TempDir(), "on-event-fastmode")
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
	const sessionID = "e2e-fast-mode-session"

	// One prompt, then the captured fast-mode call followed by a standard one —
	// the mix a fast-mode cooldown produces mid-turn. The standard call is
	// terminal so the Stop hook sees a complete turn.
	transcript := filepath.Join(t.TempDir(), "transcript.jsonl")
	lines := strings.Join([]string{
		`{"type":"user","message":{"role":"user","content":[{"type":"text","text":"hello"}]}}`,
		fastModeAssistantEntry,
		`{"type":"assistant","requestId":"req_2","message":{"role":"assistant","model":"claude-opus-5","stop_reason":"end_turn","content":[{"type":"text","text":"more"}],"usage":{"input_tokens":8,"output_tokens":26,"cache_creation_input_tokens":1000,"cache_read_input_tokens":500,"cache_creation":{"ephemeral_5m_input_tokens":1000,"ephemeral_1h_input_tokens":0},"service_tier":"standard","speed":"standard"}}}`,
	}, "\n") + "\n"
	require.NoError(t, os.WriteFile(transcript, []byte(lines), 0o644))

	// UserPromptSubmit creates the turn's trace context; Stop emits the chat span
	// and reads token usage from the transcript we point it at.
	runBinary(t, binary, fmt.Sprintf(`{"hook_event_name":"UserPromptSubmit","session_id":%q,"prompt":"hello"}`, sessionID), dataDir, srv.URL)
	runBinary(t, binary, fmt.Sprintf(`{"hook_event_name":"Stop","session_id":%q,"model":"claude-opus-5","stop_reason":"end_turn","transcript_path":%q}`, sessionID, transcript), dataDir, srv.URL)

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

	// Totals cover both calls and are unchanged by the split.
	assert.Equal(t, int64(10), spanIntAttr(t, *chat, "gen_ai.usage.input_tokens"))
	assert.Equal(t, int64(40), spanIntAttr(t, *chat, "gen_ai.usage.output_tokens"))
	assert.Equal(t, int64(15079), spanIntAttr(t, *chat, "gen_ai.usage.cache_creation.input_tokens"))
	assert.Equal(t, int64(22968), spanIntAttr(t, *chat, "gen_ai.usage.cache_read.input_tokens"))

	// The fast entry is the captured call alone; the default-rate entry is the
	// rest, and together they account for every token in the totals above.
	assert.JSONEq(t, `[
		{"speed":"fast","input_tokens":2,"output_tokens":14,
		 "cache_creation_input_tokens":14079,"cache_read_input_tokens":22468,
		 "cache_creation_5m_input_tokens":0,"cache_creation_1h_input_tokens":14079},
		{"input_tokens":8,"output_tokens":26,
		 "cache_creation_input_tokens":1000,"cache_read_input_tokens":500,
		 "cache_creation_5m_input_tokens":1000,"cache_creation_1h_input_tokens":0}
	]`, spanStrAttr(t, *chat, "dash0.gen_ai.usage.breakdown"))
}

// spanStrAttr returns the string value of an attribute on a span, failing the
// test if it is missing or not a string.
func spanStrAttr(t *testing.T, s otlp.Span, key string) string {
	t.Helper()
	for _, a := range s.Attributes {
		if a.Key != key {
			continue
		}
		require.NotNil(t, a.Value.StringValue, "attribute %s is not a string", key)
		return *a.Value.StringValue
	}
	t.Fatalf("attribute %s not found on span %q", key, s.Name)
	return ""
}
