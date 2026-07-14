// SPDX-FileCopyrightText: Copyright 2026 Dash0 Inc.

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
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/dash0hq/dash0-agent-plugin/internal/otlp"
)

// TestE2EFullFlowWithCodex is the Codex drift canary: it runs the REAL codex
// CLI with our hooks installed exactly the way a customer installs them —
// registered in config.toml and PRE-TRUSTED (no --dangerously-bypass-hook-trust)
// — and asserts that a live session produces Codex telemetry in the shape the
// plugin expects. Unlike the golden test (which replays frozen fixtures), this
// catches Codex-side changes a new version could introduce: payload/event
// renames, hook contract drift, AND hook-trust serialization changes that would
// make our reproduced trusted_hash stop matching (Codex would then silently skip
// the hooks → no spans → this fails). trust_test.go pinpoints hash-algorithm
// drift without a live binary; this proves the whole path against real Codex.
//
// Gated behind the e2e build tag. Like the Claude e2e, it FAILS (not skips)
// when the codex CLI or auth is missing, so a misconfigured secret is loud
// rather than silently disabling the canary. Auth resolution:
//   - OPENAI_API_KEY set  → `codex login --with-api-key` into a temp CODEX_HOME
//     (the CI path; use a service-account key).
//   - otherwise, a local ~/.codex/auth.json is copied into the temp CODEX_HOME
//     (the dev path; reuses an existing `codex login`).
//   - neither → t.Fatal.
//
// Everything runs against a hermetic temp CODEX_HOME so the developer's real
// ~/.codex config is never touched.
func TestE2EFullFlowWithCodex(t *testing.T) {
	codexBin, err := exec.LookPath("codex")
	if err != nil {
		t.Fatal("codex CLI not found in PATH — install with: npm install -g @openai/codex")
	}

	pluginDir := findPluginDir(t)

	// Hermetic Codex home so we never touch the developer's real ~/.codex.
	codexHome := t.TempDir()
	if !authenticateCodex(t, codexBin, codexHome) {
		t.Fatal("no Codex auth available — set OPENAI_API_KEY (CI: a service-account key) or run `codex login` (local)")
	}

	// Build the Codex entrypoint binary.
	binary := filepath.Join(t.TempDir(), "codex-on-event")
	build := exec.Command("go", "build", "-o", binary, "./cmd/codex-on-event")
	build.Dir = pluginDir
	out, err := build.CombinedOutput()
	require.NoError(t, err, "build failed: %s", string(out))

	// Mock OTLP server records every request body.
	var (
		mu     sync.Mutex
		bodies [][]byte
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		bodies = append(bodies, b)
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	pluginData := t.TempDir()

	// Bootstrap wrapper: injects OTLP config into the environment and execs the
	// binary. Codex runs this as the hook command and pipes the event on stdin.
	wrapper := filepath.Join(t.TempDir(), "codex-on-event-wrapper.sh")
	wrapperScript := fmt.Sprintf(`#!/usr/bin/env bash
export DASH0_OTLP_URL=%q
export CODEX_PLUGIN_OPTION_AUTH_TOKEN="e2e-codex-token"
export DASH0_PLUGIN_DATA=%q
export DASH0_OMIT_IO="false"
exec %q
`, srv.URL, pluginData, binary)
	require.NoError(t, os.WriteFile(wrapper, []byte(wrapperScript), 0o755))

	// Install hooks the customer way: register in config.toml AND pre-trust them
	// via the installer's own emit path. The command written must match what we
	// hash, so emit owns both. No bypass flag below — this is the real path.
	writeCodexHooksTrusted(t, codexHome, binary, fmt.Sprintf("bash %q", wrapper))

	// Work in a throwaway git repo so the agent has somewhere to write.
	workDir := t.TempDir()
	gitInit(t, workDir)

	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()

	// NOTE: deliberately NO --dangerously-bypass-hook-trust — the hooks above are
	// pre-trusted, exactly as a customer install leaves them.
	cmd := exec.CommandContext(ctx, codexBin, "exec",
		"-s", "workspace-write",
		"-c", "approval_policy=\"never\"",
		"-C", workDir,
		"Create a file hello.txt containing exactly the text 'hi from codex', then run the shell command 'cat hello.txt'. Keep it brief.",
	)
	cmd.Env = append(os.Environ(), "CODEX_HOME="+codexHome)
	out, err = cmd.CombinedOutput()
	t.Logf("codex exec output (err=%v):\n%s", err, string(out))
	require.NoError(t, err, "codex exec failed")

	// codex exec is synchronous and our hooks POST synchronously, so spans have
	// arrived by now; a short grace covers any straggler.
	time.Sleep(500 * time.Millisecond)

	mu.Lock()
	defer mu.Unlock()

	spans := collectSpans(t, bodies)
	require.NotEmpty(t, spans,
		"no spans from a live Codex session with pre-trusted hooks (no bypass flag). "+
			"If trust_test.go still passes, Codex likely changed hook payloads/events; if it "+
			"fails too, the reproduced trusted_hash no longer matches — see internal/source/codex/trust.go")
	logSpanTree(t, spans)

	var (
		harnessCodex bool
		toolSpan     bool
		chatSpan     bool
		chatHasUsage bool
	)
	for _, s := range spans {
		for _, a := range s.Attributes {
			if a.Key == "gen_ai.harness.name" && a.Value.StringValue != nil && *a.Value.StringValue == "codex" {
				harnessCodex = true
			}
		}
		switch {
		case strings.HasPrefix(s.Name, "execute_tool"):
			toolSpan = true
		case strings.HasPrefix(s.Name, "chat"):
			chatSpan = true
			if spanHasPositiveTokenUsage(s) {
				chatHasUsage = true
			}
		}
	}

	assert.True(t, harnessCodex, "expected a span tagged gen_ai.harness.name=codex")
	assert.True(t, toolSpan, "expected at least one execute_tool span (the agent should run a tool)")
	assert.True(t, chatSpan, "expected a chat span (the turn should close with Stop)")
	// Token usage is read from the session's rollout file (see internal/source/codex/rollout.go).
	// This assertion doubles as a compression drift canary: if a future Codex writes
	// rollouts as .jsonl.zst, the reader emits no usage and this goes red, signalling
	// that zstd support is now required.
	assert.True(t, chatHasUsage, "expected the chat span to carry gen_ai.usage.*_tokens > 0 "+
		"(no usage may mean Codex now writes compressed .jsonl.zst rollouts — see rollout.go)")
	t.Logf("live Codex e2e: %d spans, harness=codex=%v tool=%v chat=%v chatUsage=%v",
		len(spans), harnessCodex, toolSpan, chatSpan, chatHasUsage)
}

// spanHasPositiveTokenUsage reports whether the span carries a gen_ai.usage.*_tokens
// attribute with a positive value (OTLP encodes int64 attributes as strings).
func spanHasPositiveTokenUsage(s otlp.Span) bool {
	for _, a := range s.Attributes {
		if !strings.HasPrefix(a.Key, "gen_ai.usage.") || !strings.HasSuffix(a.Key, "_tokens") {
			continue
		}
		if a.Value.IntValue == nil {
			continue
		}
		if n, err := strconv.ParseInt(*a.Value.IntValue, 10, 64); err == nil && n > 0 {
			return true
		}
	}
	return false
}

// writeCodexHooksTrusted writes CODEX_HOME/config.toml using the installer's own
// emit path: hooks + reproduced [hooks.state] trusted_hash for the given command.
func writeCodexHooksTrusted(t *testing.T, codexHome, binary, command string) {
	t.Helper()
	configPath := filepath.Join(codexHome, "config.toml")
	cmd := exec.Command(binary, "emit-codex-hooks", "--config", configPath, "--command", command)
	block, err := cmd.CombinedOutput()
	require.NoError(t, err, "emit-codex-hooks failed: %s", string(block))
	require.NoError(t, os.WriteFile(configPath, block, 0o644))
}

// authenticateCodex sets up auth inside a hermetic CODEX_HOME. Returns false
// when no auth source is available (the caller then fails).
func authenticateCodex(t *testing.T, codexBin, codexHome string) bool {
	t.Helper()
	if key := os.Getenv("OPENAI_API_KEY"); key != "" {
		cmd := exec.Command(codexBin, "login", "--with-api-key")
		cmd.Env = append(os.Environ(), "CODEX_HOME="+codexHome)
		cmd.Stdin = stringReader(key)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Logf("codex login --with-api-key failed: %v\n%s", err, string(out))
			return false
		}
		return true
	}
	// Dev fallback: reuse an existing local login by copying its auth.json.
	home, err := os.UserHomeDir()
	if err != nil {
		return false
	}
	src := filepath.Join(home, ".codex", "auth.json")
	data, err := os.ReadFile(src)
	if err != nil {
		return false
	}
	return os.WriteFile(filepath.Join(codexHome, "auth.json"), data, 0o600) == nil
}

