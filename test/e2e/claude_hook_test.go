// SPDX-FileCopyrightText: Copyright 2026 Dash0 Inc.
// SPDX-License-Identifier: Apache-2.0

//go:build e2e

package e2e

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/dash0hq/dash0-agent-plugin/internal/otlp"
)

// TestE2EFullFlowWithClaudeRealHook drives the real Claude Code CLI against the
// real scripts/on-event.sh and asserts spans come back.
//
// TestE2EFullFlowWithClaude next door stubs the hook script out — it replaces
// on-event.sh with a two-liner that execs a pre-built binary — so it proves
// Claude fires hooks but says nothing about the bash entry point actually
// shipped to users. This test keeps the real script, which is the only way to
// answer the question Windows support hinges on: can Claude Code execute a .sh
// hook at all on that platform, or does it need a .cmd/.ps1 entry point?
//
// Gated behind DASH0_E2E_CLAUDE_HOOK because it spends real API budget:
//
//	DASH0_E2E_CLAUDE_HOOK=1 ANTHROPIC_API_KEY=sk-... \
//	  go test -tags=e2e -v -timeout=300s -run TestE2EFullFlowWithClaudeRealHook ./test/e2e/
func TestE2EFullFlowWithClaudeRealHook(t *testing.T) {
	if os.Getenv("DASH0_E2E_CLAUDE_HOOK") == "" {
		t.Skip("set DASH0_E2E_CLAUDE_HOOK=1 — drives the real Claude CLI and spends API budget")
	}
	claudeBin, err := exec.LookPath("claude")
	require.NoError(t, err, "claude CLI not found — install with: npm install -g @anthropic-ai/claude-code")
	// The run gets its own CLAUDE_CONFIG_DIR (below), and a claude.ai login
	// lives in that directory, so API-key auth is the only option here.
	require.NotEmpty(t, os.Getenv("ANTHROPIC_API_KEY"),
		"ANTHROPIC_API_KEY is required — the isolated config dir has no claude.ai login")

	pluginDir := findPluginDir(t)

	// Stage the plugin exactly as shipped — crucially including the real
	// scripts/, so Claude invokes the same bash entry point a user gets.
	stageDir := t.TempDir()
	for _, dir := range []string{".claude-plugin", "hooks", "claude", "scripts"} {
		copyDir(t, filepath.Join(pluginDir, dir), filepath.Join(stageDir, dir))
	}

	// Give Claude its own config directory. Without it the run is not hermetic
	// in the way that matters: a plugin of the same name already installed on
	// the machine wins over the --plugin-dir copy, so the staged hooks never
	// fire and the test fails for a reason that has nothing to do with the code
	// under test. It also keeps the run out of the developer's real ~/.claude.
	configDir := t.TempDir()

	// Claude computes CLAUDE_PLUGIN_DATA itself for a --plugin-dir load and
	// ignores any value in the parent environment: it is
	// <config-dir>/plugins/data/<plugin-name>-inline. Pre-place the binary
	// there so on-event.sh finds it instead of downloading a release.
	binary := hostBinaryName(t, filepath.Join(pluginDir, "scripts", "on-event.sh"))
	binDir := filepath.Join(configDir, "plugins", "data", pluginName(t, pluginDir)+"-inline", "bin")
	require.NoError(t, os.MkdirAll(binDir, 0o755))
	binaryPath := filepath.Join(binDir, binary)

	// Stamp a unique service.version into the binary. If Claude ever changes
	// the -inline path convention, on-event.sh would silently download a
	// release binary instead and the spans would still arrive — a false pass.
	// Asserting the marker below makes that failure mode visible.
	marker := fmt.Sprintf("e2e-hook-%d", os.Getpid())
	build := exec.Command("go", "build",
		"-ldflags", "-X github.com/dash0hq/dash0-agent-plugin/internal/version.Version="+marker,
		"-o", binaryPath, "./cmd/on-event")
	build.Dir = pluginDir
	out, err := build.CombinedOutput()
	require.NoError(t, err, "build failed: %s", out)

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

	// Project-level settings outrank the global file, so a developer's own
	// credentials cannot leak this run's spans to a real endpoint.
	workDir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(workDir, ".claude"), 0o755))
	settings := fmt.Sprintf("---\notlp_url: %q\nauth_token: %q\ndataset: \"e2e\"\n---\n",
		srv.URL, "e2e-claude-hook-token")
	require.NoError(t, os.WriteFile(
		filepath.Join(workDir, ".claude", "dash0-agent-plugin.local.md"), []byte(settings), 0o644))

	// Bound the run: the CLI retries an auth failure for ~3 minutes, which turns
	// a bad key into a very slow red test.
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, claudeBin,
		"--print",
		"--plugin-dir", stageDir,
		"--dangerously-skip-permissions",
		"-p", "respond with exactly: hello",
		"--model", "haiku",
		"--max-budget-usd", "0.05",
	)
	cmd.Dir = workDir
	cmd.Env = append(os.Environ(), "CLAUDE_CONFIG_DIR="+configDir)
	output, cmdErr := cmd.CombinedOutput()
	t.Logf("claude output (err=%v): %s", cmdErr, output)
	// Fail on the CLI's own error rather than letting an auth or budget problem
	// surface later as a confusing "no spans" assertion.
	require.NoError(t, cmdErr, "claude CLI failed: %s", output)

	// Hooks flush out of band.
	time.Sleep(3 * time.Second)

	mu.Lock()
	defer mu.Unlock()
	t.Logf("requests received: %d", len(requests))

	var traceReqs []capturedRequest
	for _, r := range requests {
		if r.path == "/v1/traces" {
			traceReqs = append(traceReqs, r)
		}
	}
	require.NotEmpty(t, traceReqs,
		"no /v1/traces requests — Claude Code did not execute scripts/on-event.sh")

	assert.Equal(t, "Bearer e2e-claude-hook-token", traceReqs[0].auth)

	// Separate the two ways this can go wrong, so a failed run says which.
	spans, versions := spanSummary(t, traceReqs)
	require.NotZero(t, spans,
		"only the connectivity check arrived (%d requests, no spans) — the hook ran but the turn produced nothing",
		len(traceReqs))
	assert.Contains(t, versions, marker,
		"spans carry service.version=%v, not %s — they came from some other binary, "+
			"so the pre-placed data dir was not the one Claude used", versions, marker)
}

