// SPDX-FileCopyrightText: Copyright 2026 Dash0 Inc.
// SPDX-License-Identifier: Apache-2.0

package pipeline

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/dash0hq/dash0-agent-plugin/internal/otlp"
)

// The sentinel carries a hyphen and mixed case so no extractor mistakes it for
// a commit SHA, a URL or a diff stat and republishes it as a derived attribute
// — the guard has to fail on a leak, not on an extractor doing its job.
const leakSentinel = "Dash0-LEAK-SENTINEL"

const (
	leakPrompt      = "please summarize " + leakSentinel + "-prompt"
	leakResponse    = "the answer is " + leakSentinel + "-response"
	leakToolArg     = leakSentinel + "-tool-argument"
	leakToolResult  = leakSentinel + "-tool-result"
	leakToolError   = leakSentinel + "-tool-error"
	leakAgentPrompt = leakSentinel + "-agent-prompt"
)

// leakTexts is the content the restrictive levels promise to withhold, one
// string per class the spec names: prompt, response, tool arguments, tool
// results, and a failed call's message.
var leakTexts = []string{
	leakPrompt, leakResponse, leakToolArg, leakToolResult, leakToolError, leakAgentPrompt,
}

func sentinelBash(agentID string) map[string]any {
	event := map[string]any{
		"hook_event_name": "PostToolUse",
		"session_id":      "sess-1",
		"tool_name":       "Bash",
		"tool_use_id":     "tu-bash",
		"tool_input":      map[string]any{"command": `git commit -m "` + leakToolArg + `"`},
		"tool_response":   leakToolResult,
	}
	if agentID != "" {
		event["agent_id"] = agentID
		event["tool_use_id"] = "tu-bash-" + agentID
	}
	return event
}

func sentinelSkill() map[string]any {
	return map[string]any{
		"hook_event_name": "PostToolUse",
		"session_id":      "sess-1",
		"tool_name":       "Skill",
		"tool_use_id":     "tu-skill",
		"tool_input":      map[string]any{"skill": "otel-ottl", "args": leakToolArg},
		"tool_response":   leakToolResult,
	}
}

func sentinelFailure() map[string]any {
	return map[string]any{
		"hook_event_name": "PostToolUseFailure",
		"session_id":      "sess-1",
		"tool_name":       "Bash",
		"tool_use_id":     "tu-bash-failed",
		"tool_input":      map[string]any{"command": `curl ` + leakToolArg},
		"error":           leakToolError,
	}
}

// sentinelSession replays a session in which every content field the four
// dimensions govern carries the sentinel: the user's prompt and the assistant's
// reply, a tool call's arguments and result, a failed call's message, and a
// delegation's own prompt and reply.
func (s *setup) sentinelSession(t *testing.T) {
	t.Helper()
	s.feed(t, map[string]any{"hook_event_name": "SessionStart", "session_id": "sess-1", "model": "opus"})
	s.feed(t, map[string]any{"hook_event_name": "UserPromptSubmit", "session_id": "sess-1", "prompt": leakPrompt})
	s.feed(t, sentinelBash(""))
	s.feed(t, sentinelSkill())
	s.feed(t, map[string]any{"hook_event_name": "SubagentStart", "session_id": "sess-1", "agent_id": "agent1"})
	s.feed(t, map[string]any{
		"hook_event_name": "PostToolUse",
		"session_id":      "sess-1",
		"tool_name":       "Agent",
		"tool_use_id":     "tu-agent",
		"tool_input":      map[string]any{"prompt": leakAgentPrompt},
		"tool_response":   map[string]any{"agentId": "agent1"},
	})
	s.feed(t, sentinelBash("agent1"))
	s.feed(t, map[string]any{
		"hook_event_name":        "SubagentStop",
		"session_id":             "sess-1",
		"agent_id":               "agent1",
		"agent_type":             "Explore",
		"prompt":                 leakAgentPrompt,
		"last_assistant_message": leakResponse,
	})
	s.feed(t, sentinelFailure())
	s.feed(t, map[string]any{
		"hook_event_name":        "Stop",
		"session_id":             "sess-1",
		"transcript_path":        claudeTranscript(t),
		"last_assistant_message": leakResponse,
	})
}

// mockOTLPRecorder captures the raw request bodies rather than the decoded
// spans: a leak test has to read what actually went over the wire, including
// anything a struct with no field for it would silently drop.
func mockOTLPRecorder(t *testing.T) (url string, bodies *[]string, mu *sync.Mutex) {
	t.Helper()
	var captured []string
	var lock sync.Mutex
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		lock.Lock()
		captured = append(captured, string(body))
		lock.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)
	return srv.URL, &captured, &lock
}

