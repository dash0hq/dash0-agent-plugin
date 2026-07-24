// SPDX-FileCopyrightText: Copyright 2026 Dash0 Inc.
// SPDX-License-Identifier: Apache-2.0

//go:build e2e

package e2e

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const copilotConvID = "aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee"

type otlpCapture struct {
	mu     sync.Mutex
	bodies [][]byte
	auths  []string
}

func newOTLPCapture(t *testing.T) (*otlpCapture, *httptest.Server) {
	t.Helper()
	c := &otlpCapture{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		c.mu.Lock()
		c.bodies = append(c.bodies, b)
		c.auths = append(c.auths, r.Header.Get("Authorization"))
		c.mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	return c, srv
}

func (c *otlpCapture) snapshot() ([][]byte, []string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([][]byte(nil), c.bodies...), append([]string(nil), c.auths...)
}

func buildCopilotBinary(t *testing.T, pluginDir string) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "copilot-on-event")
	build := exec.Command("go", "build", "-o", bin, "./cmd/copilot-on-event")
	build.Dir = pluginDir
	out, err := build.CombinedOutput()
	require.NoError(t, err, "build failed: %s", out)
	return bin
}

func bootstrapVersion(t *testing.T, pluginDir string) string {
	t.Helper()
	script, err := os.ReadFile(filepath.Join(pluginDir, "copilot", "copilot-on-event.sh"))
	require.NoError(t, err)
	m := regexp.MustCompile(`(?m)^VERSION="([^"]+)"`).FindSubmatch(script)
	require.NotNil(t, m)
	return string(m[1])
}

func stagedChatSpan(dir, conv string) {
	line := fmt.Sprintf(`{"type":"span","name":"chat gpt-5.3-codex","attributes":{"gen_ai.conversation.id":%q,"gen_ai.request.model":"gpt-5.3-codex","gen_ai.usage.input_tokens":14613,"gen_ai.usage.output_tokens":68,"gen_ai.usage.cache_read.input_tokens":14592,"github.copilot.cost":1.0,"gen_ai.output.messages":"[{\"role\":\"assistant\",\"parts\":[{\"type\":\"text\",\"content\":\"Echo complete.\"}]}]"}}`+"\n", conv)
	_ = os.WriteFile(filepath.Join(dir, "otel.jsonl"), []byte(line), 0o644)
}

// TestE2ECopilotPerTurnSpans (L2) feeds a full turn of synthetic camelCase hook
// events through the built binary with a staged native-OTel file, and asserts
// the emitted canonical spans include a chat span carrying per-turn tokens and a
// tool span — all keyed to one conversation, harness github-copilot-cli.
func TestE2ECopilotPerTurnSpans(t *testing.T) {
	pluginDir := findPluginDir(t)
	bin := buildCopilotBinary(t, pluginDir)
	cap, srv := newOTLPCapture(t)
	defer srv.Close()

	pluginData := t.TempDir()
	otelDir := t.TempDir()
	stagedChatSpan(otelDir, copilotConvID)

	run := func(eventName, payload string) {
		cmd := exec.Command(bin, eventName)
		cmd.Env = append(os.Environ(),
			"DASH0_OTLP_URL="+srv.URL,
			"COPILOT_PLUGIN_OPTION_AUTH_TOKEN=e2e-token",
			"COPILOT_PLUGIN_DATA="+pluginData,
			"DASH0_COPILOT_OTEL_DIR="+otelDir,
			"DASH0_OMIT_IO=false",
		)
		cmd.Stdin = strings.NewReader(payload)
		out, err := cmd.CombinedOutput()
		require.NoError(t, err, "%s failed: %s", eventName, out)
	}

	sid := `"sessionId":"` + copilotConvID + `"`
	run("sessionStart", `{`+sid+`,"cwd":"`+t.TempDir()+`","source":"new"}`)
	run("userPromptSubmitted", `{`+sid+`,"prompt":"run echo hi"}`)
	run("postToolUse", `{`+sid+`,"toolName":"bash","toolArgs":{"command":"echo hi"},"toolResult":"hi"}`)
	run("agentStop", `{`+sid+`,"stopReason":"end_turn"}`)

	time.Sleep(200 * time.Millisecond)
	bodies, _ := cap.snapshot()
	spans := collectSpans(t, bodies)
	require.NotEmpty(t, spans)
	logSpanTree(t, spans)

	var chatWithUsage, chatWithResponse, toolSpan, harnessOK bool
	for _, s := range spans {
		for _, a := range s.Attributes {
			if a.Key == "gen_ai.harness.name" && a.Value.StringValue != nil && *a.Value.StringValue == "github-copilot-cli" {
				harnessOK = true
			}
		}
		switch {
		case strings.HasPrefix(s.Name, "chat"):
			if spanHasPositiveTokenUsage(s) {
				chatWithUsage = true
			}
			for _, a := range s.Attributes {
				if a.Key == "gen_ai.output.messages" && a.Value.StringValue != nil && strings.Contains(*a.Value.StringValue, "Echo complete.") {
					chatWithResponse = true
				}
			}
		case strings.HasPrefix(s.Name, "execute_tool"):
			toolSpan = true
		}
	}
	assert.True(t, harnessOK, "expected a span tagged gen_ai.harness.name=github-copilot-cli")
	assert.True(t, toolSpan, "expected an execute_tool span from postToolUse")
	assert.True(t, chatWithUsage, "expected the chat span to carry per-turn gen_ai.usage.*_tokens from the native-OTel file")
	assert.True(t, chatWithResponse, "expected the chat span to carry gen_ai.output.messages (the agent response) from the native-OTel file")
}

