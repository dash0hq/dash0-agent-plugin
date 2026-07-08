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
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestE2EHookInvocation simulates what Claude Code does when firing a hook:
// builds the binary, invokes on-event.sh with a SessionStart event on stdin,
// and verifies the mock OTLP server receives the connectivity check.
func TestE2EHookInvocation(t *testing.T) {
	pluginDir := findPluginDir(t)

	// Build the binary fresh.
	binDir := t.TempDir()
	binary := filepath.Join(binDir, "on-event-test-linux-amd64")
	build := exec.Command("go", "build", "-o", binary, "./cmd/on-event")
	build.Dir = pluginDir
	out, err := build.CombinedOutput()
	require.NoError(t, err, "build failed: %s", string(out))

	var (
		mu       sync.Mutex
		requests []capturedRequest
	)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		requests = append(requests, capturedRequest{
			path:   r.URL.Path,
			auth:   r.Header.Get("Authorization"),
			body:   body,
			method: r.Method,
		})
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	dataDir := t.TempDir()

	t.Run("SessionStart fires connectivity check", func(t *testing.T) {
		event := `{"hook_event_name":"SessionStart","session_id":"e2e-test-session","model":"claude-opus-4-7"}`
		runBinary(t, binary, event, dataDir, srv.URL)

		time.Sleep(500 * time.Millisecond)

		mu.Lock()
		defer mu.Unlock()

		var traceReqs []capturedRequest
		for _, r := range requests {
			if r.path == "/v1/traces" {
				traceReqs = append(traceReqs, r)
			}
		}

		assert.NotEmpty(t, traceReqs, "connectivity check should hit /v1/traces on SessionStart")
		if len(traceReqs) > 0 {
			assert.Equal(t, "Bearer e2e-test-token", traceReqs[0].auth)
		}
	})

	t.Run("full turn produces chat and tool spans", func(t *testing.T) {
		mu.Lock()
		requests = nil
		mu.Unlock()

		// UserPromptSubmit — creates trace context.
		runBinary(t, binary, `{"hook_event_name":"UserPromptSubmit","session_id":"e2e-test-session","prompt":"hello"}`, dataDir, srv.URL)

		// PostToolUse — emits a tool span.
		runBinary(t, binary, `{"hook_event_name":"PostToolUse","session_id":"e2e-test-session","tool_name":"Bash","tool_use_id":"tu1","tool_input":"ls","tool_response":"file.txt","duration_ms":100}`, dataDir, srv.URL)

		// Stop — emits a chat span.
		runBinary(t, binary, `{"hook_event_name":"Stop","session_id":"e2e-test-session","model":"claude-opus-4-7","stop_reason":"end_turn"}`, dataDir, srv.URL)

		time.Sleep(500 * time.Millisecond)

		mu.Lock()
		defer mu.Unlock()

		var traceReqs []capturedRequest
		for _, r := range requests {
			if r.path == "/v1/traces" {
				traceReqs = append(traceReqs, r)
			}
		}

		// Expect at least 2 trace exports: tool span + chat span.
		assert.GreaterOrEqual(t, len(traceReqs), 2, "expected tool span + chat span")

		// Verify spans contain expected attributes.
		for _, r := range traceReqs {
			body := string(r.body)
			assert.Contains(t, body, "e2e-test-session", "span should contain conversation ID")
		}
	})

	t.Run("SessionEnd cleans up session directory", func(t *testing.T) {
		sessionDir := filepath.Join(dataDir, "e2e-test-session")
		require.DirExists(t, sessionDir, "session dir should exist before SessionEnd")

		runBinary(t, binary, `{"hook_event_name":"SessionEnd","session_id":"e2e-test-session"}`, dataDir, srv.URL)

		assert.NoDirExists(t, sessionDir, "session dir should be cleaned up after SessionEnd")
	})
}