func gitInit(t *testing.T, dir string) {
	t.Helper()
	for _, args := range [][]string{
		{"init", "-q"},
		{"config", "user.email", "e2e@dash0.com"},
		{"config", "user.name", "Codex E2E"},
		{"commit", "-q", "--allow-empty", "-m", "init"},
	} {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		require.NoError(t, cmd.Run(), "git %v", args)
	}
}

func collectSpans(t *testing.T, bodies [][]byte) []otlp.Span {
	t.Helper()
	var spans []otlp.Span
	for _, b := range bodies {
		var req otlp.ExportTracesRequest
		if err := json.Unmarshal(b, &req); err != nil {
			continue
		}
		for _, rs := range req.ResourceSpans {
			for _, ss := range rs.ScopeSpans {
				spans = append(spans, ss.Spans...)
			}
		}
	}
	return spans
}

// logSpanTree renders the received spans as a parent→child tree for debugging
// (visible with -v and on failure). Spans whose parent wasn't emitted are shown
// at the root so nothing is hidden.
func logSpanTree(t *testing.T, spans []otlp.Span) {
	t.Helper()
	known := make(map[string]bool, len(spans))
	for _, s := range spans {
		known[s.SpanID] = true
	}
	children := map[string][]otlp.Span{}
	for _, s := range spans {
		parent := s.ParentSpanID
		if parent != "" && !known[parent] {
			parent = "" // dangling/external parent → treat as root for display
		}
		children[parent] = append(children[parent], s)
	}

	var b strings.Builder
	b.WriteString(fmt.Sprintf("received %d span(s):\n", len(spans)))
	var walk func(parent, indent string)
	walk = func(parent, indent string) {
		for _, s := range children[parent] {
			b.WriteString(fmt.Sprintf("%s- %s%s\n", indent, s.Name, spanTag(s)))
			walk(s.SpanID, indent+"    ")
		}
	}
	walk("", "  ")
	t.Log(b.String())
}

// spanTag returns a compact suffix of the most useful identity attributes.
func spanTag(s otlp.Span) string {
	var parts []string
	for _, a := range s.Attributes {
		if a.Value.StringValue == nil {
			continue
		}
		switch a.Key {
		case "gen_ai.harness.name", "gen_ai.provider.name", "gen_ai.tool.name", "gen_ai.agent.id":
			parts = append(parts, a.Key+"="+*a.Value.StringValue)
		}
	}
	if len(parts) == 0 {
		return ""
	}
	return "  [" + strings.Join(parts, " ") + "]"
}
