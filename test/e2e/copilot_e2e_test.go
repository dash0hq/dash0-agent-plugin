// SPDX-FileCopyrightText: Copyright 2026 Dash0 Inc.
// SPDX-License-Identifier: Apache-2.0

//go:build e2e

package e2e

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
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
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/dash0hq/dash0-agent-plugin/internal/otlp"
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

// stagedOtelTurn writes a realistic native-OTel file for one turn, mirroring a
// real capture: an invoke_agent root, a chat span (usage + response), a
// top-level bash tool (real 0.5s duration), and a task spawn whose sub-agent
// runs its own bash — the sub-agent's tool hangs off an invoke_agent-task layer
// that the plugin must collapse.
func stagedOtelTurn(dir, conv string) {
	const trace = "11111111111111111111111111111111"
	lines := []string{
		// chat span: usage/model/response for the turn.
		fmt.Sprintf(`{"type":"span","traceId":"%s","spanId":"aaaaaaaaaaaaaa01","parentSpanId":"aaaaaaaaaaaaaa00","name":"chat gpt-5.3-codex","startTime":[1000,0],"endTime":[1001,0],"status":{"code":0},"attributes":{"gen_ai.conversation.id":%q,"gen_ai.request.model":"gpt-5.3-codex","gen_ai.usage.input_tokens":14613,"gen_ai.usage.output_tokens":68,"gen_ai.usage.cache_read.input_tokens":14592,"github.copilot.cost":1.0,"gen_ai.output.messages":"[{\"role\":\"assistant\",\"parts\":[{\"type\":\"text\",\"content\":\"Echo complete.\"}]}]"}}`, trace, conv),
		// top-level bash: 0.5s real duration.
		fmt.Sprintf(`{"type":"span","traceId":"%s","spanId":"aaaaaaaaaaaaaa02","parentSpanId":"aaaaaaaaaaaaaa00","name":"execute_tool bash","startTime":[1001,0],"endTime":[1001,500000000],"status":{"code":0},"attributes":{"gen_ai.tool.name":"bash","gen_ai.tool.call.id":"call_top","gen_ai.tool.call.arguments":"{\"command\":\"echo hi\"}","gen_ai.tool.call.result":"hi"}}`, trace),
		// sub-agent's bash, under the invoke_agent-task layer.
		fmt.Sprintf(`{"type":"span","traceId":"%s","spanId":"aaaaaaaaaaaaaa05","parentSpanId":"aaaaaaaaaaaaaa04","name":"execute_tool bash","startTime":[1002,0],"endTime":[1002,250000000],"status":{"code":0},"attributes":{"gen_ai.tool.name":"bash","gen_ai.tool.call.id":"call_sub","gen_ai.tool.call.arguments":"{\"command\":\"echo hello\"}","gen_ai.tool.call.result":"hello"}}`, trace),
		// the sub-agent root (collapsed by the plugin).
		fmt.Sprintf(`{"type":"span","traceId":"%s","spanId":"aaaaaaaaaaaaaa04","parentSpanId":"aaaaaaaaaaaaaa03","name":"invoke_agent task","startTime":[1001,600000000],"endTime":[1003,0],"status":{"code":0},"attributes":{"gen_ai.conversation.id":%q}}`, trace, conv),
		// the task spawn itself.
		fmt.Sprintf(`{"type":"span","traceId":"%s","spanId":"aaaaaaaaaaaaaa03","parentSpanId":"aaaaaaaaaaaaaa00","name":"execute_tool task","startTime":[1001,600000000],"endTime":[1003,100000000],"status":{"code":0},"attributes":{"gen_ai.tool.name":"task","gen_ai.tool.call.id":"call_spawn","gen_ai.tool.call.arguments":"{\"agent_type\":\"task\",\"name\":\"echo-runner\"}","gen_ai.tool.call.result":"done"}}`, trace),
		// the turn's invoke_agent root.
		fmt.Sprintf(`{"type":"span","traceId":"%s","spanId":"aaaaaaaaaaaaaa00","parentSpanId":"","name":"invoke_agent","startTime":[1000,0],"endTime":[1004,0],"status":{"code":0},"attributes":{"gen_ai.conversation.id":%q}}`, trace, conv),
	}
	_ = os.WriteFile(filepath.Join(dir, "otel.jsonl"), []byte(strings.Join(lines, "\n")+"\n"), 0o644)
}