func TestE2EFullFlowWithClaude(t *testing.T) {
	claudeBin, err := exec.LookPath("claude")
	if err != nil {
		t.Fatal("claude CLI not found in PATH — install with: npm install -g @anthropic-ai/claude-code")
	}
	if os.Getenv("ANTHROPIC_API_KEY") == "" {
		t.Fatal("ANTHROPIC_API_KEY not set — required for e2e test")
	}

	pluginDir := findPluginDir(t)

	// Build the binary.
	binary := filepath.Join(t.TempDir(), "on-event")
	build := exec.Command("go", "build", "-o", binary, "./cmd/on-event")
	build.Dir = pluginDir
	out, err := build.CombinedOutput()
	require.NoError(t, err, "build failed: %s", string(out))

	var (
		mu       sync.Mutex
		requests []capturedRequest
	)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		requests = append(requests, capturedRequest{
			path:   r.URL.Path,
			auth:   r.Header.Get("Authorization"),
			body:   body,
			method: r.Method,
		})
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	// Create a staging copy of the plugin with a simplified on-event.sh
	// that invokes the pre-built binary directly (no GitHub download).
	stageDir := t.TempDir()
	copyDir(t, filepath.Join(pluginDir, ".claude-plugin"), filepath.Join(stageDir, ".claude-plugin"))
	copyDir(t, filepath.Join(pluginDir, "hooks"), filepath.Join(stageDir, "hooks"))
	copyDir(t, filepath.Join(pluginDir, "claude"), filepath.Join(stageDir, "claude"))
	require.NoError(t, os.MkdirAll(filepath.Join(stageDir, "scripts"), 0o755))

	// Write a hook script that sets env vars and execs the pre-built binary.
	// We export CLAUDE_PLUGIN_OPTION_* so the binary sees the OTLP config
	// without needing the download/config-file logic from the real on-event.sh.
	hookScript := fmt.Sprintf(`#!/usr/bin/env bash
export CLAUDE_PLUGIN_OPTION_OTLP_URL="${CLAUDE_PLUGIN_OPTION_OTLP_URL:-%s}"
export CLAUDE_PLUGIN_OPTION_AUTH_TOKEN="${CLAUDE_PLUGIN_OPTION_AUTH_TOKEN:-e2e-test-token}"
exec %q "$@"
`, srv.URL, binary)
	require.NoError(t, os.WriteFile(filepath.Join(stageDir, "scripts", "on-event.sh"), []byte(hookScript), 0o755))

	workDir := t.TempDir()

	// Also place a .env as fallback (binary reads CWD/.env via dotenv.Load).
	envContent := fmt.Sprintf("DASH0_OTLP_URL=%s\nDASH0_AUTH_TOKEN=e2e-test-token\n", srv.URL)
	require.NoError(t, os.WriteFile(filepath.Join(workDir, ".env"), []byte(envContent), 0o644))

	cmd := exec.Command(claudeBin,
		"--print",
		"--plugin-dir", stageDir,
		"--dangerously-skip-permissions",
		"-p", "respond with exactly: hello",
		"--model", "haiku",
		"--max-budget-usd", "0.05",
	)
	cmd.Dir = workDir
	cmd.Env = os.Environ()

	output, cmdErr := cmd.CombinedOutput()
	t.Logf("claude output (err=%v): %s", cmdErr, string(output))

	// Give hooks time to flush.
	time.Sleep(3 * time.Second)

	mu.Lock()
	defer mu.Unlock()

	t.Logf("requests received: %d", len(requests))
	for _, r := range requests {
		t.Logf("  %s %s auth=%q (%d bytes)", r.method, r.path, r.auth, len(r.body))
	}

	require.NotEmpty(t, requests, "expected at least one OTLP request from the Claude session with the plugin loaded")

	var traceReqs []capturedRequest
	for _, r := range requests {
		if r.path == "/v1/traces" {
			traceReqs = append(traceReqs, r)
		}
	}
	assert.NotEmpty(t, traceReqs, "expected at least one /v1/traces request (connectivity check on SessionStart)")
}

// copyDir recursively copies src to dst.
func copyDir(t *testing.T, src, dst string) {
	t.Helper()
	require.NoError(t, filepath.Walk(src, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, path)
		target := filepath.Join(dst, rel)
		if info.IsDir() {
			return os.MkdirAll(target, info.Mode())
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(target, data, info.Mode())
	}))
}

type capturedRequest struct {
	path   string
	auth   string
	body   []byte
	method string
}

func runBinary(t *testing.T, binary, event, dataDir, otlpURL string) {
	t.Helper()
	cmd := exec.Command(binary)
	cmd.Stdin = stringReader(event)
	cmd.Env = []string{
		"CLAUDE_PLUGIN_DATA=" + dataDir,
		"CLAUDE_PLUGIN_OPTION_OTLP_URL=" + otlpURL,
		"CLAUDE_PLUGIN_OPTION_AUTH_TOKEN=e2e-test-token",
		"CLAUDE_PLUGIN_OPTION_OMIT_USER_INFO=false",
		"CLAUDE_PLUGIN_OPTION_OMIT_IO=false",
		"HOME=" + os.Getenv("HOME"),
		"PATH=" + os.Getenv("PATH"),
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Logf("binary output: %s (err: %v)", string(out), err)
	}
}

func stringReader(s string) *os.File {
	r, w, _ := os.Pipe()
	go func() {
		w.Write([]byte(s))
		w.Close()
	}()
	return r
}

func findPluginDir(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	require.NoError(t, err)
	for {
		if _, err := os.Stat(filepath.Join(dir, ".claude-plugin", "plugin.json")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("could not find plugin root (no .claude-plugin/plugin.json)")
		}
		dir = parent
	}
}