// TestE2ECopilotVCSAttributes (L2) proves the binary chdirs into the hook
// payload's `cwd` before vcs.Detect() runs. It invokes the built binary from a
// deliberately NON-repo working directory while the payload's cwd points at a
// throwaway git repo (with a github remote, a committed HEAD, and a distinctive
// local identity), and asserts the emitted spans carry the full dash0.gen_ai.vcs.*
// set plus the repo-local user identity. Without the chdir, git would run in the
// process CWD (not a repo) and only the global user identity would survive — the
// exact regression this guards against.
func TestE2ECopilotVCSAttributes(t *testing.T) {
	pluginDir := findPluginDir(t)
	bin := buildCopilotBinary(t, pluginDir)
	cap, srv := newOTLPCapture(t)
	defer srv.Close()

	// The workspace the payload's cwd will point at: a git repo with a known
	// origin remote and a repo-local identity that differs from any global config.
	repo := t.TempDir()
	gitRepoWithRemote(t, repo, "https://github.com/dash0hq/vcs-e2e.git")

	// The process CWD is a DIFFERENT, non-repo dir — so a green test can only come
	// from the binary honoring the payload's cwd, not from inheriting a repo CWD.
	nonRepo := t.TempDir()

	pluginData := t.TempDir()
	otelDir := t.TempDir()
	stagedOtelTurn(otelDir, copilotConvID)

	run := func(eventName, payload string) {
		cmd := exec.Command(bin, eventName)
		cmd.Dir = nonRepo
		cmd.Env = append(os.Environ(),
			"DASH0_OTLP_URL="+srv.URL,
			"COPILOT_PLUGIN_OPTION_AUTH_TOKEN=e2e-token",
			"COPILOT_PLUGIN_DATA="+pluginData,
			"DASH0_COPILOT_OTEL_DIR="+otelDir,
			"DASH0_OMIT_USER_INFO=false",
		)
		cmd.Stdin = strings.NewReader(payload)
		out, err := cmd.CombinedOutput()
		require.NoError(t, err, "%s failed: %s", eventName, out)
	}

	sid := `"sessionId":"` + copilotConvID + `"`
	cwd := `"cwd":` + strconv.Quote(repo)
	run("sessionStart", `{`+sid+`,`+cwd+`,"source":"new"}`)
	run("userPromptSubmitted", `{`+sid+`,`+cwd+`,"prompt":"run echo hi"}`)
	run("agentStop", `{`+sid+`,`+cwd+`,"stopReason":"end_turn"}`)

	time.Sleep(200 * time.Millisecond)
	bodies, _ := cap.snapshot()
	spans := collectSpans(t, bodies)
	require.NotEmpty(t, spans)
	logSpanTree(t, spans)

	// Union of the vcs/user attributes across every emitted span.
	got := map[string]string{}
	for _, s := range spans {
		for _, a := range s.Attributes {
			if !strings.HasPrefix(a.Key, "dash0.gen_ai.vcs.") && a.Key != "user.name" && a.Key != "user.email" {
				continue
			}
			if a.Value.StringValue != nil {
				got[a.Key] = *a.Value.StringValue
			}
		}
	}

	assert.Equal(t, "https://github.com/dash0hq/vcs-e2e", got["dash0.gen_ai.vcs.repository.url.full"],
		"repository URL must be derived from the payload cwd's origin remote — proves the chdir happened")
	assert.Equal(t, "vcs-e2e", got["dash0.gen_ai.vcs.repository.name"])
	assert.Equal(t, "dash0hq", got["dash0.gen_ai.vcs.owner.name"])
	assert.Equal(t, "github", got["dash0.gen_ai.vcs.provider.name"])
	assert.Equal(t, "branch", got["dash0.gen_ai.vcs.ref.head.type"])
	assert.NotEmpty(t, got["dash0.gen_ai.vcs.ref.head.name"], "branch name requires running git inside the repo")
	assert.NotEmpty(t, got["dash0.gen_ai.vcs.ref.head.revision"], "HEAD revision requires running git inside the repo")
	// The repo-local identity (not any global git config) confirms git ran inside
	// the payload cwd. OMIT_USER_INFO=false, so it's the plain value, not a hash.
	assert.Equal(t, "Copilot E2E", got["user.name"])
	assert.Equal(t, "copilot-e2e@dash0.com", got["user.email"])
}