// TestE2ECopilotPerTurnSpans (L2) feeds a turn of synthetic camelCase hook
// events through the built binary with a staged native-OTel file, and asserts
// the emitted canonical spans: a chat span carrying per-turn tokens + response,
// and OTel-sourced execute_tool spans with REAL durations — the top-level tool
// under the chat span, the sub-agent's tool nested under its `task` spawn.
func TestE2ECopilotPerTurnSpans(t *testing.T) {
	pluginDir := findPluginDir(t)
	bin := buildCopilotBinary(t, pluginDir)
	cap, srv := newOTLPCapture(t)
	defer srv.Close()

	pluginData := t.TempDir()
	otelDir := t.TempDir()
	stagedOtelTurn(otelDir, copilotConvID)

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
	run("agentStop", `{`+sid+`,"stopReason":"end_turn"}`)

	time.Sleep(200 * time.Millisecond)
	bodies, _ := cap.snapshot()
	spans := collectSpans(t, bodies)
	require.NotEmpty(t, spans)
	logSpanTree(t, spans)

	var chatWithUsage, chatWithResponse, harnessOK bool
	chatSpanID := ""
	tools := map[string]otlp.Span{} // by native span id
	for _, s := range spans {
		for _, a := range s.Attributes {
			if a.Key == "gen_ai.harness.name" && a.Value.StringValue != nil && *a.Value.StringValue == "github-copilot-cli" {
				harnessOK = true
			}
		}
		switch {
		case strings.HasPrefix(s.Name, "chat"):
			chatSpanID = s.SpanID
			if spanHasPositiveTokenUsage(s) {
				chatWithUsage = true
			}
			for _, a := range s.Attributes {
				if a.Key == "gen_ai.output.messages" && a.Value.StringValue != nil && strings.Contains(*a.Value.StringValue, "Echo complete.") {
					chatWithResponse = true
				}
			}
		case strings.HasPrefix(s.Name, "execute_tool"):
			tools[s.SpanID] = s
		}
	}
	assert.True(t, harnessOK, "expected a span tagged gen_ai.harness.name=github-copilot-cli")
	assert.True(t, chatWithUsage, "expected the chat span to carry per-turn gen_ai.usage.*_tokens from the native-OTel file")
	assert.True(t, chatWithResponse, "expected the chat span to carry gen_ai.output.messages (the agent response) from the native-OTel file")

	require.Len(t, tools, 3, "all execute_tool spans (top-level bash, task spawn, sub-agent bash) must be emitted from the native-OTel file")
	topBash, ok := tools["aaaaaaaaaaaaaa02"]
	require.True(t, ok, "top-level bash keeps its native span id")
	assert.Equal(t, chatSpanID, topBash.ParentSpanID, "top-level tool must parent under the turn's chat span")
	assert.Equal(t, "execute_tool bash", topBash.Name)
	assert.NotEqual(t, topBash.StartTimeUnixNano, topBash.EndTimeUnixNano, "tool spans must carry the REAL duration, not a zero-length instant")

	task, ok := tools["aaaaaaaaaaaaaa03"]
	require.True(t, ok, "task spawn emitted")
	assert.Equal(t, chatSpanID, task.ParentSpanID)
	taskName := ""
	for _, a := range task.Attributes {
		if a.Key == "dash0.gen_ai.tool.task.name" && a.Value.StringValue != nil {
			taskName = *a.Value.StringValue
		}
	}
	assert.Equal(t, "echo-runner", taskName, "task spans carry their instance name")

	subBash, ok := tools["aaaaaaaaaaaaaa05"]
	require.True(t, ok, "sub-agent tool emitted")
	assert.Equal(t, "aaaaaaaaaaaaaa03", subBash.ParentSpanID, "sub-agent tool must nest under its spawning task span (invoke_agent layer collapsed)")
}