// pluginName reads .name from .claude-plugin/plugin.json — the value Claude
// uses when it builds the plugin's data directory.
func pluginName(t *testing.T, pluginDir string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(pluginDir, ".claude-plugin", "plugin.json"))
	require.NoError(t, err)
	var manifest struct {
		Name string `json:"name"`
	}
	require.NoError(t, json.Unmarshal(data, &manifest))
	require.NotEmpty(t, manifest.Name)
	return manifest.Name
}

// spanSummary counts the exported spans and collects the service.version values
// they were tagged with. The SessionStart connectivity check posts an empty
// resourceSpans payload and carries no resource attributes, so it contributes
// to neither.
func spanSummary(t *testing.T, reqs []capturedRequest) (int, []string) {
	t.Helper()
	var count int
	seen := map[string]bool{}
	for _, r := range reqs {
		var req otlp.ExportTracesRequest
		if err := json.Unmarshal(r.body, &req); err != nil {
			continue
		}
		for _, rs := range req.ResourceSpans {
			for _, ss := range rs.ScopeSpans {
				count += len(ss.Spans)
			}
			for _, a := range rs.Resource.Attributes {
				if a.Key == "service.version" && a.Value.StringValue != nil {
					seen[*a.Value.StringValue] = true
				}
			}
		}
	}
	versions := make([]string, 0, len(seen))
	for v := range seen {
		versions = append(versions, v)
	}
	sort.Strings(versions)
	return count, versions
}
