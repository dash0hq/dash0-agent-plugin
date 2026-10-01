// SPDX-FileCopyrightText: Copyright 2026 Dash0 Inc.
// SPDX-License-Identifier: Apache-2.0

//go:build e2e

package e2e

import (
	"bufio"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/dash0hq/dash0-agent-plugin/internal/otlp"
)

func buildOpenCodeBinary(t *testing.T, pluginDir string) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "opencode-on-event")
	build := exec.Command("go", "build", "-o", bin, "./cmd/opencode-on-event")
	build.Dir = pluginDir
	out, err := build.CombinedOutput()
	require.NoError(t, err, "build failed: %s", out)
	return bin
}

// forwardedEnvelopes reads the envelopes the TypeScript plugin produces for the
// recorded session — the same fixture the golden test replays, so the binary is
// exercised against bytes the plugin really writes to its stdin rather than a
// hand-written approximation of them.
func forwardedEnvelopes(t *testing.T, pluginDir string) []string {
	t.Helper()
	f, err := os.Open(filepath.Join(pluginDir, "internal", "source", "opencode", "testdata", "forwarded_envelopes.jsonl"))
	require.NoError(t, err)
	defer f.Close()

	var out []string
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1024*1024), 4*1024*1024)
	for sc.Scan() {
		if line := strings.TrimSpace(sc.Text()); line != "" {
			out = append(out, line)
		}
	}
	require.NoError(t, sc.Err())
	require.NotEmpty(t, out)
	return out
}

func attr(s otlp.Span, key string) string {
	for _, a := range s.Attributes {
		if a.Key == key && a.Value.StringValue != nil {
			return *a.Value.StringValue
		}
	}
	return ""
}

// TestE2EOpenCodeSessionSpans (L2) pipes one recorded session's envelopes
// through the built binary, one process per envelope exactly as the plugin
// spawns it, and asserts the span tree that reaches the collector: a chat span
// carrying the turn's usage, the turn's tools beneath it, and the delegated
// sub-agent anchored under its Agent tool span — all on one trace, since the
// trace context has to survive across those separate processes.
func TestE2EOpenCodeSessionSpans(t *testing.T) {
	pluginDir := findPluginDir(t)
	bin := buildOpenCodeBinary(t, pluginDir)
	cap, srv := newOTLPCapture(t)
	defer srv.Close()

	pluginData := t.TempDir()
	for _, envelope := range forwardedEnvelopes(t, pluginDir) {
		cmd := exec.Command(bin)
		cmd.Env = append(os.Environ(),
			"DASH0_OTLP_URL="+srv.URL,
			"OPENCODE_PLUGIN_OPTION_AUTH_TOKEN=e2e-token",
			"OPENCODE_PLUGIN_DATA="+pluginData,
			"DASH0_OMIT_IO=false",
		)
		cmd.Stdin = strings.NewReader(envelope)
		out, err := cmd.CombinedOutput()
		require.NoError(t, err, "binary failed: %s", out)
	}

	time.Sleep(200 * time.Millisecond)
	bodies, auths := cap.snapshot()
	spans := collectSpans(t, bodies)
	require.NotEmpty(t, spans)
	logSpanTree(t, spans)
	assert.Contains(t, auths, "Bearer e2e-token")

	var chat, agentTool, invokeAgent otlp.Span
	tools := map[string]otlp.Span{}
	traces := map[string]bool{}
	for _, s := range spans {
		traces[s.TraceID] = true
		assert.Equal(t, "opencode", attr(s, "gen_ai.harness.name"))
		switch {
		case strings.HasPrefix(s.Name, "chat"):
			chat = s
		case strings.HasPrefix(s.Name, "invoke_agent"):
			invokeAgent = s
		case strings.HasPrefix(s.Name, "execute_tool"):
			tools[attr(s, "gen_ai.tool.name")] = s
			if attr(s, "gen_ai.tool.name") == "Agent" {
				agentTool = s
			}
		}
	}

	require.NotEmpty(t, chat.SpanID, "the turn must produce a chat span")
	assert.Len(t, traces, 1, "the whole session, sub-agent included, is one trace")
	assert.Empty(t, chat.ParentSpanID, "the chat span is the turn's root")
	assert.True(t, spanHasPositiveTokenUsage(chat), "the chat span must carry the turn's summed usage")
	assert.Equal(t, "mock-model", attr(chat, "gen_ai.request.model"))

	require.Contains(t, tools, "read", "the turn's file reads must be exported")
	assert.Equal(t, chat.SpanID, tools["read"].ParentSpanID, "a top-level tool hangs under the chat span")
	assert.NotEqual(t, tools["read"].StartTimeUnixNano, tools["read"].EndTimeUnixNano,
		"tool spans carry OpenCode's own start/end times, not a zero-length instant")

	require.Contains(t, tools, "echo", "the MCP tool must be exported under its bare name")
	assert.Equal(t, "capture", attr(tools["echo"], "dash0.gen_ai.tool.mcp_server"),
		"OpenCode's flat <server>_<tool> name must resolve to an MCP server")

	require.NotEmpty(t, agentTool.SpanID, "the delegation must produce an Agent tool span")
	require.NotEmpty(t, invokeAgent.SpanID, "the sub-agent must produce an invoke_agent span")
	assert.Equal(t, chat.SpanID, agentTool.ParentSpanID)
	assert.Equal(t, agentTool.SpanID, invokeAgent.ParentSpanID,
		"the sub-agent span anchors under the Agent tool span the normalizer synthesizes")
	assert.Equal(t, attr(agentTool, "gen_ai.agent.id"), attr(invokeAgent, "gen_ai.agent.id"))
	assert.True(t, spanHasPositiveTokenUsage(invokeAgent), "the sub-agent's own usage must be reported separately")
}