// TestE2ECopilotDefersTurnWhenTraceContextMissing (L2) guards the F1 invariant:
// when a Stop lands without an intact trace context (only sessionStart ran, so no
// TraceID was minted), the turn must be DEFERRED, not consumed — no chat/tool
// spans emit and the native-OTel cursor is left untouched — so the turn's usage
// and tools fold into the next valid turn instead of being silently dropped.
func TestE2ECopilotDefersTurnWhenTraceContextMissing(t *testing.T) {
	pluginDir := findPluginDir(t)
	bin := buildCopilotBinary(t, pluginDir)
	cap, srv := newOTLPCapture(t)
	defer srv.Close()

	pluginData := t.TempDir()
	otelDir := t.TempDir()
	stagedOtelTurn(otelDir, copilotConvID)

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

	countKinds := func() (chat, tools int) {
		bodies, _ := cap.snapshot()
		for _, s := range collectSpans(t, bodies) {
			switch {
			case strings.HasPrefix(s.Name, "chat"):
				chat++
			case strings.HasPrefix(s.Name, "execute_tool"):
				tools++
			}
		}
		return chat, tools
	}

	sid := `"sessionId":"` + copilotConvID + `"`
	cursorFile := filepath.Join(pluginData, copilotConvID, "otel_cursor.json")

	// Phase 1: sessionStart records only SessionID (no TraceID minted), so a Stop
	// now has no intact context. The turn must be deferred.
	run("sessionStart", `{`+sid+`,"cwd":"`+t.TempDir()+`","source":"new"}`)
	run("agentStop", `{`+sid+`,"stopReason":"end_turn"}`)

	time.Sleep(200 * time.Millisecond)
	chat, tools := countKinds()
	assert.Zero(t, chat, "a Stop without trace context must emit no chat span")
	assert.Zero(t, tools, "a Stop without trace context must emit no tool spans")
	require.NoFileExists(t, cursorFile, "the OTel cursor must not advance when the turn is deferred")

	// Phase 2: a normal turn (userPromptSubmitted mints the trace) re-reads the
	// SAME native-OTel spans — the cursor never moved — and emits them now.
	run("userPromptSubmitted", `{`+sid+`,"prompt":"run echo hi"}`)
	run("agentStop", `{`+sid+`,"stopReason":"end_turn"}`)

	time.Sleep(200 * time.Millisecond)
	chat, tools = countKinds()
	assert.Equal(t, 1, chat, "the deferred turn's chat span emits on the next valid turn")
	assert.Equal(t, 3, tools, "the deferred turn's tool spans fold into the next valid turn")
	assert.FileExists(t, cursorFile, "the cursor advances once the turn is actually emitted")
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
			if !strings.HasPrefix(a.Key, "dash0.gen_ai.vcs.") && !strings.HasPrefix(a.Key, "user.") &&
				a.Key != "dash0.gen_ai.user.identity.source" {
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
	assert.Equal(t, "git", got["dash0.gen_ai.user.identity.source"],
		"a configured git identity must declare itself as such, not as the OS fallback")
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
	run("agentStop", `{`+sub+`,"stopReason":"end_turn"}`)

	time.Sleep(200 * time.Millisecond)
	bodies, _ := cap.snapshot()
	spans := collectSpans(t, bodies)
	assert.Empty(t, spans, "sub-agent (call_) session events must be dropped — no spans emitted")
}

// TestE2ECopilotSystemNotificationInput asserts that a Copilot-injected
// <system_notification> turn (fired as a synthetic userPromptSubmitted, e.g.
// when a sub-agent goes idle) renders its input message with role "assistant",
// not "user" — it's agent-side context, not something the user typed.
func TestE2ECopilotSystemNotificationInput(t *testing.T) {
	pluginDir := findPluginDir(t)
	bin := buildCopilotBinary(t, pluginDir)
	cap, srv := newOTLPCapture(t)
	defer srv.Close()

	pluginData := t.TempDir()
	otelDir := t.TempDir()

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
	run("userPromptSubmitted", `{`+sid+`,"prompt":"<system_notification>\nAgent \"time-ticker\" (task) has finished processing and is now idle."}`)
	run("agentStop", `{`+sid+`,"stopReason":"end_turn"}`)

	time.Sleep(200 * time.Millisecond)
	bodies, _ := cap.snapshot()
	spans := collectSpans(t, bodies)
	require.NotEmpty(t, spans)

	var input string
	for _, s := range spans {
		if strings.HasPrefix(s.Name, "chat") {
			for _, a := range s.Attributes {
				if a.Key == "gen_ai.input.messages" && a.Value.StringValue != nil {
					input = *a.Value.StringValue
				}
			}
		}
	}
	require.NotEmpty(t, input, "chat span must carry gen_ai.input.messages")
	assert.Contains(t, input, `"role":"assistant"`, "a <system_notification> turn must render as an assistant-role input message")
	assert.NotContains(t, input, `"role":"user"`, "must not be labeled as user input")
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

	t.Run("project config overrides global", func(t *testing.T) {
		cap, srv := newOTLPCapture(t)
		defer srv.Close()

		// Global config carries a token that must NOT win.
		home := t.TempDir()
		require.NoError(t, os.MkdirAll(filepath.Join(home, ".copilot"), 0o755))
		globalCfg := fmt.Sprintf("---\notlp_url: %q\nauth_token: \"global-token\"\n---\n", srv.URL)
		require.NoError(t, os.WriteFile(filepath.Join(home, ".copilot", "dash0-agent-plugin.local.md"), []byte(globalCfg), 0o600))

		// Project-level config in the workspace CWD must take precedence.
		workspace := t.TempDir()
		require.NoError(t, os.MkdirAll(filepath.Join(workspace, ".copilot"), 0o755))
		projectCfg := fmt.Sprintf("---\notlp_url: %q\nauth_token: \"project-token\"\n---\n", srv.URL)
		require.NoError(t, os.WriteFile(filepath.Join(workspace, ".copilot", "dash0-agent-plugin.local.md"), []byte(projectCfg), 0o600))

		pdata := t.TempDir()
		binDir := filepath.Join(pdata, "bin")
		require.NoError(t, os.MkdirAll(binDir, 0o755))
		placed := filepath.Join(binDir, fmt.Sprintf("copilot-on-event-%s-%s-%s", version, runtime.GOOS, runtime.GOARCH))
		copyExecutable(t, bin, placed)

		cmd := exec.Command("bash", bootstrap, "sessionStart")
		cmd.Dir = workspace // bootstrap resolves the project file relative to CWD
		cmd.Env = append(os.Environ(), "HOME="+home, "COPILOT_PLUGIN_DATA="+pdata)
		cmd.Stdin = strings.NewReader(sessionStart)
		out, err := cmd.CombinedOutput()
		require.NoError(t, err, "bootstrap failed: %s", out)

		time.Sleep(200 * time.Millisecond)
		_, auths := cap.snapshot()
		assert.Contains(t, auths, "Bearer project-token", "project-level config must take precedence over global")
		assert.NotContains(t, auths, "Bearer global-token", "global token must not be used when a project file is present")
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
	events := []string{"sessionStart", "userPromptSubmitted", "agentStop", "sessionEnd"}
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
	setNewProcessGroup(cmd)
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

// TestE2ECopilotMarketplaceInstall validates the self-hosted Copilot marketplace:
// `copilot plugin marketplace add <repo>` + `copilot plugin install
// dash0-agent-plugin@dash0` must index and install the plugin declared in
// .github/plugin/marketplace.json. The static consistency test proves the JSON is
// well-formed and matches the manifest, but only the real copilot CLI proves it
// actually resolves the `source` path and installs the package.
//
// The CLI serves an installed plugin in one of two shapes, and both are valid:
// it copies the package into <COPILOT_HOME>/installed-plugins, or — since 1.0.81,
// for a marketplace whose source is a local directory — it loads the plugin live
// from that directory and copies nothing. So this asserts on the location the CLI
// records, never on a fixed layout.
//
// No auth/LLM session is needed (marketplace add + install only resolve + link or
// copy), so this needs no COPILOT_GITHUB_TOKEN. Gated behind the e2e build tag;
// FAILS (not skips) if the copilot CLI is missing so a misconfigured CI is loud.
func TestE2ECopilotMarketplaceInstall(t *testing.T) {
	copilotBin, err := exec.LookPath("copilot")
	require.NoError(t, err, "copilot CLI not found — install with: npm install -g @github/copilot")

	repoRoot := findPluginDir(t) // holds .github/plugin/marketplace.json
	copilotHome := t.TempDir()   // hermetic — never touches the developer's ~/.copilot
	env := append(os.Environ(), "COPILOT_HOME="+copilotHome)

	run := func(args ...string) (string, error) {
		cmd := exec.Command(copilotBin, args...)
		cmd.Env = env
		out, err := cmd.CombinedOutput()
		// Log every invocation. Both commands can exit 0 and still do nothing,
		// so a CI failure needs their output even on the success path.
		t.Logf("copilot %s (err=%v):\n%s", strings.Join(args, " "), err, out)
		return string(out), err
	}

	// The CLI is unpinned (npm install -g @github/copilot), and its install
	// behaviour differs between builds, so record which build produced the result.
	_, _ = run("--version")

	// 1. Register the repo as a marketplace (its .github/plugin/marketplace.json
	//    lists dash0-agent-plugin with source "./copilot").
	out, err := run("plugin", "marketplace", "add", repoRoot)
	require.NoError(t, err, "marketplace add failed:\n%s", out)

	// 2. Install must succeed via <plugin>@<marketplace>.
	out, err = run("plugin", "install", "dash0-agent-plugin@dash0")
	require.NoError(t, err, "plugin install failed:\n%s", out)

	// 3. The installed plugin must carry the manifest, camelCase hooks, and bootstrap,
	//    wherever the CLI decided to serve it from.
	_, _ = run("plugin", "list")
	root := installedPluginPath(t, copilotHome, "dash0", "dash0-agent-plugin")
	for _, f := range []string{"plugin.json", "hooks.json", "copilot-on-event.sh"} {
		_, statErr := os.Stat(filepath.Join(root, f))
		require.NoError(t, statErr, "installed plugin missing %s", f)
	}

	// 4. The dev-only capture harness must NOT be part of the plugin package.
	_, statErr := os.Stat(filepath.Join(root, "capture"))
	require.True(t, os.IsNotExist(statErr), "capture/ must not ship inside the installed plugin")
}

// installedPluginPath returns the directory the copilot CLI serves the installed
// plugin from. A copied install records a `cache_path` in <copilotHome>/config.json.
// A live install (local-directory marketplace, CLI 1.0.81 and later) copies nothing
// and records only the marketplace path and the enablement in settings.json, so the
// path is the plugin's declared `source` inside that marketplace. It fails with a
// dump of both candidate homes when neither record exists — an install that exits 0
// and writes nothing is otherwise silent.
func installedPluginPath(t *testing.T, copilotHome, marketplace, name string) string {
	t.Helper()

	if raw, err := os.ReadFile(filepath.Join(copilotHome, "config.json")); err == nil {
		var config struct {
			InstalledPlugins []struct {
				Name      string `json:"name"`
				CachePath string `json:"cache_path"`
			} `json:"installedPlugins"`
		}
		decodeCopilotJSON(t, raw, &config)
		for _, p := range config.InstalledPlugins {
			if p.Name == name && p.CachePath != "" {
				return p.CachePath
			}
		}
	}

	settingsPath := filepath.Join(copilotHome, "settings.json")
	raw, err := os.ReadFile(settingsPath)
	if err != nil {
		dumpCopilotHomes(t, copilotHome)
		t.Fatalf("copilot recorded no install of %s: %v", name, err)
	}
	var settings struct {
		ExtraKnownMarketplaces map[string]struct {
			Source struct {
				Path string `json:"path"`
			} `json:"source"`
		} `json:"extraKnownMarketplaces"`
		EnabledPlugins map[string]bool `json:"enabledPlugins"`
	}
	decodeCopilotJSON(t, raw, &settings)

	ref := name + "@" + marketplace
	if !settings.EnabledPlugins[ref] || settings.ExtraKnownMarketplaces[marketplace].Source.Path == "" {
		dumpCopilotHomes(t, copilotHome)
		t.Fatalf("%s records no live install of %s:\n%s", settingsPath, ref, raw)
	}

	// A live install serves the plugin straight out of the marketplace, so the
	// path is the `source` the marketplace declares for it. Resolving it here is
	// what proves the declared source is real.
	marketplaceRoot := settings.ExtraKnownMarketplaces[marketplace].Source.Path
	manifest, err := os.ReadFile(filepath.Join(marketplaceRoot, ".github", "plugin", "marketplace.json"))
	require.NoError(t, err, "live install points at %s, which holds no marketplace.json", marketplaceRoot)
	var index struct {
		Plugins []struct {
			Name   string `json:"name"`
			Source string `json:"source"`
		} `json:"plugins"`
	}
	decodeCopilotJSON(t, manifest, &index)
	for _, p := range index.Plugins {
		if p.Name == name {
			require.NotEmpty(t, p.Source, "marketplace.json declares no source for %s", name)
			return filepath.Join(marketplaceRoot, p.Source)
		}
	}
	t.Fatalf("marketplace.json at %s lists no plugin %s", marketplaceRoot, name)
	return ""
}

// decodeCopilotJSON unmarshals a file the copilot CLI wrote. config.json carries a
// leading `//` banner, which is JSONC and not JSON, so comment lines are dropped.
func decodeCopilotJSON(t *testing.T, raw []byte, into any) {
	t.Helper()
	var lines []string
	for _, line := range strings.Split(string(raw), "\n") {
		if !strings.HasPrefix(strings.TrimSpace(line), "//") {
			lines = append(lines, line)
		}
	}
	require.NoError(t, json.Unmarshal([]byte(strings.Join(lines, "\n")), into),
		"could not parse copilot JSON:\n%s", raw)
}

// dumpCopilotHomes lists the file names under the hermetic COPILOT_HOME and the
// real ~/.copilot. This separates "the CLI wrote nothing" from "the CLI wrote
// somewhere else". It prints file contents only for the two install records, and
// only from the hermetic home this test created: the real home holds auth state
// and MCP credentials, which must never reach a CI log.
func dumpCopilotHomes(t *testing.T, copilotHome string) {
	t.Helper()
	homes := []string{copilotHome}
	if home, err := os.UserHomeDir(); err == nil {
		homes = append(homes, filepath.Join(home, ".copilot"))
	}
	for _, dir := range homes {
		var paths []string
		_ = filepath.WalkDir(dir, func(path string, _ fs.DirEntry, err error) error {
			if err != nil {
				return nil
			}
			if len(paths) >= 200 {
				return fs.SkipAll
			}
			paths = append(paths, path)
			return nil
		})
		t.Logf("contents of %s (max 200 entries):\n%s", dir, strings.Join(paths, "\n"))
	}

	for _, name := range []string{"config.json", "settings.json"} {
		p := filepath.Join(copilotHome, name)
		if body, err := os.ReadFile(p); err == nil {
			t.Logf("%s:\n%s", p, body)
		}
	}
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
