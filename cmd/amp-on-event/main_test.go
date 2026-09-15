// SPDX-FileCopyrightText: Copyright 2026 Dash0 Inc.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/dash0hq/dash0-agent-plugin/internal/config"
	"github.com/dash0hq/dash0-agent-plugin/internal/harness"
	"github.com/dash0hq/dash0-agent-plugin/internal/otlp"
)

const envelope = `{"thread_id":"T-test","id":"M-prompt","start":"2026-09-14T12:00:00Z","end":"2026-09-14T12:00:10Z","status":"done","executor":"remote","assistant_ids":["M-a","M-b"],"message":"SECRET","tools":[{"id":"call","name":"shell_command","start":"2026-09-14T12:00:01Z","end":"2026-09-14T12:00:03Z","status":"error","error":"SECRET","input":"SECRET"}]}`
const exportFixture = `{"id":"T-test","messages":[{"messageId":2,"protocolMessageID":"M-a","role":"assistant","state":{"type":"complete"},"usage":{"model":"gpt-6-astra","totalInputTokens":41,"outputTokens":13}},{"messageId":4,"protocolMessageID":"M-b","role":"assistant","state":{"type":"complete"},"usage":{"model":"claude-sonnet-4-6","totalInputTokens":71,"outputTokens":19}}]}`

func setup(t *testing.T) chan []byte {
	t.Helper()
	harness.ResetConfig()
	t.Cleanup(harness.ResetConfig)
	t.Chdir(t.TempDir())
	t.Setenv("HOME", t.TempDir())
	t.Setenv("USERPROFILE", t.TempDir())
	for _, entry := range os.Environ() {
		key, _, _ := strings.Cut(entry, "=")
		if strings.HasPrefix(key, "AMP_PLUGIN_OPTION_") || strings.HasPrefix(key, "DASH0_") {
			t.Setenv(key, "")
		}
	}
	got := make(chan []byte, 4)
	requests := make(chan *http.Request, 4)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests <- r
		data, _ := io.ReadAll(r.Body)
		got <- data
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(func() {
		srv.Close()
		close(requests)
		for r := range requests {
			require.Equal(t, "/v1/traces", r.URL.Path)
			require.True(t, r.Header.Get("Authorization") == "Bearer test-token")
			require.Equal(t, "test-dataset", r.Header.Get("Dash0-Dataset"))
		}
	})
	t.Setenv("AMP_PLUGIN_OPTION_OTLP_URL", srv.URL)
	t.Setenv("AMP_PLUGIN_OPTION_AUTH_TOKEN", "test-token")
	t.Setenv("AMP_PLUGIN_OPTION_DATASET", "test-dataset")
	t.Setenv("AMP_PLUGIN_OPTION_OMIT_IDENTITY_FALLBACK", "true")
	return got
}

