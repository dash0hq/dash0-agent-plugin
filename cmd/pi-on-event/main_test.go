// SPDX-FileCopyrightText: Copyright 2026 Dash0 Inc.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/dash0hq/dash0-agent-plugin/internal/config"
	"github.com/dash0hq/dash0-agent-plugin/internal/harness"
	"github.com/dash0hq/dash0-agent-plugin/internal/otlp"
)

const envelope = `{"session_id":"019a-test","start":"2026-11-13T12:00:00Z","end":"2026-11-13T12:00:10Z","status":"done","prompt":"SECRET","response":"SECRET",` +
	`"calls":[{"provider":"openai","model":"gpt-5.5","start":"2026-11-13T12:00:01Z","end":"2026-11-13T12:00:02Z","stop_reason":"toolUse","usage":{"input":10,"output":2,"cache_read":0,"cache_write":0}},` +
	`{"provider":"claude-code","model":"claude-sonnet-5","start":"2026-11-13T12:00:04Z","end":"2026-11-13T12:00:09Z","stop_reason":"stop","usage":{"input":2,"output":40,"cache_read":300,"cache_write":50}}],` +
	`"tools":[{"id":"call_1","name":"bash","start":"2026-11-13T12:00:02Z","end":"2026-11-13T12:00:03Z","error":true,"call":0,"input":"{\"command\":\"git SECRET\"}","output":"SECRET"}]}`

// setup isolates one helper process: no inherited options, an empty project
// directory and an empty home.
func setup(t *testing.T) (chan []byte, string) {
	t.Helper()
	harness.ResetConfig()
	t.Cleanup(harness.ResetConfig)
	project := t.TempDir()
	t.Chdir(project)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	for _, entry := range os.Environ() {
		key, _, _ := strings.Cut(entry, "=")
		if strings.HasPrefix(key, "PI_") || strings.HasPrefix(key, "DASH0_") {
			t.Setenv(key, "")
		}
	}
	got := make(chan []byte, 4)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		data, _ := io.ReadAll(r.Body)
		if r.URL.Path == "/v1/traces" && r.Header.Get("Authorization") == "Bearer test-token" {
			got <- data
		}
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)
	return got, srv.URL
}

func spansOf(t *testing.T, data []byte) []otlp.Span {
	t.Helper()
	var req otlp.ExportTracesRequest
	require.NoError(t, json.Unmarshal(data, &req))
	return req.ResourceSpans[0].ScopeSpans[0].Spans
}

func TestHelperExportsOneTurnOnce(t *testing.T) {
	got, url := setup(t)
	t.Setenv("PI_PLUGIN_OPTION_OTLP_URL", url)
	t.Setenv("PI_PLUGIN_OPTION_AUTH_TOKEN", "test-token")
	t.Setenv("PI_PLUGIN_OPTION_OMIT_IDENTITY_FALLBACK", "true")
	require.NoError(t, run(strings.NewReader(envelope)))
	data := <-got
	require.Empty(t, got, "one turn is one request")
	// omit_io is on unless configured off.
	require.NotContains(t, string(data), "SECRET")
	spans := spansOf(t, data)
	require.Len(t, spans, 3)
	require.Equal(t, "chat claude-sonnet-5", spans[0].Name)
	require.Equal(t, "chat gpt-5.5", spans[1].Name)
	require.Equal(t, "execute_tool bash", spans[2].Name)
	require.Contains(t, string(data), `"service.name","value":{"stringValue":"pi"}`)
}

// The user-level file is ~/.pi/dash0-agent-plugin.local.md, the same rule as
// every other runtime. A project file in .pi/ wins over it. Pi's own agent
// directory is not read: PI_CODING_AGENT_DIR does not move this file.
func TestConfigFileLocations(t *testing.T) {
	write := func(t *testing.T, dir, body string) {
		t.Helper()
		require.NoError(t, os.MkdirAll(dir, 0o700))
		require.NoError(t, os.WriteFile(filepath.Join(dir, config.Name), []byte(body), 0o600))
	}
	body := func(url string) string {
		return "---\notlp_url: " + url + "\nauth_token: test-token\nomit_identity_fallback: true\n---\n"
	}

	t.Run("home directory", func(t *testing.T) {
		got, url := setup(t)
		write(t, filepath.Join(os.Getenv("HOME"), ".pi"), body(url))
		require.NoError(t, run(strings.NewReader(envelope)))
		require.Len(t, spansOf(t, <-got), 3)
	})

	t.Run("not the agent directory", func(t *testing.T) {
		got, url := setup(t)
		dir := t.TempDir()
		t.Setenv("PI_CODING_AGENT_DIR", dir)
		write(t, dir, body(url))
		write(t, filepath.Join(os.Getenv("HOME"), ".pi", "agent"), body(url))
		require.NoError(t, run(strings.NewReader(envelope)))
		require.Empty(t, got)
	})

	t.Run("project file wins and can disable", func(t *testing.T) {
		got, url := setup(t)
		write(t, filepath.Join(os.Getenv("HOME"), ".pi"), body(url))
		write(t, ".pi", "---\nenabled: false\n---\n")
		require.NoError(t, run(strings.NewReader(envelope)))
		require.Empty(t, got)
	})
}

func TestUnconfiguredHelperReadsNothingAndExportsNothing(t *testing.T) {
	got, _ := setup(t)
	// Not even the envelope is read: an unconfigured install costs nothing.
	require.NoError(t, run(failingReader{}))
	require.Empty(t, got)
}

type failingReader struct{}

func (failingReader) Read([]byte) (int, error) { return 0, io.ErrUnexpectedEOF }

func TestHelperRejectsBadEnvelopes(t *testing.T) {
	for name, input := range map[string]string{
		"not json":  "{",
		"oversized": `{"prompt":"` + strings.Repeat("x", 1<<20) + `"}`,
		"invalid":   `{"session_id":"../x","start":"2026-11-13T12:00:00Z","end":"2026-11-13T12:00:10Z","status":"done"}`,
	} {
		t.Run(name, func(t *testing.T) {
			got, url := setup(t)
			t.Setenv("PI_PLUGIN_OPTION_OTLP_URL", url)
			t.Setenv("PI_PLUGIN_OPTION_AUTH_TOKEN", "test-token")
			require.Error(t, run(strings.NewReader(input)))
			require.Empty(t, got)
		})
	}
}