// TestE2ECopilotDropsSubAgentSessions feeds a full sub-agent lifecycle under a
// synthetic "call_<toolCallId>" session id through the built binary and asserts
// NO spans are emitted — the normalizer drops these so they never mint a spurious
// standalone conversation (their tokens roll into the parent turn instead).
func TestE2ECopilotDropsSubAgentSessions(t *testing.T) {
	bin := buildCopilotBinary(t, findPluginDir(t))
	cap, srv := newOTLPCapture(t)
	defer srv.Close()

	run := func(eventName, payload string) {
		cmd := exec.Command(bin, eventName)
		cmd.Env = append(os.Environ(),
			"DASH0_OTLP_URL="+srv.URL,
			"COPILOT_PLUGIN_OPTION_AUTH_TOKEN=e2e-token",
			"COPILOT_PLUGIN_DATA="+t.TempDir(),
			"DASH0_COPILOT_OTEL_DIR="+t.TempDir(),
		)
		cmd.Stdin = strings.NewReader(payload)
		out, err := cmd.CombinedOutput()
		require.NoError(t, err, "%s failed: %s", eventName, out)
	}

	sub := `"sessionId":"call_s6uW2cBFL6xsNgNWRM66Zx1o"`
	run("userPromptSubmitted", `{`+sub+`,"prompt":"echo hello"}`)
	run("postToolUse", `{`+sub+`,"toolName":"bash","toolArgs":{"command":"echo hello"},"toolResult":"hello"}`)
	run("agentStop", `{`+sub+`,"stopReason":"end_turn"}`)

	time.Sleep(200 * time.Millisecond)
	bodies, _ := cap.snapshot()
	spans := collectSpans(t, bodies)
	assert.Empty(t, spans, "sub-agent (call_) session events must be dropped — no spans emitted")
}

// TestE2ECopilotFailOpen asserts the binary never exits non-zero, even on
// malformed input — mandatory because Copilot's tool hooks are fail-closed.
func TestE2ECopilotFailOpen(t *testing.T) {
	bin := buildCopilotBinary(t, findPluginDir(t))
	cmd := exec.Command(bin, "agentStop")
	cmd.Stdin = strings.NewReader("this is not json")
	cmd.Env = append(os.Environ(), "COPILOT_PLUGIN_DATA="+t.TempDir())
	err := cmd.Run()
	assert.NoError(t, err, "binary must exit 0 on malformed input")
}