// TestE2EOpenCodeFailsOpen asserts the binary never exits non-zero. The plugin
// runs in OpenCode's process and swallows the child's exit status, but a crash
// loop here would still show up as noise in the user's session.
func TestE2EOpenCodeFailsOpen(t *testing.T) {
	bin := buildOpenCodeBinary(t, findPluginDir(t))

	for name, stdin := range map[string]string{
		"malformed json":  "this is not json",
		"empty":           "",
		"unknown event":   `{"kind":"event","name":"session.updated","payload":{}}`,
		"missing payload": `{"kind":"event","name":"session.created"}`,
	} {
		t.Run(name, func(t *testing.T) {
			cmd := exec.Command(bin)
			cmd.Env = append(os.Environ(),
				"DASH0_OTLP_URL=http://127.0.0.1:1",
				"OPENCODE_PLUGIN_OPTION_AUTH_TOKEN=e2e-token",
				"OPENCODE_PLUGIN_DATA="+t.TempDir(),
			)
			cmd.Stdin = strings.NewReader(stdin)
			assert.NoError(t, cmd.Run(), "binary must exit 0 on %s", name)
		})
	}
}

// TestE2EOpenCodeSessionEndClearsScratch asserts the plugin's shutdown envelope
// frees the session's scratch directory — the plugin is the only thing that can
// tell the pipeline a session is over, so a regression there leaks a directory
// per session.
func TestE2EOpenCodeSessionEndClearsScratch(t *testing.T) {
	pluginDir := findPluginDir(t)
	bin := buildOpenCodeBinary(t, pluginDir)
	_, srv := newOTLPCapture(t)
	defer srv.Close()

	pluginData := t.TempDir()
	envelopes := forwardedEnvelopes(t, pluginDir)

	var sessionID string
	for _, envelope := range envelopes {
		var decoded struct {
			RootSessionID string `json:"root_session_id"`
		}
		require.NoError(t, json.Unmarshal([]byte(envelope), &decoded))
		sessionID = decoded.RootSessionID

		cmd := exec.Command(bin)
		cmd.Env = append(os.Environ(),
			"DASH0_OTLP_URL="+srv.URL,
			"OPENCODE_PLUGIN_OPTION_AUTH_TOKEN=e2e-token",
			"OPENCODE_PLUGIN_DATA="+pluginData,
		)
		cmd.Stdin = strings.NewReader(envelope)
		out, err := cmd.CombinedOutput()
		require.NoError(t, err, "binary failed: %s", out)
	}

	require.NotEmpty(t, sessionID)
	assert.NoDirExists(t, filepath.Join(pluginData, sessionID),
		"the shutdown envelope must remove the session's scratch directory")
}
