// SPDX-FileCopyrightText: Copyright 2026 Dash0 Inc.
// SPDX-License-Identifier: Apache-2.0

//go:build e2e

package e2e

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/dash0hq/dash0-agent-plugin/internal/otlp"
)

// TestE2ECopilotAppTurns feeds the built copilot-app-on-event the calls the
// extension makes, with session events recorded from a real app session, and
// asserts the spans that reach the collector:
//
//   - a steered turn: one chat span whose input carries both prompts, the main
//     agent's tokens on the chat span and the sub-agent's on its invoke_agent
//     span, and the sub-agent's tool naming the sub-agent and its model;
//   - a failed turn under omit_io: a failed chat span whose status says only
//     the error's category.
func TestE2ECopilotAppTurns(t *testing.T) {
	pluginDir := findPluginDir(t)
	name := "copilot-app-on-event"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	bin := filepath.Join(t.TempDir(), name)
	build := exec.Command("go", "build", "-o", bin, "./cmd/copilot-app-on-event")
	build.Dir = pluginDir
	out, err := build.CombinedOutput()
	require.NoError(t, err, "build failed: %s", out)

	cap, srv := newOTLPCapture(t)
	defer srv.Close()
	pluginData := t.TempDir()
	cwd := t.TempDir()

	run := func(omitIO, eventName string, payload map[string]any) {
		payload["sessionId"] = copilotConvID
		payload["cwd"] = cwd
		body, err := json.Marshal(payload)
		require.NoError(t, err)
		cmd := exec.Command(bin, eventName)
		cmd.Dir = cwd
		cmd.Env = append(hermeticEnv(t),
			"DASH0_OTLP_URL="+srv.URL,
			"COPILOT_APP_PLUGIN_OPTION_AUTH_TOKEN=e2e-token",
			"COPILOT_APP_PLUGIN_DATA="+pluginData,
			"DASH0_OMIT_IO="+omitIO,
		)
		cmd.Stdin = strings.NewReader(string(body))
		out, err := cmd.CombinedOutput()
		require.NoError(t, err, "%s failed: %s", eventName, out)
	}

	raw, err := os.ReadFile(filepath.Join(pluginDir, "internal", "source", "copilotapp", "testdata", "turn_with_subagent.json"))
	require.NoError(t, err)
	var recorded []any
	require.NoError(t, json.Unmarshal(raw, &recorded))

	run("false", "sessionStart", map[string]any{})
	run("false", "userPromptSubmitted", map[string]any{"prompt": "check the repo"})
	run("false", "turnEnd", map[string]any{"events": recorded, "prompt": "check the repo\nand the README"})

	run("true", "userPromptSubmitted", map[string]any{"prompt": "again"})
	run("true", "turnEnd", map[string]any{"events": []any{
		map[string]any{"type": "session.error", "timestamp": time.Now().UTC().Format(time.RFC3339Nano),
			"data": map[string]any{"errorType": "quota", "message": "quota exceeded for 'again'"}},
	}})

	time.Sleep(200 * time.Millisecond)
	spans := collectSpansFrom(t, cap)
	logSpanTree(t, spans)

	var chats []otlp.Span
	byName := map[string]otlp.Span{}
	for _, s := range spans {
		if strings.HasPrefix(s.Name, "chat") {
			chats = append(chats, s)
		}
		byName[s.Name] = s
	}
	require.Len(t, chats, 2, "one chat span per turn: steering does not start a turn")
	steered, failed := chats[0], chats[1]
	if spanAttrString(steered, "gen_ai.input.messages") == "" {
		steered, failed = failed, steered
	}

	t.Run("the steered turn", func(t *testing.T) {
		assert.Contains(t, spanAttrString(steered, "gen_ai.input.messages"), `check the repo\nand the README`)
		assert.Equal(t, int64(124277+124696), spanIntAttr(t, steered, "gen_ai.usage.input_tokens"), "the main agent's rounds only")
		assert.NotEqual(t, otlp.StatusCodeError, steered.Status.Code)
		assertSpanAttr(t, steered, "gen_ai.harness.name", "github-copilot-app")

		agent, ok := byName["invoke_agent explore"]
		require.True(t, ok)
		assert.Equal(t, int64(7723+7796), spanIntAttr(t, agent, "gen_ai.usage.input_tokens"))
		assertSpanAttr(t, agent, "gen_ai.request.model", "gpt-5.6-luna")

		view, ok := byName["execute_tool view"]
		require.True(t, ok)
		assert.Equal(t, agent.SpanID, view.ParentSpanID)
		assertSpanAttr(t, view, "gen_ai.agent.name", "explore")
		assertSpanAttr(t, view, "gen_ai.request.model", "gpt-5.6-luna")
	})

	t.Run("the failed turn", func(t *testing.T) {
		assert.Equal(t, otlp.StatusCodeError, failed.Status.Code)
		assert.Equal(t, "quota", failed.Status.Message, "omit_io keeps the error message out of the span status")
		for _, a := range failed.Attributes {
			if a.Value.StringValue != nil {
				assert.NotContains(t, *a.Value.StringValue, "quota exceeded", "attribute %s", a.Key)
			}
		}
	})
}
