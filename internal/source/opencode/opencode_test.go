// SPDX-FileCopyrightText: Copyright 2026 Dash0 Inc.
// SPDX-License-Identifier: Apache-2.0

package opencode

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	rootSession  = "ses_fd56324abffeYUUI5c6tAmJq8L"
	childSession = "ses_fd5632109ffeIJoNI5KhyWKCl2"
)

// captured returns the recorded OpenCode event with the given sequence number,
// wrapped in the plugin envelope. Driving the tests from the real capture keeps
// them honest about the payload shapes OpenCode actually emits; the plugin
// context is supplied per test because it is resolved in-process, not recorded.
func captured(t *testing.T, seq int, context map[string]any) map[string]any {
	t.Helper()

	file, err := os.Open(filepath.Join("testdata", "captured_events.jsonl"))
	require.NoError(t, err)
	defer file.Close()

	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for scanner.Scan() {
		var envelope map[string]any
		require.NoError(t, json.Unmarshal(scanner.Bytes(), &envelope))
		if int(envelope["seq"].(float64)) != seq {
			continue
		}
		for key, value := range context {
			envelope[key] = value
		}
		return envelope
	}
	require.NoError(t, scanner.Err())
	t.Fatalf("no captured event with seq %d", seq)
	return nil
}

func rootContext() map[string]any {
	return map[string]any{"cwd": "/project", "root_session_id": rootSession}
}

// withAssistants sets the envelope's per-session assistant map on a context.
func withAssistants(context map[string]any, entries map[string]any) map[string]any {
	context["assistants"] = entries
	return context
}

func TestSessionStart(t *testing.T) {
	event := Normalize(captured(t, 1, map[string]any{"root_session_id": rootSession}))

	require.NotNil(t, event)
	assert.Equal(t, "SessionStart", event["hook_event_name"])
	assert.Equal(t, rootSession, event["session_id"])
	assert.Equal(t, "/private/var/folders/n5/0mkkbtqs36s2f_4s45lmdxgr0000gn/T/tmp.JBE6GOxB6e/project", event["cwd"])
}

func TestUserPromptSubmit(t *testing.T) {
	event := Normalize(captured(t, 3, rootContext()))

	require.NotNil(t, event)
	assert.Equal(t, "UserPromptSubmit", event["hook_event_name"])
	assert.Equal(t, rootSession, event["session_id"])
	assert.Equal(t, "mock-model", event["model"])
	assert.Contains(t, event["prompt"], "delegate a sub-task")
}

// A child session's prompt must not reach the pipeline: UserPromptSubmit is
// where the turn's trace is allocated, and the sub-agent belongs to the
// spawning turn's trace.
func TestChildPromptIsDropped(t *testing.T) {
	assert.Nil(t, Normalize(captured(t, 134, rootContext())))
}

func TestSubagentStart(t *testing.T) {
	event := Normalize(captured(t, 131, rootContext()))

	require.NotNil(t, event)
	assert.Equal(t, "SubagentStart", event["hook_event_name"])
	assert.Equal(t, rootSession, event["session_id"])
	assert.Equal(t, childSession, event["agent_id"])
	assert.Equal(t, "general", event["agent_type"])
}

// The child session's own `info.agent` is authoritative; the root session's mode
// must not be reported as the sub-agent's type.
func TestSubagentStartPrefersChildAgentOverRootMode(t *testing.T) {
	context := withAssistants(rootContext(), map[string]any{
		rootSession: map[string]any{"mode": "build"},
	})
	event := Normalize(captured(t, 131, context))

	require.NotNil(t, event)
	assert.Equal(t, "general", event["agent_type"])
}

func TestPostToolUse(t *testing.T) {
	event := Normalize(captured(t, 75, rootContext()))

	require.NotNil(t, event)
	assert.Equal(t, "PostToolUse", event["hook_event_name"])
	assert.Equal(t, rootSession, event["session_id"])
	assert.Equal(t, "read", event["tool_name"])
	assert.Equal(t, "call_0_1787421318796", event["tool_use_id"])
	assert.Equal(t, float64(12), event["duration_ms"])
	assert.Contains(t, event["tool_response"], "capture fixture project")
	assert.NotContains(t, event, "agent_id")
	assert.NotContains(t, event, "error")
}