// captureStderr redirects the process's stderr for the duration of the test and
// returns a reader for what was written there. debugLog resolves os.Stderr at
// call time, so this is where its output lands.
func captureStderr(t *testing.T) func() string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "stderr.log")
	f, err := os.Create(path)
	require.NoError(t, err)
	orig := os.Stderr
	os.Stderr = f
	t.Cleanup(func() {
		os.Stderr = orig
		_ = f.Close()
	})
	return func() string {
		contents, err := os.ReadFile(path)
		require.NoError(t, err)
		return string(contents)
	}
}

// debugSetup wires a session whose debug output is captured from both places it
// goes — stderr and the debug file — against a recording OTLP endpoint.
func debugSetup(t *testing.T) (s *setup, bodies *[]string, mu *sync.Mutex, output func() string) {
	t.Helper()
	url, captured, lock := mockOTLPRecorder(t)
	stderr := captureStderr(t)
	debugFile := filepath.Join(t.TempDir(), "debug.log")

	s = newSetup(t, url)
	s.cfg.Dimensions = true
	s.cfg.Debug = true
	s.cfg.DebugFile = debugFile

	return s, captured, lock, func() string {
		contents, err := os.ReadFile(debugFile)
		require.NoError(t, err)
		return stderr() + string(contents)
	}
}

// TestProcess_DebugOutputWithholdsContentAtRestrictiveLevels covers the debug
// path at the combination the spec calls out. Debug output is the marshalled
// payload, so it is redacted by construction — this pins that no code path
// prints the raw event alongside it.
func TestProcess_DebugOutputWithholdsContentAtRestrictiveLevels(t *testing.T) {
	s, _, _, output := debugSetup(t)
	s.cfg.Prompts = otlp.LevelDisabled
	s.cfg.Tools = otlp.LevelLimited
	s.cfg.Skills = otlp.LevelLimited
	s.cfg.Agents = otlp.LevelLimited

	s.sentinelSession(t)

	out := output()
	require.Contains(t, out, "[dash0:trace]", "no debug output was produced, so this asserts nothing")
	for _, text := range leakTexts {
		assert.NotContains(t, out, text)
	}
}

// TestProcess_NoRestrictiveLevelLeaksTheSentinel is the end-to-end guard: every
// combination of the two restrictive levels across the four dimensions, over a
// session whose content is all sentinel, asserting the sentinel reaches neither
// the wire nor the debug output. Sixteen combinations rather than the spec's
// named ones, because the dimensions are independent switches and the cost of
// covering them exhaustively is one replay each.
func TestProcess_NoRestrictiveLevelLeaksTheSentinel(t *testing.T) {
	restrictive := []otlp.Level{otlp.LevelDisabled, otlp.LevelLimited}
	for _, prompts := range restrictive {
		for _, tools := range restrictive {
			for _, skills := range restrictive {
				for _, agents := range restrictive {
					name := strings.Join([]string{
						"prompts=" + prompts.String(), "tools=" + tools.String(),
						"skills=" + skills.String(), "agents=" + agents.String(),
					}, ",")
					t.Run(name, func(t *testing.T) {
						s, bodies, mu, output := debugSetup(t)
						s.cfg.Prompts = prompts
						s.cfg.Tools = tools
						s.cfg.Skills = skills
						s.cfg.Agents = agents

						s.sentinelSession(t)

						mu.Lock()
						defer mu.Unlock()
						require.NotEmpty(t, *bodies, "nothing was exported, so this asserts nothing")
						for i, body := range *bodies {
							assert.NotContains(t, body, leakSentinel, "OTLP payload %d", i)
						}
						assert.NotContains(t, output(), leakSentinel)
					})
				}
			}
		}
	}
}

// TestProcess_FullLevelsExportTheContent is the counter-test the guard above
// needs: it fails if the sentinel session stopped carrying content, or if the
// replay stopped reaching the exporter, which would make every assertion above
// vacuously true.
func TestProcess_FullLevelsExportTheContent(t *testing.T) {
	s, bodies, mu, output := debugSetup(t)
	s.cfg.Prompts = otlp.LevelFull
	s.cfg.Tools = otlp.LevelFull
	s.cfg.Skills = otlp.LevelFull
	s.cfg.Agents = otlp.LevelFull

	s.sentinelSession(t)

	mu.Lock()
	defer mu.Unlock()
	all := strings.Join(*bodies, "\n")
	debug := output()
	for _, text := range leakTexts {
		assert.Contains(t, all, text)
		assert.Contains(t, debug, text)
	}
}
