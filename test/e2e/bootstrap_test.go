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
	"regexp"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// scripts/on-event.sh is the bash entry point Claude Code actually invokes. Its
// job is to turn `uname` output into the GOOS/GOARCH-shaped name of a release
// asset, then exec that binary.

// TestBootstrapScript covers the cached-binary path and is credential-free, so
// it runs everywhere the other e2e tests cannot.
func TestBootstrapScript(t *testing.T) {
	pluginDir := findPluginDir(t)
	script := filepath.Join(pluginDir, "scripts", "on-event.sh")

	t.Run("script has no CRLF line endings", func(t *testing.T) {
		// Git for Windows checks out CRLF unless .gitattributes pins LF, and a
		// trailing \r on the shebang makes every hook fail with
		// "bad interpreter: /usr/bin/env bash^M".
		data, err := os.ReadFile(script)
		require.NoError(t, err)
		assert.NotContains(t, string(data), "\r",
			"on-event.sh must keep LF endings — see .gitattributes")
	})

	t.Run("resolves the host binary name and forwards events", func(t *testing.T) {
		dataDir := t.TempDir()
		binDir := filepath.Join(dataDir, "bin")
		require.NoError(t, os.MkdirAll(binDir, 0o755))

		// Pre-place the binary under the name the script is expected to derive.
		// A broken normalization computes a different path, misses the file, and
		// falls through to the GitHub download — which shows up as a non-zero
		// exit (404) and as an unexpected entry in bin/.
		binary := hostBinaryName(t, script)
		build := exec.Command("go", "build", "-o", filepath.Join(binDir, binary), "./cmd/on-event")
		build.Dir = pluginDir
		out, err := build.CombinedOutput()
		require.NoError(t, err, "build failed: %s", out)

		traceReqs := runFullTurn(t, script, dataDir)

		assert.Equal(t, []string{binary}, binDirEntries(t, binDir),
			"script resolved a different binary name and tried to download one")
		assertTurnExported(t, traceReqs)
	})
}

// TestBootstrapScriptDownloadsReleasedBinary lets the script take its real
// download path: fetch the published asset for the configured VERSION, verify
// its checksum, and exec it. This is the only place the asset name the script
// builds is checked against what GoReleaser actually published — and on Windows
// the only proof that the .exe runs.
//
// Gated because it requires the release for the configured VERSION to exist;
// the post-release workflow sets the flag.
func TestBootstrapScriptDownloadsReleasedBinary(t *testing.T) {
	if os.Getenv("DASH0_E2E_RELEASE_DOWNLOAD") == "" {
		t.Skip("set DASH0_E2E_RELEASE_DOWNLOAD=1 — needs the release for the configured VERSION to be published")
	}

	pluginDir := findPluginDir(t)
	script := filepath.Join(pluginDir, "scripts", "on-event.sh")
	dataDir := t.TempDir()

	traceReqs := runFullTurn(t, script, dataDir)

	assert.Equal(t, []string{hostBinaryName(t, script)}, binDirEntries(t, filepath.Join(dataDir, "bin")),
		"downloaded asset does not match the name the script derives")
	assertTurnExported(t, traceReqs)
}

// runFullTurn pipes a full turn's worth of hook events through the bash entry
// point, the way Claude Code does, and returns the captured trace exports.
func runFullTurn(t *testing.T, script, dataDir string) []capturedRequest {
	t.Helper()

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

	for _, event := range []string{
		`{"hook_event_name":"SessionStart","session_id":"bootstrap-e2e","model":"claude-opus-4-7"}`,
		`{"hook_event_name":"UserPromptSubmit","session_id":"bootstrap-e2e","prompt":"hello"}`,
		`{"hook_event_name":"PostToolUse","session_id":"bootstrap-e2e","tool_name":"Bash","tool_use_id":"tu1","duration_ms":100}`,
		`{"hook_event_name":"Stop","session_id":"bootstrap-e2e","model":"claude-opus-4-7","stop_reason":"end_turn"}`,
	} {
		runBootstrap(t, script, event, dataDir, srv.URL)
	}
	time.Sleep(500 * time.Millisecond)

	mu.Lock()
	defer mu.Unlock()
	var traceReqs []capturedRequest
	for _, r := range requests {
		if r.path == "/v1/traces" {
			traceReqs = append(traceReqs, r)
		}
	}
	return traceReqs
}

func assertTurnExported(t *testing.T, traceReqs []capturedRequest) {
	t.Helper()
	// Connectivity check on SessionStart, tool span, chat span.
	assert.GreaterOrEqual(t, len(traceReqs), 3, "expected connectivity check + tool span + chat span")
	for _, r := range traceReqs {
		assert.Equal(t, "Bearer bootstrap-e2e-token", r.auth)
	}
}

func binDirEntries(t *testing.T, binDir string) []string {
	t.Helper()
	entries, err := os.ReadDir(binDir)
	require.NoError(t, err)
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		names = append(names, e.Name())
	}
	return names
}

// hostBinaryName is the cached-binary name on-event.sh must derive for the
// running platform: on-event-<version>-<goos>-<goarch>[.exe]. Release assets
// are named by GOOS/GOARCH, so that is the contract `uname` has to normalize
// into.
func hostBinaryName(t *testing.T, script string) string {
	t.Helper()
	data, err := os.ReadFile(script)
	require.NoError(t, err)
	m := regexp.MustCompile(`(?m)^VERSION="([^"]+)"`).FindStringSubmatch(string(data))
	require.Len(t, m, 2, "could not read VERSION from %s", script)

	name := fmt.Sprintf("on-event-%s-%s-%s", m[1], runtime.GOOS, runtime.GOARCH)
	if runtime.GOOS == "windows" {
		// Git Bash is an x64 build, so `uname -m` reports x86_64 even on ARM64
		// Windows. A GOARCH=arm64 test binary there would legitimately disagree
		// with the script; CI runs amd64.
		name += ".exe"
	}
	return name
}

// runBootstrap pipes one hook event through the bash entry point. HOME is
// redirected at a scratch dir so a developer's own
// ~/.claude/dash0-agent-plugin.local.md cannot override the test's OTLP config.
func runBootstrap(t *testing.T, script, event, dataDir, otlpURL string) {
	t.Helper()
	cmd := exec.Command("bash", script)
	cmd.Stdin = strings.NewReader(event)
	cmd.Dir = t.TempDir()
	cmd.Env = append(os.Environ(),
		"HOME="+t.TempDir(),
		"USERPROFILE="+t.TempDir(),
		"CLAUDE_PLUGIN_DATA="+dataDir,
		"CLAUDE_PLUGIN_OPTION_OTLP_URL="+otlpURL,
		"CLAUDE_PLUGIN_OPTION_AUTH_TOKEN=bootstrap-e2e-token",
		"DASH0_OTLP_URL="+otlpURL,
	)
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "on-event.sh failed for %s: %s", event, out)
}