func TestPostToolUseFailure(t *testing.T) {
	event := Normalize(captured(t, 94, rootContext()))

	require.NotNil(t, event)
	assert.Equal(t, "PostToolUseFailure", event["hook_event_name"])
	assert.Equal(t, "read", event["tool_name"])
	assert.Contains(t, event["error"], "File not found")
	assert.NotContains(t, event, "tool_response")
}

// Only a terminal status produces a span; OpenCode repeats `running` for a call
// that reports intermediate metadata.
func TestNonTerminalToolPartsAreDropped(t *testing.T) {
	for _, seq := range []int{70, 72, 132, 138} {
		assert.Nil(t, Normalize(captured(t, seq, rootContext())), "seq %d", seq)
	}
}

func TestMCPToolNameRewrite(t *testing.T) {
	context := rootContext()
	context["mcp_servers"] = []any{"capture"}
	event := Normalize(captured(t, 114, context))

	require.NotNil(t, event)
	assert.Equal(t, "mcp__capture__echo", event["tool_name"])
}

// Without the server key the name is ambiguous — `_` is legal in both halves —
// so it is left alone rather than split on the first underscore.
func TestMCPToolNameKeptWhenServerUnknown(t *testing.T) {
	event := Normalize(captured(t, 114, rootContext()))

	require.NotNil(t, event)
	assert.Equal(t, "capture_echo", event["tool_name"])
}

func TestMCPToolNameResolvesLongestServerKey(t *testing.T) {
	envelope := map[string]any{
		"kind": "event", "name": "message.part.updated",
		"root_session_id": rootSession,
		"mcp_servers":     []any{"dash0", "dash0_otel"},
		"payload": map[string]any{"properties": map[string]any{"part": map[string]any{
			"type": "tool", "tool": "dash0_otel_get_spans", "sessionID": rootSession,
			"state": map[string]any{"status": "completed"},
		}}},
	}

	event := Normalize(envelope)
	require.NotNil(t, event)
	assert.Equal(t, "mcp__dash0_otel__get_spans", event["tool_name"])
}

// The pipeline only allocates the sub-agent's parent span for a tool named
// "Agent" whose response carries agentId, so the delegation tool is presented
// in that shape.
func TestDelegationBecomesAgentTool(t *testing.T) {
	event := Normalize(captured(t, 159, rootContext()))

	require.NotNil(t, event)
	assert.Equal(t, "PostToolUse", event["hook_event_name"])
	assert.Equal(t, "Agent", event["tool_name"])

	response, ok := event["tool_response"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, childSession, response["agentId"])
}

func TestChildToolSpanCarriesAgentID(t *testing.T) {
	envelope := map[string]any{
		"kind": "event", "name": "message.part.updated",
		"root_session_id": rootSession,
		"payload": map[string]any{"properties": map[string]any{"part": map[string]any{
			"type": "tool", "tool": "read", "callID": "call_x", "sessionID": childSession,
			"state": map[string]any{"status": "completed"},
		}}},
	}

	event := Normalize(envelope)
	require.NotNil(t, event)
	assert.Equal(t, rootSession, event["session_id"])
	assert.Equal(t, childSession, event["agent_id"])
}

func TestStop(t *testing.T) {
	context := rootContext()
	context["session_title"] = "Read files and delegate"
	withAssistants(context, map[string]any{
		rootSession: map[string]any{"modelID": "mock-model", "mode": "build", "text": "all done"},
	})
	event := Normalize(captured(t, 183, context))

	require.NotNil(t, event)
	assert.Equal(t, "Stop", event["hook_event_name"])
	assert.Equal(t, rootSession, event["session_id"])
	assert.Equal(t, "mock-model", event["model"])
	assert.Equal(t, "all done", event["last_assistant_message"])
	assert.Equal(t, "Read files and delegate", event["gen_ai.conversation.name"])
	assert.NotContains(t, event, "agent_id")
}