func TestAmpHelperExportsPerModelUsageOnce(t *testing.T) {
	got := setup(t)
	t.Setenv("AMP_PLUGIN_OPTION_EXPORT_USAGE", "true")
	require.NoError(t, run(strings.NewReader(envelope), func(id string) ([]byte, error) {
		require.Equal(t, "T-test", id)
		return []byte(exportFixture), nil
	}))
	data := <-got
	require.NotContains(t, string(data), "SECRET")
	var req otlp.ExportTracesRequest
	require.NoError(t, json.Unmarshal(data, &req))
	spans := req.ResourceSpans[0].ScopeSpans[0].Spans
	require.Len(t, spans, 3)
	// The answering message names the turn root; the tool span carries no usage;
	// the earlier model call stays a zero-duration child of the same root.
	require.Equal(t, "chat claude-sonnet-4-6", spans[0].Name)
	require.NotEqual(t, spans[0].StartTimeUnixNano, spans[0].EndTimeUnixNano)
	require.Equal(t, "chat gpt-6-astra", spans[2].Name)
	require.Equal(t, spans[2].StartTimeUnixNano, spans[2].EndTimeUnixNano)
	for i, span := range spans {
		if i > 0 {
			require.Equal(t, spans[0].SpanID, span.ParentSpanID)
		}
	}
	toolEncoded, _ := json.Marshal(spans[1])
	require.NotContains(t, string(toolEncoded), "gen_ai.usage.")
	require.NotContains(t, string(toolEncoded), "gen_ai.request.model")
	for _, tc := range []struct {
		span                           otlp.Span
		model, input, output, provider string
	}{
		{spans[0], "claude-sonnet-4-6", "71", "19", "anthropic"},
		{spans[2], "gpt-6-astra", "41", "13", "openai"},
	} {
		attrs := map[string]otlp.AttrValue{}
		for _, attr := range tc.span.Attributes {
			attrs[attr.Key] = attr.Value
		}
		require.Equal(t, tc.model, *attrs["gen_ai.request.model"].StringValue)
		require.Equal(t, tc.provider, *attrs["gen_ai.provider.name"].StringValue)
		require.Equal(t, tc.input, *attrs["gen_ai.usage.input_tokens"].IntValue)
		require.Equal(t, tc.output, *attrs["gen_ai.usage.output_tokens"].IntValue)
	}
}

// fastPoll removes the wait between reads so a test exercises the loop's
// control flow rather than its clock.
func fastPoll(t *testing.T) {
	t.Helper()
	delay, window := usagePollDelay, usagePollWindow
	usagePollDelay, usagePollWindow = time.Millisecond, 50*time.Millisecond
	t.Cleanup(func() { usagePollDelay, usagePollWindow = delay, window })
}

func TestPartialUsageIsPolledUntilItMatches(t *testing.T) {
	got := setup(t)
	t.Setenv("AMP_PLUGIN_OPTION_EXPORT_USAGE", "true")
	fastPoll(t)
	// Amp returns the thread with NO messages for the first seconds after a
	// turn ends, then materializes it whole. Both stages are replayed here, in
	// that order, because the empty stage is the one the old single re-read
	// landed in every time.
	empty := `{"id":"T-test","messages":[]}`
	pending := `{"id":"T-test","messages":[{"messageId":2,"protocolMessageID":"M-a","role":"assistant","state":{"type":"complete"},"usage":{"model":"gpt-6-astra","totalInputTokens":41,"outputTokens":13}},{"messageId":4,"protocolMessageID":"M-b","role":"assistant","state":{"type":"streaming"}}]}`
	reads := 0
	require.NoError(t, run(strings.NewReader(envelope), func(string) ([]byte, error) {
		reads++
		switch reads {
		case 1, 2:
			return []byte(empty), nil
		case 3:
			return []byte(pending), nil
		}
		return []byte(exportFixture), nil
	}))
	require.Equal(t, 4, reads, "must keep polling through the empty window")
	data := <-got
	require.Contains(t, string(data), `"matched"`)
	require.Contains(t, string(data), "chat claude-sonnet-4-6")
}

func TestUsagePollStopsOnAPermanentVerdict(t *testing.T) {
	// "unsupported" says the export is not about this thread, and "invalid"
	// says the bridge sent an unusable id. Neither improves by waiting, so
	// polling them would just burn the deadline on every turn.
	got := setup(t)
	t.Setenv("AMP_PLUGIN_OPTION_EXPORT_USAGE", "true")
	fastPoll(t)
	reads := 0
	require.NoError(t, run(strings.NewReader(envelope), func(string) ([]byte, error) {
		reads++
		return []byte(`{"id":"T-other","messages":[]}`), nil
	}))
	require.Equal(t, 1, reads)
	require.Contains(t, string(<-got), "unsupported")
}

