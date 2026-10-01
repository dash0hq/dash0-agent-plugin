// SPDX-FileCopyrightText: Copyright 2026 Dash0 Inc.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bufio"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/dash0hq/dash0-agent-plugin/internal/otlp"
)

const rootSession = "ses_fd56324abffeYUUI5c6tAmJq8L"

// capturedEventsPath points at the normalizer's fixture rather than a second
// copy: this test is about the entrypoint's plumbing, and a divergent copy of
// the same recording would be a second thing to re-capture.
var capturedEventsPath = filepath.Join("..", "..", "internal", "source", "opencode", "testdata", "captured_events.jsonl")

// captured returns the recorded OpenCode event with the given sequence number,
// as the plugin would deliver it: the raw event plus the context the plugin
// resolves in-process.
func captured(t *testing.T, seq int, context map[string]any) string {
	t.Helper()

	file, err := os.Open(capturedEventsPath)
	require.NoError(t, err)
	defer file.Close()

	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for scanner.Scan() {
		var envelope map[string]any
		require.NoError(t, json.Unmarshal(scanner.Bytes(), &envelope))
		if int(envelope["seq"].(float64)) != seq {
			continue
		}
		for key, value := range context {
			envelope[key] = value
		}
		encoded, err := json.Marshal(envelope)
		require.NoError(t, err)
		return string(encoded)
	}
	require.NoError(t, scanner.Err())
	t.Fatalf("no captured event with seq %d", seq)
	return ""
}

func TestCapturedTurnExportsSpans(t *testing.T) {
	dataDir := t.TempDir()
	t.Setenv("OPENCODE_PLUGIN_DATA", dataDir)
	srv, spans := collectingServer(t)
	t.Setenv("DASH0_OTLP_URL", srv.URL)

	context := map[string]any{"root_session_id": rootSession}
	feed(t, captured(t, 1, context))
	feed(t, captured(t, 3, context))
	feed(t, captured(t, 75, context))
	feed(t, captured(t, 183, withAssistant(context)))

	names := make([]string, 0, len(*spans))
	for _, span := range *spans {
		names = append(names, span.Name)
	}
	assert.ElementsMatch(t, []string{"execute_tool read", "chat mock-model"}, names)

	for _, span := range *spans {
		assertStringAttr(t, span.Attributes, "gen_ai.conversation.id", rootSession)
		assertStringAttr(t, span.Attributes, "gen_ai.harness.name", "opencode")
	}
}

// Every event the plugin forwards must leave the binary with a clean exit,
// including the ones the normalizer drops and the ones it cannot parse: the
// wrapper's caller is a live OpenCode session.
func TestExitsCleanly(t *testing.T) {
	dataDir := t.TempDir()
	t.Setenv("OPENCODE_PLUGIN_DATA", dataDir)
	t.Setenv("DASH0_OTLP_URL", "http://127.0.0.1:1")

	t.Run("unreachable endpoint", func(t *testing.T) {
		feed(t, captured(t, 3, map[string]any{"root_session_id": rootSession}))
	})

	t.Run("dropped event", func(t *testing.T) {
		feed(t, captured(t, 7, map[string]any{"root_session_id": rootSession}))
	})

	t.Run("unparseable stdin exits 0", func(t *testing.T) {
		// run() reports the parse failure; main() swallows it into stderr.
		assert.Error(t, runWithStdin("not json"))
	})
}

func withAssistant(context map[string]any) map[string]any {
	out := map[string]any{"assistants": map[string]any{
		rootSession: map[string]any{
			"modelID": "mock-model",
			"mode":    "build",
			"text":    "all done",
			"tokens":  map[string]any{"input": 94, "output": 6, "cache": map[string]any{"read": 0, "write": 0}},
		},
	}}
	for key, value := range context {
		out[key] = value
	}
	return out
}

func feed(t *testing.T, input string) {
	t.Helper()
	require.NoError(t, runWithStdin(input))
}

func runWithStdin(input string) error {
	oldStdin := os.Stdin
	defer func() { os.Stdin = oldStdin }()

	r, w, err := os.Pipe()
	if err != nil {
		return err
	}
	os.Stdin = r
	go func() {
		_, _ = w.WriteString(input)
		w.Close()
	}()

	return run()
}

func collectingServer(t *testing.T) (*httptest.Server, *[]otlp.Span) {
	t.Helper()
	var spans []otlp.Span
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/traces" {
			body, _ := io.ReadAll(r.Body)
			var req otlp.ExportTracesRequest
			if err := json.Unmarshal(body, &req); err == nil {
				for _, rs := range req.ResourceSpans {
					for _, ss := range rs.ScopeSpans {
						spans = append(spans, ss.Spans...)
					}
				}
			}
		}
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)
	return srv, &spans
}

func assertStringAttr(t *testing.T, attrs []otlp.Attribute, key, want string) {
	t.Helper()
	for _, a := range attrs {
		if a.Key == key {
			require.NotNil(t, a.Value.StringValue, "attribute %s: stringValue is nil", key)
			assert.Equal(t, want, *a.Value.StringValue, "attribute %s", key)
			return
		}
	}
	t.Errorf("attribute %s not found", key)
}