func TestSubagentStop(t *testing.T) {
	context := withAssistants(rootContext(), map[string]any{
		childSession: map[string]any{"modelID": "mock-model", "mode": "general", "text": "done"},
	})
	event := Normalize(captured(t, 157, context))

	require.NotNil(t, event)
	assert.Equal(t, "SubagentStop", event["hook_event_name"])
	assert.Equal(t, rootSession, event["session_id"])
	assert.Equal(t, childSession, event["agent_id"])
	assert.Equal(t, "general", event["agent_type"])
	assert.Equal(t, "done", event["last_assistant_message"])
}

// The sub-agent's span reports the sub-agent's own turn. Inheriting the root
// session's model, text or tokens would double-count the parent turn's usage.
func TestSubagentStopDoesNotInheritRootAssistant(t *testing.T) {
	context := withAssistants(rootContext(), map[string]any{
		rootSession: map[string]any{
			"modelID": "root-model", "mode": "build", "text": "root text",
			"tokens": map[string]any{"input": float64(94), "output": float64(6)},
		},
		childSession: map[string]any{
			"modelID": "child-model", "mode": "general", "text": "child text",
			"tokens": map[string]any{"input": float64(11), "output": float64(2)},
		},
	})
	event := Normalize(captured(t, 157, context))

	require.NotNil(t, event)
	assert.Equal(t, "child-model", event["model"])
	assert.Equal(t, "child text", event["last_assistant_message"])
	assert.Equal(t, "general", event["agent_type"])
	assert.Equal(t, int64(11), event["gen_ai.usage.input_tokens"])
	assert.Equal(t, int64(2), event["gen_ai.usage.output_tokens"])
}

// A sub-agent whose session the plugin reported no assistant message for still
// produces the SubagentStop, but carries none of the root turn's usage.
func TestSubagentStopWithoutOwnAssistant(t *testing.T) {
	context := withAssistants(rootContext(), map[string]any{
		rootSession: map[string]any{
			"modelID": "root-model", "mode": "build", "text": "root text",
			"tokens": map[string]any{"input": float64(94), "output": float64(6)},
		},
	})
	event := Normalize(captured(t, 157, context))

	require.NotNil(t, event)
	assert.Equal(t, "SubagentStop", event["hook_event_name"])
	for _, key := range []string{
		"model", "last_assistant_message", "agent_type",
		"gen_ai.usage.input_tokens", "gen_ai.usage.output_tokens",
	} {
		assert.NotContains(t, event, key)
	}
}

func TestStopFailure(t *testing.T) {
	envelope := map[string]any{
		"kind": "event", "name": "session.error",
		"root_session_id": rootSession,
		"payload": map[string]any{"properties": map[string]any{
			"sessionID": rootSession,
			"error":     map[string]any{"name": "ProviderAuthError", "data": map[string]any{"message": "invalid api key"}},
		}},
	}

	event := Normalize(envelope)
	require.NotNil(t, event)
	assert.Equal(t, "StopFailure", event["hook_event_name"])
	assert.Equal(t, "invalid api key", event["error"])
}

// The pipeline clears the turn's trace context on any Stop/StopFailure, whatever
// its agent_id. A sub-agent's failure must therefore close only the sub-agent,
// or the parent turn's chat span and remaining tool spans are never exported.
func TestChildSessionErrorIsSubagentStop(t *testing.T) {
	context := withAssistants(rootContext(), map[string]any{
		childSession: map[string]any{"modelID": "child-model", "mode": "general"},
	})
	context["kind"], context["name"] = "event", "session.error"
	context["payload"] = map[string]any{"properties": map[string]any{
		"sessionID": childSession,
		"error":     map[string]any{"name": "ProviderAuthError", "data": map[string]any{"message": "invalid api key"}},
	}}

	event := Normalize(context)
	require.NotNil(t, event)
	assert.Equal(t, "SubagentStop", event["hook_event_name"])
	assert.Equal(t, rootSession, event["session_id"])
	assert.Equal(t, childSession, event["agent_id"])
	assert.Equal(t, "general", event["agent_type"])
	assert.Equal(t, "invalid api key", event["error"])
}

