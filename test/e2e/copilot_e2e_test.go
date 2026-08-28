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
	name := "copilot-on-event"
	if runtime.GOOS == "windows" {
		// go build honors -o verbatim: without the extension, Windows refuses to
		// exec the file directly ("executable file not found in %PATH%").
		name += ".exe"
	}
	bin := filepath.Join(t.TempDir(), name)
	build := exec.Command("go", "build", "-o", bin, "./cmd/copilot-on-event")
	build.Dir = pluginDir
	out, err := build.CombinedOutput()
	require.NoError(t, err, "build failed: %s", out)
	return bin
}

// copilotBinaryName is the cached-binary name copilot-on-event.sh derives for the
// running platform. The bootstrap normalizes uname's mingw/msys/cygwin output to
// "windows" and appends ".exe" there, so a binary staged without the suffix is
// never found: the script falls through to the release download, 404s, and fails
// open without sending anything.
func copilotBinaryName(version string) string {
	name := fmt.Sprintf("copilot-on-event-%s-%s-%s", version, runtime.GOOS, runtime.GOARCH)
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	return name
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
// runs its own bash under an invoke_agent-task layer the plugin must re-emit as
// its own span.
func stagedOtelTurn(dir, conv string) {
	const trace = "11111111111111111111111111111111"
	lines := []string{
		// chat span: usage/model/response for the turn.
		fmt.Sprintf(`{"type":"span","traceId":"%s","spanId":"aaaaaaaaaaaaaa01","parentSpanId":"aaaaaaaaaaaaaa00","name":"chat gpt-5.3-codex","startTime":[1000,0],"endTime":[1001,0],"status":{"code":0},"attributes":{"gen_ai.conversation.id":%q,"gen_ai.request.model":"gpt-5.3-codex","gen_ai.usage.input_tokens":14613,"gen_ai.usage.output_tokens":68,"gen_ai.usage.cache_read.input_tokens":14592,"github.copilot.cost":1.0,"gen_ai.output.messages":"[{\"role\":\"assistant\",\"parts\":[{\"type\":\"text\",\"content\":\"Echo complete.\"}]}]"}}`, trace, conv),
		// top-level bash: 0.5s real duration.
		fmt.Sprintf(`{"type":"span","traceId":"%s","spanId":"aaaaaaaaaaaaaa02","parentSpanId":"aaaaaaaaaaaaaa00","name":"execute_tool bash","startTime":[1001,0],"endTime":[1001,500000000],"status":{"code":0},"attributes":{"gen_ai.tool.name":"bash","gen_ai.tool.call.id":"call_top","gen_ai.tool.call.arguments":"{\"command\":\"echo hi\"}","gen_ai.tool.call.result":"hi"}}`, trace),
		// sub-agent's bash, under the invoke_agent-task layer.
		fmt.Sprintf(`{"type":"span","traceId":"%s","spanId":"aaaaaaaaaaaaaa05","parentSpanId":"aaaaaaaaaaaaaa04","name":"execute_tool bash","startTime":[1002,0],"endTime":[1002,250000000],"status":{"code":0},"attributes":{"gen_ai.tool.name":"bash","gen_ai.tool.call.id":"call_sub","gen_ai.tool.call.arguments":"{\"command\":\"echo hello\"}","gen_ai.tool.call.result":"hello"}}`, trace),
		// the sub-agent root, which the plugin re-emits as its own invoke_agent span.
		fmt.Sprintf(`{"type":"span","traceId":"%s","spanId":"aaaaaaaaaaaaaa04","parentSpanId":"aaaaaaaaaaaaaa03","name":"invoke_agent task","startTime":[1001,600000000],"endTime":[1003,0],"status":{"code":0},"attributes":{"gen_ai.conversation.id":%q,"gen_ai.agent.name":"task","gen_ai.agent.id":"builtin:task"}}`, trace, conv),
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
// OTel-sourced execute_tool spans with REAL durations, and an invoke_agent span
// per sub-agent. The tree must reproduce the native one minus the layers nothing
// is emitted for: chat → execute_tool task → invoke_agent task → execute_tool bash.
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
		cmd.Env = append(hermeticEnv(t),
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
	run("sessionStart", `{`+sid+`,"cwd":`+strconv.Quote(t.TempDir())+`,"source":"new"}`)
	run("userPromptSubmitted", `{`+sid+`,"prompt":"run echo hi"}`)
	run("agentStop", `{`+sid+`,"stopReason":"end_turn"}`)

	time.Sleep(200 * time.Millisecond)
	bodies, _ := cap.snapshot()
	spans := collectSpans(t, bodies)
	require.NotEmpty(t, spans)
	logSpanTree(t, spans)

	var chatWithUsage, chatWithResponse, harnessOK bool
	chatSpanID := ""
	tools := map[string]otlp.Span{}  // by native span id
	agents := map[string]otlp.Span{} // sub-agent invoke_agent spans, by native span id
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
		case strings.HasPrefix(s.Name, "invoke_agent"):
			agents[s.SpanID] = s
		}
	}
	assert.True(t, harnessOK, "expected a span tagged gen_ai.harness.name=github-copilot-cli")
	assert.True(t, chatWithUsage, "expected the chat span to carry per-turn gen_ai.usage.*_tokens from the native-OTel file")
	assert.True(t, chatWithResponse, "expected the chat span to carry gen_ai.output.messages (the agent response) from the native-OTel file")

	// Two keys that must NOT survive the trip, both present in this fixture's
	// inputs. github.copilot.cost is on the staged native chat span, in AI
	// credits, and would land beside the money figure Dash0 derives at ingest.
	// stopReason is on the agentStop payload and is camelCase, so the deny list's
	// snake_case spellings did not cover it. Both shipped once; this is the
	// end-to-end lock, above the unit test in internal/otlp.
	for _, s := range spans {
		for _, a := range s.Attributes {
			assert.NotEqual(t, "github.copilot.cost", a.Key,
				"Copilot's AI-credit cost must not be exported (span %q)", s.Name)
			assert.NotEqual(t, "stopReason", a.Key,
				"the agentStop payload's stopReason must not reach a span (span %q)", s.Name)
		}
	}

	require.Len(t, tools, 3, "all execute_tool spans (top-level bash, task spawn, sub-agent bash) must be emitted from the native-OTel file")
	topBash, ok := tools["aaaaaaaaaaaaaa02"]
	require.True(t, ok, "top-level bash keeps its native span id")
	assert.Equal(t, chatSpanID, topBash.ParentSpanID, "top-level tool must parent under the turn's chat span")
	assert.Equal(t, "execute_tool bash", topBash.Name)
	assert.NotEqual(t, topBash.StartTimeUnixNano, topBash.EndTimeUnixNano, "tool spans must carry the REAL duration, not a zero-length instant")

	task, ok := tools["aaaaaaaaaaaaaa03"]
	require.True(t, ok, "task spawn emitted")
	assert.Equal(t, chatSpanID, task.ParentSpanID)

	// The sub-agent gets its own invoke_agent span, carrying the SAME standard
	// attributes Claude and Codex produce — not a Copilot-specific key on the
	// tool span. gen_ai.agent.id is the spawning call id, which is unique per
	// invocation; the native gen_ai.agent.id ("builtin:task") is shared by every
	// sub-agent of that kind and would be a type filter wearing an id's name.
	agent, ok := agents["aaaaaaaaaaaaaa04"]
	require.True(t, ok, "the sub-agent's invoke_agent span must be emitted, not collapsed")
	assert.Equal(t, "invoke_agent task", agent.Name)
	assert.Equal(t, "aaaaaaaaaaaaaa03", agent.ParentSpanID, "the sub-agent hangs under the task tool that spawned it")
	assertSpanAttr(t, agent, "gen_ai.agent.name", "task")
	assertSpanAttr(t, agent, "gen_ai.agent.id", "call_spawn")

	subBash, ok := tools["aaaaaaaaaaaaaa05"]
	require.True(t, ok, "sub-agent tool emitted")
	assert.Equal(t, "aaaaaaaaaaaaaa04", subBash.ParentSpanID, "sub-agent tool must nest under the sub-agent's invoke_agent span")
}

// assertSpanAttr fails unless the span carries key with exactly want.
func assertSpanAttr(t *testing.T, s otlp.Span, key, want string) {
	t.Helper()
	for _, a := range s.Attributes {
		if a.Key == key {
			require.NotNil(t, a.Value.StringValue, "%s on %s has no string value", key, s.Name)
			assert.Equal(t, want, *a.Value.StringValue, "%s on %s", key, s.Name)
			return
		}
	}
	t.Errorf("span %s carries no %s", s.Name, key)
}

// TestE2ECopilotUnmarkedRealSessionStillExports (L2) is the other half of the
// sub-agent suppression: a missing marker is not on its own evidence of a
// sub-agent.
//
// Only sessionStart writes a marker, and that hook can fail to write one for
// reasons that have nothing to do with sub-agents — the bootstrap downloads the
// binary inside it, under Copilot's 10s timeout, and every fail-open path exits
// before the binary runs. The first session after a version bump on a slow link
// is a real session with no marker. Suppressing on the marker alone silenced
// every one of its turns, permanently, since sessionStart does not fire again.
//
// The native-OTel file settles it: a sub-agent has no spans under its own
// conversation id, because its chat spans carry the parent's. So this drives a
// turn with no sessionStart at all, for a session the file DOES describe, and it
// must still export.
func TestE2ECopilotUnmarkedRealSessionStillExports(t *testing.T) {
	pluginDir := findPluginDir(t)
	bin := buildCopilotBinary(t, pluginDir)
	cap, srv := newOTLPCapture(t)
	defer srv.Close()

	pluginData := t.TempDir()
	otelDir := t.TempDir()
	stagedOtelTurn(otelDir, copilotConvID)

	run := func(eventName, payload string) {
		cmd := exec.Command(bin, eventName)
		cmd.Env = append(hermeticEnv(t),
			"DASH0_OTLP_URL="+srv.URL,
			"COPILOT_PLUGIN_OPTION_AUTH_TOKEN=e2e-token",
			"COPILOT_PLUGIN_DATA="+pluginData,
			"DASH0_COPILOT_OTEL_DIR="+otelDir,
		)
		cmd.Stdin = strings.NewReader(payload)
		out, err := cmd.CombinedOutput()
		require.NoError(t, err, "%s failed: %s", eventName, out)
	}

	// No sessionStart: this is the shape a failed bootstrap leaves behind.
	sid := `"sessionId":"` + copilotConvID + `"`
	run("userPromptSubmitted", `{`+sid+`,"prompt":"still a real session"}`)
	run("agentStop", `{`+sid+`,"stopReason":"end_turn"}`)

	time.Sleep(200 * time.Millisecond)
	bodies, _ := cap.snapshot()
	spans := collectSpans(t, bodies)
	require.NotEmpty(t, spans,
		"a real session whose marker was never written must still export")

	require.NoFileExists(t, filepath.Join(pluginData, "started", copilotConvID),
		"the premise: this session really has no marker")

	var chatWithUsage bool
	for _, s := range spans {
		if strings.HasPrefix(s.Name, "chat") && spanHasPositiveTokenUsage(s) {
			chatWithUsage = true
		}
	}
	assert.True(t, chatWithUsage,
		"and it must carry the turn's usage, not a bare span")
}

// TestE2ECopilotSubAgentSessionMintsNoConversation (L2) pins the suppression of
// a sub-agent's own hook session, using the payload shape an INTERACTIVE Copilot
// session produces.
//
// Copilot gives a sub-agent its own session id, and its shape depends on how the
// CLI was launched: `copilot -p` names it call_<toolCallId>, which
// copilot.Normalize drops on the prefix, while an interactive session gives it a
// plain UUID that the prefix cannot catch. That one shipped. Observed
// 2026-08-28 against Copilot CLI 1.0.80: a delegating turn produced the expected
// four-span conversation plus a second conversation holding one `chat ` span
// with no model and no usage — the sub-agent's own turn, whose native-OTel spans
// carry the PARENT's conversation id.
//
// What holds in both modes is that a sub-agent session receives no sessionStart.
// The events below are the interactive shape: the parent runs a full lifecycle,
// the sub-agent gets userPromptSubmitted and agentStop under a UUID, and both
// carry the traceparent interactive mode adds.
func TestE2ECopilotSubAgentSessionMintsNoConversation(t *testing.T) {
	pluginDir := findPluginDir(t)
	bin := buildCopilotBinary(t, pluginDir)
	cap, srv := newOTLPCapture(t)
	defer srv.Close()

	pluginData := t.TempDir()
	otelDir := t.TempDir()
	stagedOtelTurn(otelDir, copilotConvID)

	run := func(eventName, payload string) {
		cmd := exec.Command(bin, eventName)
		cmd.Env = append(hermeticEnv(t),
			"DASH0_OTLP_URL="+srv.URL,
			"COPILOT_PLUGIN_OPTION_AUTH_TOKEN=e2e-token",
			"COPILOT_PLUGIN_DATA="+pluginData,
			"DASH0_COPILOT_OTEL_DIR="+otelDir,
		)
		cmd.Stdin = strings.NewReader(payload)
		out, err := cmd.CombinedOutput()
		require.NoError(t, err, "%s failed: %s", eventName, out)
	}

	const subAgentID = "5b65a91f-cbe0-4feb-9736-aadec8fe09b8"
	const trace = `"traceparent":"00-558ca38a5fd1be05a94cf7002271be76-36bfb9a97f18b534-01"`
	parent := `"sessionId":"` + copilotConvID + `"`
	sub := `"sessionId":"` + subAgentID + `"`

	run("sessionStart", `{`+parent+`,"cwd":`+strconv.Quote(t.TempDir())+`,"source":"new",`+trace+`}`)
	run("userPromptSubmitted", `{`+parent+`,"prompt":"delegate this"}`)

	// The sub-agent's whole lifecycle: a prompt and a stop, no sessionStart.
	run("userPromptSubmitted", `{`+sub+`,"prompt":"Execute exactly this shell command: echo sub",`+trace+`}`)
	run("agentStop", `{`+sub+`,"stopReason":"end_turn","stop_hook_active":false,`+trace+`}`)

	run("agentStop", `{`+parent+`,"stopReason":"end_turn"}`)

	time.Sleep(200 * time.Millisecond)
	bodies, _ := cap.snapshot()
	spans := collectSpans(t, bodies)
	require.NotEmpty(t, spans)
	logSpanTree(t, spans)

	conversations := map[string]int{}
	for _, s := range spans {
		for _, a := range s.Attributes {
			if a.Key == "gen_ai.conversation.id" && a.Value.StringValue != nil {
				conversations[*a.Value.StringValue]++
			}
		}
	}
	assert.NotContains(t, conversations, subAgentID,
		"a sub-agent session must mint no conversation, whatever its id looks like")
	assert.Len(t, conversations, 1,
		"a delegating turn is one conversation; got %v", conversations)
	assert.Positive(t, conversations[copilotConvID], "the parent turn must still be exported")

	// The parent is unaffected: its turn still carries the usage and the tools.
	var chatWithUsage, tools int
	for _, s := range spans {
		switch {
		case strings.HasPrefix(s.Name, "chat") && spanHasPositiveTokenUsage(s):
			chatWithUsage++
		case strings.HasPrefix(s.Name, "execute_tool"):
			tools++
		}
	}
	assert.Equal(t, 1, chatWithUsage, "the parent turn's chat span must still carry usage")
	assert.Positive(t, tools, "the parent turn's tool spans must still be emitted")
}

// TestE2ECopilotUnsafeSessionIDDestroysNothing (L2) pins the guard in front of
// the entrypoint's os.RemoveAll.
//
// The sub-agent suppression removes the session directory of a turn it drops,
// and it resolves that directory from the raw payload — before pipeline.Process,
// which is what normally substitutes a random id for a missing or unsafe one. An
// agentStop with no sessionId therefore resolved SessionDir to the plugin's data
// root ITSELF and deleted it: the bootstrap's cached binary and every concurrent
// session's state, exit 0, no diagnostic. "../victim" walked out of it.
func TestE2ECopilotUnsafeSessionIDDestroysNothing(t *testing.T) {
	pluginDir := findPluginDir(t)
	bin := buildCopilotBinary(t, pluginDir)
	_, srv := newOTLPCapture(t)
	defer srv.Close()

	root := t.TempDir()
	pluginData := filepath.Join(root, "data")
	binCache := filepath.Join(pluginData, "bin")
	other := filepath.Join(pluginData, "another-live-session")
	sibling := filepath.Join(root, "victim")
	markers := filepath.Join(pluginData, "started")
	for _, d := range []string{binCache, other, sibling, markers} {
		require.NoError(t, os.MkdirAll(d, 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(d, "keep"), []byte("x"), 0o644))
	}
	require.NoError(t, os.WriteFile(filepath.Join(markers, "live-session"), nil, 0o644))

	runAs := func(eventName, payload string) {
		cmd := exec.Command(bin, eventName)
		cmd.Env = append(hermeticEnv(t),
			"DASH0_OTLP_URL="+srv.URL,
			"COPILOT_PLUGIN_OPTION_AUTH_TOKEN=e2e-token",
			"COPILOT_PLUGIN_DATA="+pluginData,
			"DASH0_COPILOT_OTEL_DIR="+t.TempDir(),
		)
		cmd.Stdin = strings.NewReader(payload)
		out, err := cmd.CombinedOutput()
		require.NoError(t, err, "the binary must stay fail-open: %s", out)
	}
	run := func(payload string) { runAs("agentStop", payload) }

	// Quoted, not interpolated raw: a Windows path is full of backslashes, and
	// every one of them starts a JSON escape. An unquoted path made the payload
	// undecodable, the binary rejected it before reading a single field, and this
	// case passed on Windows for the wrong reason — nothing was destroyed because
	// nothing ran.
	cwd := strconv.Quote(root)

	run(`{"cwd":` + cwd + `,"stopReason":"end_turn"}`)                         // no sessionId at all
	run(`{"cwd":` + cwd + `,"sessionId":"../victim","stopReason":"end_turn"}`) // escapes the data dir
	run(`{"cwd":` + cwd + `,"sessionId":"","stopReason":"end_turn"}`)          // empty is the data dir
	// A safe path segment is not enough: these three name directories the plugin
	// owns, so resolving them would delete every live session's marker, or the
	// bootstrap's cached binary.
	run(`{"cwd":` + cwd + `,"sessionId":"started","stopReason":"end_turn"}`)
	run(`{"cwd":` + cwd + `,"sessionId":"bin","stopReason":"end_turn"}`)
	run(`{"cwd":` + cwd + `,"sessionId":"otel","stopReason":"end_turn"}`)

	// sessionEnd is the other RemoveAll, and it is pipeline.Process's rather than
	// this entrypoint's: Process joins the payload's id onto the data dir itself
	// and removes what it joined. Keeping the entrypoint's own paths off these
	// names leaves that one untouched, so the id has to be renamed in the payload
	// before Process ever sees it.
	for _, id := range []string{"started", "bin", "otel"} {
		runAs("sessionEnd", `{"cwd":`+cwd+`,"sessionId":"`+id+`"}`)
	}

	assert.DirExists(t, pluginData, "the plugin data root must survive an unusable session id")
	assert.FileExists(t, filepath.Join(binCache, "keep"), "the bootstrap's binary cache must survive")
	assert.FileExists(t, filepath.Join(other, "keep"), "a concurrent session's state must survive")
	assert.FileExists(t, filepath.Join(sibling, "keep"), "a sibling directory must be unreachable")
	assert.FileExists(t, filepath.Join(markers, "live-session"), "the marker directory must be unreachable")

	// Nothing is asserted about spans here. A bare Stop has no preceding prompt
	// and so no trace context, and pipeline.Process declines to build a chat span
	// without one — which is correct and predates this guard. What matters is
	// that the event took the normal path instead of the suppression's
	// RemoveAll.
}

// TestE2ECopilotSweepsOnSessionStart (L2) pins the wiring of the session-directory
// sweep. Removing the call keeps the whole suite green otherwise: the sweep's own
// behaviour is covered at function level, its wiring by nothing.
func TestE2ECopilotSweepsOnSessionStart(t *testing.T) {
	pluginDir := findPluginDir(t)
	bin := buildCopilotBinary(t, pluginDir)
	_, srv := newOTLPCapture(t)
	defer srv.Close()

	pluginData := t.TempDir()

	// What a killed run leaves: a session directory nothing will ever end, and
	// the sessionStart marker its launch wrote, which nothing deletes. The marker
	// is what identifies the directory as this runtime's — under
	// DASH0_PLUGIN_DATA the data root is shared with Cursor and Codex, whose
	// sessions the sweep must not touch.
	dead := filepath.Join(pluginData, "killed-session")
	require.NoError(t, os.MkdirAll(dead, 0o755))
	require.NoError(t, os.MkdirAll(filepath.Join(pluginData, "started"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(pluginData, "started", "killed-session"), nil, 0o644))
	events := filepath.Join(dead, "events.jsonl")
	require.NoError(t, os.WriteFile(events, []byte(`{"prompt":"private"}`+"\n"), 0o644))
	old := time.Now().Add(-4 * 7 * 24 * time.Hour)
	require.NoError(t, os.Chtimes(events, old, old))

	cmd := exec.Command(bin, "sessionStart")
	cmd.Env = append(hermeticEnv(t),
		"DASH0_OTLP_URL="+srv.URL,
		"COPILOT_PLUGIN_OPTION_AUTH_TOKEN=e2e-token",
		"COPILOT_PLUGIN_DATA="+pluginData,
		"DASH0_COPILOT_OTEL_DIR="+t.TempDir(),
	)
	cmd.Stdin = strings.NewReader(`{"sessionId":"` + copilotConvID + `","cwd":` + strconv.Quote(t.TempDir()) + `,"source":"new"}`)
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "sessionStart failed: %s", out)

	assert.NoDirExists(t, dead,
		"sessionStart must collect what killed runs left, or their prompts sit on disk forever")
	assert.DirExists(t, filepath.Join(pluginData, copilotConvID),
		"the starting session's own directory must survive its own sweep")
}

// TestE2ECopilotTurnAfterSessionEndStillExports (L2) pins the marker against the
// thing that deletes the directory it used to live in.
//
// pipeline.Process removes the session directory on SessionEnd. A turn arriving
// afterwards recreates it — its userPromptSubmitted mints a fresh trace context
// — so the pipeline will build the chat span; what it cannot recreate is the
// sessionStart marker, because sessionStart does not fire again. With the marker
// inside that directory the turn was read as a sub-agent's and dropped in
// silence.
//
// The reachable case is not session reuse but the end of every session:
// agentStop and sessionEnd fire together as separate processes, and agentStop is
// the slower — it scans the native-OTel file — so a sessionEnd that wins the
// race took the last turn with it.
func TestE2ECopilotTurnAfterSessionEndStillExports(t *testing.T) {
	pluginDir := findPluginDir(t)
	bin := buildCopilotBinary(t, pluginDir)
	cap, srv := newOTLPCapture(t)
	defer srv.Close()

	pluginData := t.TempDir()
	otelDir := t.TempDir()
	stagedOtelTurn(otelDir, copilotConvID)

	run := func(eventName, payload string) {
		cmd := exec.Command(bin, eventName)
		cmd.Env = append(hermeticEnv(t),
			"DASH0_OTLP_URL="+srv.URL,
			"COPILOT_PLUGIN_OPTION_AUTH_TOKEN=e2e-token",
			"COPILOT_PLUGIN_DATA="+pluginData,
			"DASH0_COPILOT_OTEL_DIR="+otelDir,
		)
		cmd.Stdin = strings.NewReader(payload)
		out, err := cmd.CombinedOutput()
		require.NoError(t, err, "%s failed: %s", eventName, out)
	}

	sid := `"sessionId":"` + copilotConvID + `"`
	cwd := t.TempDir()
	run("sessionStart", `{`+sid+`,"cwd":`+strconv.Quote(cwd)+`,"source":"new"}`)
	run("userPromptSubmitted", `{`+sid+`,"prompt":"first"}`)
	run("agentStop", `{`+sid+`,"stopReason":"end_turn"}`)
	run("sessionEnd", `{`+sid+`,"reason":"exit"}`)

	require.NoDirExists(t, filepath.Join(pluginData, copilotConvID),
		"SessionEnd removes the session directory; that is the premise of this test")

	// The late turn. No second sessionStart, exactly as a racing agentStop has none.
	before := len(collectSpansQuiet(t, cap))
	run("userPromptSubmitted", `{`+sid+`,"prompt":"second"}`)
	run("agentStop", `{`+sid+`,"stopReason":"end_turn"}`)
	time.Sleep(200 * time.Millisecond)

	after := collectSpansQuiet(t, cap)
	assert.Greater(t, len(after), before,
		"a turn arriving after SessionEnd must still export; the marker outlives the directory")

	var lateChat bool
	for _, s := range after[before:] {
		if strings.HasPrefix(s.Name, "chat") {
			lateChat = true
		}
	}
	assert.True(t, lateChat, "the late turn's chat span is the one that used to be dropped")
}

// collectSpansQuiet is collectSpans without the tree logging, for a test that
// snapshots the capture more than once.
func collectSpansQuiet(t *testing.T, cap *otlpCapture) []otlp.Span {
	t.Helper()
	bodies, _ := cap.snapshot()
	return collectSpans(t, bodies)
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
		cmd.Env = append(hermeticEnv(t),
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
	// The cursor lives beside the native-OTel files, not under the plugin's
	// per-session directory: pipeline.Process deletes that directory on
	// SessionEnd and a Copilot session id outlives its session, so a resumed
	// launch would find no cursor and re-read the whole file. See cursorPrefix in
	// internal/source/copilot.
	cursorFile := filepath.Join(otelDir, "cursor-"+copilotConvID+".json")

	// Phase 1: sessionStart records only SessionID (no TraceID minted), so a Stop
	// now has no intact context. The turn must be deferred.
	run("sessionStart", `{`+sid+`,"cwd":`+strconv.Quote(t.TempDir())+`,"source":"new"}`)
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
		cmd.Env = append(hermeticEnv(t),
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
		cmd.Env = append(hermeticEnv(t),
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
		cmd.Env = append(hermeticEnv(t),
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
	// sessionStart first, as a real session always has it: a turn ending in a
	// session that never started is a sub-agent's and is dropped. See the
	// suppression in cmd/copilot-on-event.
	run("sessionStart", `{`+sid+`,"cwd":`+strconv.Quote(t.TempDir())+`,"source":"new"}`)
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
	// strconv.Quote, not a bare interpolation: on Windows t.TempDir() is
	// "C:\Users\...", and \U is not a valid JSON escape, so the binary would
	// reject the event and fail open before sending anything.
	sessionStart := `{"sessionId":"` + copilotConvID + `","cwd":` + strconv.Quote(t.TempDir()) + `,"source":"new"}`

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
		placed := filepath.Join(binDir, copilotBinaryName(version))
		copyExecutable(t, bin, placed)

		cmd := exec.Command("bash", bootstrap, "sessionStart")
		// USERPROFILE as well as HOME: os.UserHomeDir reads HOME on Unix and
		// USERPROFILE on Windows, and the binary resolves the global config file
		// relative to it. Without both, this reads the developer's real ~/.copilot.
		cmd.Env = append(os.Environ(), "HOME="+home, "USERPROFILE="+home, "COPILOT_PLUGIN_DATA="+pdata)
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
		placed := filepath.Join(binDir, copilotBinaryName(version))
		copyExecutable(t, bin, placed)

		// The project file is resolved from the directory the hook runs in, so the
		// event's `cwd` is deliberately somewhere else: the binary reads the config
		// before it chdirs into the payload's cwd, and this pins that order.
		cmd := exec.Command("bash", bootstrap, "sessionStart")
		cmd.Dir = workspace
		// USERPROFILE as well as HOME: os.UserHomeDir reads HOME on Unix and
		// USERPROFILE on Windows, and the binary resolves the global config file
		// relative to it. Without both, this reads the developer's real ~/.copilot.
		cmd.Env = append(os.Environ(), "HOME="+home, "USERPROFILE="+home, "COPILOT_PLUGIN_DATA="+pdata)
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
		cmd.Env = append(hermeticEnv(t),
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
//
// It takes TWO `copilot -p` processes over one conversation, not one. Copilot
// buffers its native-OTel file and starts writing it only as the process shuts
// down: measured on Windows the file is 0 bytes when agentStop fires and whole
// only after exit. A single -p run therefore races its own flush for the usage
// of the turn that just ended, and loses about as often as it wins. The reader
// is built for that lag ("a span Copilot flushes late folds into the next
// turn's window"), so the fix is to give it a next turn: the resumed process
// finds turn 1's file complete whichever way turn 1's own race went.
func TestE2EFullFlowWithCopilot(t *testing.T) {
	token := os.Getenv("COPILOT_GITHUB_TOKEN")
	if token == "" {
		t.Fatal("COPILOT_GITHUB_TOKEN not set — required for e2e test")
	}
	copilotBin, err := lookRealCopilot()
	if err != nil {
		t.Fatal("copilot CLI not found — install with: npm install -g @github/copilot")
	}
	t.Logf("copilot CLI: %s", copilotBin)

	pluginDir := findPluginDir(t)
	bin := buildCopilotBinary(t, pluginDir)
	cap, srv := newOTLPCapture(t)
	defer srv.Close()

	pluginData := t.TempDir()
	otelDir := t.TempDir()

	// Hooks are launched by Copilot, not by this process, so hermeticEnv cannot
	// reach them — the wrappers below have to move HOME/USERPROFILE themselves.
	// Without it the binary finds the developer's real
	// ~/.copilot/dash0-agent-plugin.local.md, whose otlp_url outranks DASH0_OTLP_URL
	// (see Harness.PluginOption): the capture stays empty and every span goes to
	// that live dataset instead. CI has no such file, so this only ever failed on a
	// configured machine.
	hookHome := t.TempDir()

	// Hook wrapper: sets the binary's env (incl. DASH0_COPILOT_OTEL_DIR so the
	// reader scans our isolated dir — Copilot doesn't pass env to hooks) and execs
	// the binary, forwarding the event-name argv.
	wrapperDir := t.TempDir()
	wrapper := filepath.Join(wrapperDir, "hook.sh")
	require.NoError(t, os.WriteFile(wrapper, []byte(fmt.Sprintf(`#!/usr/bin/env bash
export HOME=%q
export USERPROFILE=%q
export DASH0_OTLP_URL=%q
export COPILOT_PLUGIN_OPTION_AUTH_TOKEN="e2e-copilot-token"
export COPILOT_PLUGIN_DATA=%q
export DASH0_COPILOT_OTEL_DIR=%q
exec %q "$@"
`, hookHome, hookHome, srv.URL, pluginData, otelDir, bin)), 0o755))

	// The PowerShell twin of the wrapper above. copilot/hooks.json declares both a
	// `bash` and a `powershell` variant per event and Copilot picks one by
	// platform, so a bash-only registration silently runs nothing on Windows.
	// Single-quoted PowerShell literals, not %q: a Go-quoted string would escape
	// the backslashes in a Windows path and PowerShell would keep them doubled.
	psWrapper := filepath.Join(wrapperDir, "hook.ps1")
	require.NoError(t, os.WriteFile(psWrapper, []byte(fmt.Sprintf(`$env:HOME = '%s'
$env:USERPROFILE = '%s'
$env:DASH0_OTLP_URL = '%s'
$env:COPILOT_PLUGIN_OPTION_AUTH_TOKEN = 'e2e-copilot-token'
$env:COPILOT_PLUGIN_DATA = '%s'
$env:DASH0_COPILOT_OTEL_DIR = '%s'
# Mirrors the shipped bootstrap: take the event from the pipeline when the
# harness delivers it that way, otherwise inherit this process's stdin.
$Payload = (@($input) -join ([string][char]10))
if ($Payload) {
  $Psi = New-Object System.Diagnostics.ProcessStartInfo
  $Psi.FileName = '%s'
  $Psi.Arguments = ($args -join ' ')
  $Psi.UseShellExecute = $false
  $Psi.RedirectStandardInput = $true
  $Proc = [System.Diagnostics.Process]::Start($Psi)
  $Bytes = [System.Text.Encoding]::UTF8.GetBytes($Payload)
  $Proc.StandardInput.BaseStream.Write($Bytes, 0, $Bytes.Length)
  $Proc.StandardInput.BaseStream.Flush()
  $Proc.StandardInput.Close()
  $Proc.WaitForExit()
  exit $Proc.ExitCode
}
& '%s' @args
exit $LASTEXITCODE
`, hookHome, hookHome, srv.URL, pluginData, otelDir, bin, bin)), 0o644))

	copilotHome := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(copilotHome, "hooks"), 0o755))
	// camelCase registration with the event name as argv — matches copilot/hooks.json.
	hookJSON := `{"version":1,"hooks":{`
	events := []string{"sessionStart", "userPromptSubmitted", "agentStop", "sessionEnd"}
	for i, e := range events {
		if i > 0 {
			hookJSON += ","
		}
		hookJSON += fmt.Sprintf(`%q:[{"type":"command","bash":%q,"powershell":%q,"timeoutSec":10}]`,
			e, wrapper+" "+e, `& "`+psWrapper+`" `+e)
	}
	hookJSON += `}}`
	require.NoError(t, os.WriteFile(filepath.Join(copilotHome, "hooks", "dash0.json"), []byte(hookJSON), 0o644))

	workDir := t.TempDir()
	gitInit(t, workDir)

	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Minute)
	defer cancel()

	// runCopilot drives one `copilot -p` process, each with its OWN native-OTel
	// file inside the shared otelDir the reader scans. Distinct paths are what
	// keep turn 1's file intact for turn 2 to read; one shared path is truncated
	// on relaunch. resume is "" for a fresh conversation.
	runCopilot := func(label, resume, prompt string) {
		t.Helper()
		args := []string{"-p", prompt, "--allow-all-tools", "-C", workDir}
		if resume != "" {
			args = append(args, "--resume="+resume)
		}
		cmd := exec.CommandContext(ctx, copilotBin, args...)
		cmd.Env = append(os.Environ(),
			"COPILOT_HOME="+copilotHome,
			"COPILOT_GITHUB_TOKEN="+token,
			"COPILOT_OTEL_ENABLED=true",
			"COPILOT_OTEL_FILE_EXPORTER_PATH="+filepath.Join(otelDir, label+".jsonl"),
		)
		// Own process group so copilot's exit-time cleanup (which can signal its
		// process group) cannot SIGKILL the test binary that spawned it.
		setNewProcessGroup(cmd)
		out, err := cmd.CombinedOutput()
		t.Logf("copilot -p (%s) output (err=%v):\n%s", label, err, out)
		require.NoError(t, err, "copilot -p (%s) failed", label)
	}

	runCopilot("turn1", "", "Reply with exactly one word: ok")

	// The conversation id the plugin reported for turn 1. Taking it from our own
	// span, rather than scraping the CLI's "Resume  copilot --resume=…" line,
	// keeps the test off a human-facing output format AND proves the id the
	// plugin tags spans with is the one Copilot accepts back.
	turn1 := collectSpansFrom(t, cap)
	require.NotEmpty(t, turn1, "no spans from a live Copilot session")
	logSpanTree(t, turn1)
	conv := ""
	for _, s := range turn1 {
		if v := spanAttrString(s, "gen_ai.conversation.id"); v != "" {
			conv = v
			break
		}
	}
	require.NotEmpty(t, conv, "no turn-1 span carries gen_ai.conversation.id")

	// Resume the same conversation in a second process. Turn 1's file is complete
	// on disk now, so at turn 2's agentStop the reader finds this conversation's
	// chat span and attaches its usage — the multi-launch shape it is built for
	// (see internal/source/copilot/otelfile.go).
	runCopilot("turn2", conv, "Reply with exactly one word: done")

	spans := collectSpansFrom(t, cap)
	require.NotEmpty(t, spans, "no spans from the resumed Copilot session")
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

// lookRealCopilot resolves the Copilot CLI, skipping any launcher that is one of
// our own launch wrappers.
//
// dash0-configure installs `copilot.cmd` into ~/.local/bin, which shadows npm's
// copilot on PATH and forces COPILOT_OTEL_FILE_EXPORTER_PATH to a private file it
// deletes on exit. A test that runs it gets no native-OTel file at all: the
// wrapper overrides the path the test asked for. Git Bash never picks the wrapper
// (it has no extensionless twin there) but exec.LookPath does, via PATHEXT — so
// this only ever broke on a Windows machine with the plugin installed, which is
// every developer's and no CI runner's.
//
// PATH is walked directly rather than through exec.LookPath, because LookPath
// returns only the first hit and there is no way to ask it for the next one.
func lookRealCopilot() (string, error) {
	exts := []string{""}
	if runtime.GOOS == "windows" {
		// PATHEXT only. The extensionless `copilot` npm also installs is a POSIX
		// shell script that CreateProcess cannot run, so matching it would trade
		// one wrong resolution for another.
		pathext := os.Getenv("PATHEXT")
		if pathext == "" {
			pathext = ".COM;.EXE;.BAT;.CMD"
		}
		exts = strings.Split(strings.ToLower(pathext), ";")
	}
	for _, dir := range filepath.SplitList(os.Getenv("PATH")) {
		if dir == "" {
			continue
		}
		for _, ext := range exts {
			p := filepath.Join(dir, "copilot"+ext)
			info, err := os.Stat(p)
			if err != nil || info.IsDir() {
				continue
			}
			// Windows decides by extension, not by mode bits, and reports 0o666
			// for everything — so only check the bit where it means something.
			if runtime.GOOS != "windows" && info.Mode()&0o111 == 0 {
				continue
			}
			if body, err := os.ReadFile(p); err == nil &&
				strings.Contains(string(body), "dash0-agent-plugin") {
				continue // our launch wrapper, not the CLI
			}
			return p, nil
		}
	}
	return "", fmt.Errorf("copilot CLI not found on PATH")
}

// collectSpansFrom decodes every span the capture holds so far.
func collectSpansFrom(t *testing.T, c *otlpCapture) []otlp.Span {
	t.Helper()
	bodies, _ := c.snapshot()
	return collectSpans(t, bodies)
}

// spanAttrString returns the span's string-valued attribute, or "".
func spanAttrString(s otlp.Span, key string) string {
	for _, a := range s.Attributes {
		if a.Key == key && a.Value.StringValue != nil {
			return *a.Value.StringValue
		}
	}
	return ""
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
	copilotBin, err := lookRealCopilot()
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

// hermeticEnv is os.Environ() with HOME and USERPROFILE pointed at one fresh
// directory, so a run cannot read the developer's real config. The config file
// outranks DASH0_*, so without this a real ~/.copilot file decides otlp_url and
// the case either reads an empty capture or exports to a live dataset.
// os.UserHomeDir reads HOME on Unix and USERPROFILE on Windows, so both move.
func hermeticEnv(t *testing.T) []string {
	t.Helper()
	home := t.TempDir()
	return append(os.Environ(), "HOME="+home, "USERPROFILE="+home)
}