// TestE2ECopilotCredentialContracts (L3): the auth token reaches the
// Authorization header both via the config file (through the vendored bootstrap)
// and via the plugin-option env var (direct to the binary).
func TestE2ECopilotCredentialContracts(t *testing.T) {
	pluginDir := findPluginDir(t)
	bin := buildCopilotBinary(t, pluginDir)
	version := bootstrapVersion(t, pluginDir)
	bootstrap := filepath.Join(pluginDir, "copilot", "copilot-on-event.sh")

	// A SessionStart triggers the connectivity check (an OTLP request with auth),
	// so we don't need a staged OTel file for the credential path.
	sessionStart := `{"sessionId":"` + copilotConvID + `","cwd":"` + t.TempDir() + `","source":"new"}`

	t.Run("config file token to wire", func(t *testing.T) {
		cap, srv := newOTLPCapture(t)
		defer srv.Close()

		home := t.TempDir()
		require.NoError(t, os.MkdirAll(filepath.Join(home, ".copilot"), 0o755))
		cfg := fmt.Sprintf("---\notlp_url: %q\nauth_token: \"cfg-token\"\n---\n", srv.URL)
		require.NoError(t, os.WriteFile(filepath.Join(home, ".copilot", "dash0-agent-plugin.local.md"), []byte(cfg), 0o600))

		pdata := t.TempDir()
		binDir := filepath.Join(pdata, "bin")
		require.NoError(t, os.MkdirAll(binDir, 0o755))
		placed := filepath.Join(binDir, fmt.Sprintf("copilot-on-event-%s-%s-%s", version, runtime.GOOS, runtime.GOARCH))
		copyExecutable(t, bin, placed)

		cmd := exec.Command("bash", bootstrap, "sessionStart")
		cmd.Env = append(os.Environ(), "HOME="+home, "COPILOT_PLUGIN_DATA="+pdata)
		cmd.Stdin = strings.NewReader(sessionStart)
		out, err := cmd.CombinedOutput()
		require.NoError(t, err, "bootstrap failed: %s", out)

		time.Sleep(200 * time.Millisecond)
		_, auths := cap.snapshot()
		assert.Contains(t, auths, "Bearer cfg-token", "config-file token must reach the Authorization header")
	})

	t.Run("env token to wire", func(t *testing.T) {
		cap, srv := newOTLPCapture(t)
		defer srv.Close()

		cmd := exec.Command(bin, "sessionStart")
		cmd.Env = append(os.Environ(),
			"DASH0_OTLP_URL="+srv.URL,
			"COPILOT_PLUGIN_OPTION_AUTH_TOKEN=env-token",
			"COPILOT_PLUGIN_DATA="+t.TempDir(),
		)
		cmd.Stdin = strings.NewReader(sessionStart)
		out, err := cmd.CombinedOutput()
		require.NoError(t, err, "binary failed: %s", out)

		time.Sleep(200 * time.Millisecond)
		_, auths := cap.snapshot()
		assert.Contains(t, auths, "Bearer env-token", "plugin-option env token must reach the Authorization header")
	})
}