// session.error's sessionID is optional. An error that belongs to no session
// must be dropped rather than reported against a session id the pipeline
// invents, which would export a span into a conversation that does not exist
// and leave a scratch directory behind.
func TestStopFailureDroppedWithoutSession(t *testing.T) {
	event := Normalize(map[string]any{
		"kind": "event", "name": "session.error",
		"payload": map[string]any{"properties": map[string]any{
			"error": map[string]any{"name": "UnknownError"},
		}},
	})

	assert.Nil(t, event)
}

func TestSessionEnd(t *testing.T) {
	event := Normalize(map[string]any{
		"kind": "plugin", "name": "shutdown",
		"root_session_id": rootSession,
		"payload":         map[string]any{},
	})

	require.NotNil(t, event)
	assert.Equal(t, "SessionEnd", event["hook_event_name"])
	assert.Equal(t, rootSession, event["session_id"])
}

func TestUnmappedEventsAreDropped(t *testing.T) {
	for _, seq := range []int{2, 7, 34} {
		assert.Nil(t, Normalize(captured(t, seq, rootContext())), "seq %d", seq)
	}
	assert.Nil(t, Normalize(map[string]any{"kind": "event", "name": "session.idle"}))
	assert.Nil(t, Normalize(map[string]any{}))
}

func TestTokenUsage(t *testing.T) {
	context := withAssistants(rootContext(), map[string]any{
		rootSession: map[string]any{"tokens": map[string]any{
			"input": float64(94), "output": float64(6), "reasoning": float64(5),
			"cache": map[string]any{"read": float64(7), "write": float64(3)},
		}},
	})
	event := Normalize(captured(t, 183, context))

	require.NotNil(t, event)
	assert.Equal(t, int64(94), event["gen_ai.usage.input_tokens"])
	assert.Equal(t, int64(6), event["gen_ai.usage.output_tokens"])
	assert.Equal(t, int64(7), event["gen_ai.usage.cache_read.input_tokens"])
	assert.Equal(t, int64(3), event["gen_ai.usage.cache_creation.input_tokens"])
	assert.Equal(t, int64(5), event["gen_ai.usage.reasoning.output_tokens"])
}

func TestReasoningTokensOmittedAtZero(t *testing.T) {
	context := withAssistants(rootContext(), map[string]any{
		rootSession: map[string]any{"tokens": map[string]any{
			"input": float64(94), "output": float64(6), "reasoning": float64(0),
		}},
	})
	event := Normalize(captured(t, 183, context))

	require.NotNil(t, event)
	assert.NotContains(t, event, "gen_ai.usage.reasoning.output_tokens")
	// The other four are reported even at zero, matching the other runtimes.
	assert.Equal(t, int64(0), event["gen_ai.usage.cache_read.input_tokens"])
	assert.Equal(t, int64(0), event["gen_ai.usage.cache_creation.input_tokens"])
}

func TestUsageOmittedWhenUnreported(t *testing.T) {
	event := Normalize(captured(t, 183, rootContext()))

	require.NotNil(t, event)
	for _, key := range []string{
		"gen_ai.usage.input_tokens", "gen_ai.usage.output_tokens",
		"gen_ai.usage.cache_read.input_tokens", "gen_ai.usage.cache_creation.input_tokens",
	} {
		assert.NotContains(t, event, key)
	}
}

func TestConversationNameOmittedForUntitledSession(t *testing.T) {
	t.Run("no title", func(t *testing.T) {
		event := Normalize(captured(t, 183, rootContext()))
		require.NotNil(t, event)
		assert.NotContains(t, event, "gen_ai.conversation.name")
	})

	t.Run("placeholder title", func(t *testing.T) {
		context := rootContext()
		context["session_title"] = "New session - 2026-08-22T17:55:17.972Z"
		event := Normalize(captured(t, 183, context))
		require.NotNil(t, event)
		assert.NotContains(t, event, "gen_ai.conversation.name")
	})
}