func TestUsagePollGivesUpAtTheDeadline(t *testing.T) {
	// The turn must still be exported when usage never lands, and the read
	// must not run forever.
	got := setup(t)
	t.Setenv("AMP_PLUGIN_OPTION_EXPORT_USAGE", "true")
	fastPoll(t)
	reads := 0
	require.NoError(t, run(strings.NewReader(envelope), func(string) ([]byte, error) {
		reads++
		return []byte(`{"id":"T-test","messages":[]}`), nil
	}))
	require.Greater(t, reads, 1)
	data := <-got
	require.Contains(t, string(data), `"partial"`)
	require.Contains(t, string(data), `"name":"chat`)
}

func TestTurnWithoutAssistantIDsSkipsTheExportEntirely(t *testing.T) {
	// ReadUsage matches by those IDs, so with none the answer is "unavailable"
	// whatever the export says. Reading it anyway costs the poll's first wait
	// and a whole-thread subprocess for a foregone verdict.
	got := setup(t)
	t.Setenv("AMP_PLUGIN_OPTION_EXPORT_USAGE", "true")
	turn := strings.ReplaceAll(envelope, `"assistant_ids":["M-a","M-b"]`, `"assistant_ids":[]`)
	require.NotEqual(t, envelope, turn)
	start := time.Now()
	require.NoError(t, run(strings.NewReader(turn), func(string) ([]byte, error) {
		t.Fatal("must not export a thread it cannot match")
		return nil, nil
	}))
	require.Less(t, time.Since(start), usagePollDelay)
	require.Contains(t, string(<-got), "unavailable")
}

func TestUsageFailureDoesNotSuppressLifecycle(t *testing.T) {
	for _, failure := range []bool{true, false} {
		t.Run(map[bool]string{true: "unavailable", false: "malformed"}[failure], func(t *testing.T) {
			got := setup(t)
			t.Setenv("AMP_PLUGIN_OPTION_EXPORT_USAGE", "true")
			// An export that always errors leaves the status at "unavailable",
			// which is a poll-again state, so without this the subtest waits
			// out the whole real deadline.
			fastPoll(t)
			require.NoError(t, run(strings.NewReader(envelope), func(string) ([]byte, error) {
				if failure {
					return nil, errors.New("SECRET")
				}
				return []byte(`{"messages":`), nil
			}))
			data := <-got
			require.NotContains(t, string(data), "SECRET")
			require.Contains(t, string(data), `"name":"chat"`)
			require.NotContains(t, string(data), "gen_ai.usage.")
		})
	}
}

func TestUsageDisabledByDefault(t *testing.T) {
	got := setup(t)
	t.Setenv("AMP_PLUGIN_OPTION_EXPORT_USAGE", "")
	t.Setenv("DASH0_EXPORT_USAGE", "true")
	require.NoError(t, os.Mkdir(".amp", 0700))
	require.NoError(t, os.WriteFile(filepath.Join(".amp", config.Name), []byte("---\nexport_usage: true\n---\n"), 0600))
	require.NoError(t, run(strings.NewReader(envelope), func(string) ([]byte, error) { t.Fatal("must not export thread"); return nil, nil }))
	require.Contains(t, string(<-got), "disabled")
}

func TestDisabledConfigurationDoesNotReadInput(t *testing.T) {
	setup(t)
	require.NoError(t, os.Mkdir(".amp", 0700))
	require.NoError(t, os.WriteFile(filepath.Join(".amp", config.Name), []byte("---\nenabled: false\n---\n"), 0600))
	require.NoError(t, run(strings.NewReader("invalid"), func(string) ([]byte, error) { t.Fatal("must not export"); return nil, nil }))
}

func TestRejectMalformedEnvelopes(t *testing.T) {
	setup(t)
	for _, data := range []string{"null", "[]", "{", strings.Repeat("x", (1<<20)+1), strings.ReplaceAll(envelope, "T-test", "../escape"), strings.ReplaceAll(envelope, `"M-prompt"`, `null`)} {
		require.Error(t, run(strings.NewReader(data), func(string) ([]byte, error) { t.Fatal("invalid input reached export"); return nil, nil }))
	}
}