// TestE2EFullFlowWithCopilot (L6) runs the REAL copilot CLI with the camelCase
// hooks installed and native OTel enabled to a per-session file (both via a
// launch wrapper into a hermetic COPILOT_HOME), and asserts the emitted
// canonical chat spans carry per-turn gen_ai.usage.*. FAILS without a PAT
// (loud, like the Claude/Codex canaries) so a missing token can't hide a break.
func TestE2EFullFlowWithCopilot(t *testing.T) {
	token := os.Getenv("COPILOT_GITHUB_TOKEN")
	if token == "" {
		t.Fatal("COPILOT_GITHUB_TOKEN not set — required for e2e test")
	}
	copilotBin, err := exec.LookPath("copilot")
	if err != nil {
		t.Fatal("copilot CLI not found — install with: npm install -g @github/copilot")
	}

	pluginDir := findPluginDir(t)
	bin := buildCopilotBinary(t, pluginDir)
	cap, srv := newOTLPCapture(t)
	defer srv.Close()

	pluginData := t.TempDir()
	otelDir := t.TempDir()
	otelFile := filepath.Join(otelDir, "otel.jsonl")

	// Hook wrapper: sets the binary's env (incl. DASH0_COPILOT_OTEL_DIR so the
	// reader scans our isolated dir — Copilot doesn't pass env to hooks) and execs
	// the binary, forwarding the event-name argv.
	wrapper := filepath.Join(t.TempDir(), "hook.sh")
	require.NoError(t, os.WriteFile(wrapper, []byte(fmt.Sprintf(`#!/usr/bin/env bash
export DASH0_OTLP_URL=%q
export COPILOT_PLUGIN_OPTION_AUTH_TOKEN="e2e-copilot-token"
export COPILOT_PLUGIN_DATA=%q
export DASH0_COPILOT_OTEL_DIR=%q
exec %q "$@"
`, srv.URL, pluginData, otelDir, bin)), 0o755))

	copilotHome := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(copilotHome, "hooks"), 0o755))
	// camelCase registration with the event name as argv — matches copilot/hooks.json.
	hookJSON := `{"version":1,"hooks":{`
	events := []string{"sessionStart", "userPromptSubmitted", "postToolUse", "agentStop", "sessionEnd"}
	for i, e := range events {
		if i > 0 {
			hookJSON += ","
		}
		hookJSON += fmt.Sprintf(`%q:[{"type":"command","bash":%q,"timeoutSec":10}]`, e, wrapper+" "+e)
	}
	hookJSON += `}}`
	require.NoError(t, os.WriteFile(filepath.Join(copilotHome, "hooks", "dash0.json"), []byte(hookJSON), 0o644))

	workDir := t.TempDir()
	gitInit(t, workDir)

	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, copilotBin, "-p", "Reply with exactly one word: ok", "--allow-all-tools", "-C", workDir)
	cmd.Env = append(os.Environ(),
		"COPILOT_HOME="+copilotHome,
		"COPILOT_GITHUB_TOKEN="+token,
		"COPILOT_OTEL_ENABLED=true",
		"COPILOT_OTEL_FILE_EXPORTER_PATH="+otelFile,
	)
	// Own process group so copilot's exit-time cleanup (which can signal its
	// process group) cannot SIGKILL the test binary that spawned it.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	out, err := cmd.CombinedOutput()
	t.Logf("copilot -p output (err=%v):\n%s", err, out)
	require.NoError(t, err, "copilot -p failed")

	bodies, _ := cap.snapshot()
	spans := collectSpans(t, bodies)
	require.NotEmpty(t, spans, "no spans from a live Copilot session")
	logSpanTree(t, spans)

	chatWithUsage := false
	for _, s := range spans {
		if strings.HasPrefix(s.Name, "chat") && spanHasPositiveTokenUsage(s) {
			chatWithUsage = true
		}
	}
	assert.True(t, chatWithUsage,
		"expected a canonical chat span carrying per-turn gen_ai.usage.*_tokens sourced from the native-OTel file")
}

func copyExecutable(t *testing.T, src, dst string) {
	t.Helper()
	data, err := os.ReadFile(src)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(dst, data, 0o755))
}

// gitRepoWithRemote creates a throwaway git repo with a committed HEAD, a known
// origin remote, and a distinctive repo-local user identity — the workspace a
// Copilot hook payload's cwd points at in the VCS test. The local identity is
// deliberately unlike any global git config so the emitted user.name/email prove
// git ran inside this repo.
func gitRepoWithRemote(t *testing.T, dir, remote string) {
	t.Helper()
	for _, args := range [][]string{
		{"init", "-q"},
		{"config", "user.email", "copilot-e2e@dash0.com"},
		{"config", "user.name", "Copilot E2E"},
		{"remote", "add", "origin", remote},
		{"commit", "-q", "--allow-empty", "-m", "init"},
	} {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		require.NoError(t, cmd.Run(), "git %v", args)
	}
}
