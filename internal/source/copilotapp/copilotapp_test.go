// SPDX-FileCopyrightText: Copyright 2026 Dash0 Inc.
// SPDX-License-Identifier: Apache-2.0

package copilotapp

import (
	"encoding/json"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/dash0hq/dash0-agent-plugin/internal/source/copilot"
)

// loadFixture reads session events recorded from a real Copilot app session
// (redacted): one main-agent round that runs `bash` and a `task` sub-agent in
// parallel, the sub-agent's own `view` call, and the main agent's next round.
func loadFixture(t *testing.T) []Event {
	t.Helper()
	raw, err := os.ReadFile("testdata/turn_with_subagent.json")
	require.NoError(t, err)
	var events []any
	require.NoError(t, json.Unmarshal(raw, &events))
	return DecodeEvents(map[string]any{"events": events})
}

func TestBuildTurn_fromRecordedEvents(t *testing.T) {
	end := time.Date(2026, 10, 2, 12, 54, 40, 0, time.UTC)
	turn := BuildTurn(loadFixture(t), end)
	require.NotNil(t, turn)

	t.Run("each agent's usage is its own", func(t *testing.T) {
		u := turn.Usage
		require.NotNil(t, u)
		assert.Equal(t, int64(124277+124696), u.InputTokens, "the main agent's two rounds only")
		assert.Equal(t, int64(307+219), u.OutputTokens)
		assert.Equal(t, int64(0+124275), u.CacheReadInputTokens)
		assert.Equal(t, int64(124275+419), u.CacheCreationInputTokens)

		require.Len(t, turn.Agents, 1)
		a := turn.Agents[0].Usage
		require.NotNil(t, a, "the sub-agent's rounds go on its invoke_agent span")
		assert.Equal(t, int64(7723+7796), a.InputTokens)
		assert.Equal(t, int64(51+27), a.OutputTokens)
		assert.Equal(t, int64(0+7720), a.CacheReadInputTokens)
		assert.Equal(t, int64(7720+73), a.CacheCreationInputTokens)
	})

	t.Run("model and response are the main agent's", func(t *testing.T) {
		assert.Equal(t, "claude-opus-5.5", turn.Usage.Model, "the sub-agent ran gpt-5.6-luna")
		assert.Equal(t, "claude-opus-5.5", turn.Usage.ResponseModel)
		assert.Equal(t, "Both done: three commits, and the README describes the plugin.", turn.Usage.ResponseText,
			"the last non-empty main-agent message; the sub-agent's answer is not the turn's")
	})

	tools := map[string]int{}
	for i, tc := range turn.Tools {
		tools[tc.Name] = i
	}
	require.Len(t, turn.Tools, 3)
	bash, task, view := turn.Tools[tools["bash"]], turn.Tools[tools["task"]], turn.Tools[tools["view"]]

	t.Run("tools carry real timings and results", func(t *testing.T) {
		assert.Equal(t, "2026-10-02T12:54:27.557Z", bash.Start.Format(time.RFC3339Nano))
		assert.Equal(t, "2026-10-02T12:54:30.246Z", bash.End.Format(time.RFC3339Nano))
		assert.False(t, bash.Failed)
		assert.JSONEq(t, `{"command":"git --no-pager log --oneline -3","description":"Show recent commits"}`, bash.Arguments)
		assert.Equal(t, "abc1234 first\ndef5678 second", bash.Result)
		assert.Equal(t, "toolu_011zGKqf8eDxEcZEkGfhK3BV", bash.CallID)
	})

	t.Run("the tree is chat → task → invoke_agent → view", func(t *testing.T) {
		require.Len(t, turn.Agents, 1)
		agent := turn.Agents[0]

		assert.Empty(t, bash.ParentSpanID, "a main-agent tool hangs off the chat span")
		assert.Empty(t, task.ParentSpanID)
		assert.Equal(t, task.SpanID, agent.ParentSpanID)
		assert.Equal(t, agent.SpanID, view.ParentSpanID)
		assert.NotEqual(t, task.SpanID, agent.SpanID, "the task call stands for two spans")

		assert.Equal(t, "explore", agent.AgentType)
		assert.Equal(t, "toolu_01D6LVSaYcS6R1tivgmw1Gfy", agent.CallID)
		assert.Equal(t, "gpt-5.6-luna", agent.Model, "the sub-agent's own model, not the parent's")
		assert.Equal(t, "2026-10-02T12:54:27.568Z", agent.Start.Format(time.RFC3339Nano))
		assert.Equal(t, "2026-10-02T12:54:30.242Z", agent.End.Format(time.RFC3339Nano))
		assert.False(t, agent.Failed)
	})

	t.Run("span ids are deterministic", func(t *testing.T) {
		again := BuildTurn(loadFixture(t), end)
		for i := range turn.Tools {
			assert.Equal(t, turn.Tools[i].SpanID, again.Tools[i].SpanID)
			assert.Len(t, turn.Tools[i].SpanID, 16)
		}
	})
}

func TestBuildTurn_autoModeReportsAuto(t *testing.T) {
	turn := BuildTurn([]Event{{
		Type: "assistant.usage", Timestamp: "2026-10-02T12:00:00Z",
		Data: map[string]any{"model": "gpt-5.6-luna", "isAuto": true, "inputTokens": 10.0},
	}}, time.Now())
	require.NotNil(t, turn)
	assert.Equal(t, "auto", turn.Usage.Model)
	assert.Equal(t, "gpt-5.6-luna", turn.Usage.ResponseModel)
}

func TestBuildTurn_unfinishedWorkIsClosedAsFailed(t *testing.T) {
	end := time.Date(2026, 10, 2, 12, 1, 0, 0, time.UTC)
	turn := BuildTurn([]Event{
		{Type: "tool.execution_start", Timestamp: "2026-10-02T12:00:00Z",
			Data: map[string]any{"toolCallId": "c1", "toolName": "bash"}},
	}, end)
	require.NotNil(t, turn)
	require.Len(t, turn.Tools, 1)
	assert.Equal(t, end, turn.Tools[0].End)
	assert.True(t, turn.Tools[0].Failed)
	assert.Nil(t, turn.Usage, "no usage event means no usage, not zeros")
}

func TestBuildTurn_failedToolCarriesTheError(t *testing.T) {
	turn := BuildTurn([]Event{
		{Type: "tool.execution_start", Timestamp: "2026-10-02T12:00:00Z",
			Data: map[string]any{"toolCallId": "c1", "toolName": "bash"}},
		{Type: "tool.execution_complete", Timestamp: "2026-10-02T12:00:01Z",
			Data: map[string]any{"toolCallId": "c1", "success": false, "error": map[string]any{"message": "boom"}}},
	}, time.Now())
	require.Len(t, turn.Tools, 1)
	assert.True(t, turn.Tools[0].Failed)
	assert.Equal(t, "boom", turn.Tools[0].Result)
}

func TestBuildTurn_mcpToolIsNamedForTheExtractors(t *testing.T) {
	turn := BuildTurn([]Event{
		{Type: "tool.execution_start", Timestamp: "2026-10-02T12:00:00Z",
			Data: map[string]any{"toolCallId": "c1", "toolName": "github-mcp-server-get_file_contents",
				"mcpServerName": "github-mcp-server", "mcpToolName": "get_file_contents"}},
	}, time.Now())
	require.Len(t, turn.Tools, 1)
	assert.Equal(t, "mcp__github-mcp-server__get_file_contents", turn.Tools[0].Name)
}

func TestBuildTurn_skillNameComesFromTheArguments(t *testing.T) {
	turn := BuildTurn([]Event{
		{Type: "tool.execution_start", Timestamp: "2026-10-02T12:00:00Z",
			Data: map[string]any{"toolCallId": "c1", "toolName": "skill", "arguments": map[string]any{"skill": "dash0-configure"}}},
	}, time.Now())
	require.Len(t, turn.Tools, 1)
	assert.Equal(t, "dash0-configure", turn.Tools[0].SkillName)
}

func TestBuildTurn_nothingToReport(t *testing.T) {
	assert.Nil(t, BuildTurn(nil, time.Now()))
	assert.Nil(t, BuildTurn([]Event{{Type: "session.idle", Data: map[string]any{}}}, time.Now()))
}

func TestNormalize(t *testing.T) {
	t.Run("maps the extension's events", func(t *testing.T) {
		for name, want := range map[string]string{
			"sessionStart": "SessionStart", "userPromptSubmitted": "UserPromptSubmit",
			"turnEnd": "Stop", "sessionEnd": "SessionEnd",
		} {
			got := Normalize(name, map[string]any{"sessionId": "s1"})
			require.NotNil(t, got, name)
			assert.Equal(t, want, got["hook_event_name"])
			assert.Equal(t, "s1", got["session_id"])
		}
	})

	t.Run("drops unknown events and null payloads", func(t *testing.T) {
		assert.Nil(t, Normalize("agentStop", map[string]any{}))
		assert.Nil(t, Normalize("turnEnd", nil))
	})

	// Every unrecognized key on the event becomes a span attribute, so the
	// buffered events and anything else in the payload must not come through.
	t.Run("copies only what the pipeline reads", func(t *testing.T) {
		got := Normalize("turnEnd", map[string]any{
			"sessionId": "s1", "cwd": "/repo", "timestamp": "2026-10-02T12:00:00Z",
			"events": []any{map[string]any{"type": "assistant.usage"}}, "aborted": true,
		})
		assert.Equal(t, map[string]any{"hook_event_name": "Stop", "session_id": "s1", "cwd": "/repo"}, got)
	})

	t.Run("a steered turn's prompt is carried on Stop", func(t *testing.T) {
		got := Normalize("turnEnd", map[string]any{"sessionId": "s1", "prompt": "fix it\nand the tests"})
		assert.Equal(t, "fix it\nand the tests", got["prompt"])
	})

	t.Run("a prompt is carried on UserPromptSubmit", func(t *testing.T) {
		got := Normalize("userPromptSubmitted", map[string]any{"sessionId": "s1", "prompt": "hi"})
		assert.Equal(t, "hi", got["prompt"])
		assert.NotContains(t, got, "prompt_role")
	})

	t.Run("an injected system notification is not user input", func(t *testing.T) {
		got := Normalize("userPromptSubmitted", map[string]any{"prompt": "<system_notification>agent done</system_notification>"})
		assert.Equal(t, "assistant", got["prompt_role"])
	})
}

func TestTimestamp(t *testing.T) {
	fallback := time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC)
	assert.Equal(t, time.Date(2026, 10, 2, 12, 54, 27, 552000000, time.UTC),
		Timestamp(map[string]any{"timestamp": "2026-10-02T12:54:27.552Z"}, fallback))
	assert.Equal(t, fallback, Timestamp(map[string]any{}, fallback))
	assert.Equal(t, fallback, Timestamp(map[string]any{"timestamp": "yesterday"}, fallback))
}

// Usage from a sub-agent the turn never saw start (the extension joined late)
// has no invoke_agent span to go on, so the chat span keeps it.
func TestBuildTurn_unknownSubAgentUsageStaysOnTheChat(t *testing.T) {
	turn := BuildTurn([]Event{{
		Type: "assistant.usage", Timestamp: "2026-10-02T12:00:00Z", AgentID: "a1",
		Data: map[string]any{"model": "gpt-5.6-luna", "inputTokens": 10.0, "parentToolCallId": "gone"},
	}}, time.Now())
	require.NotNil(t, turn)
	assert.Equal(t, int64(10), turn.Usage.InputTokens)
	assert.Empty(t, turn.Usage.Model, "a sub-agent's model is not the turn's")
}

func TestTurnError(t *testing.T) {
	ts := "2026-10-02T12:00:00Z"
	failure := Event{Type: "session.error", Timestamp: ts,
		Data: map[string]any{"errorType": "quota", "message": "quota exceeded for request 'secret'"}}
	reply := Event{Type: "assistant.message", Timestamp: ts, Data: map[string]any{"content": "done"}}

	t.Run("a clean turn has none", func(t *testing.T) {
		assert.Empty(t, TurnError([]Event{reply}, map[string]any{}, true))
	})
	t.Run("an error the turn ended on", func(t *testing.T) {
		events := []Event{reply, failure}
		assert.Equal(t, "quota exceeded for request 'secret'", TurnError(events, map[string]any{}, false))
		assert.Equal(t, "quota", TurnError(events, map[string]any{}, true), "omit_io sends only the category")
	})
	t.Run("an error the turn recovered from", func(t *testing.T) {
		assert.Empty(t, TurnError([]Event{failure, reply}, map[string]any{}, false))
	})
	t.Run("a sub-agent's error is not the turn's", func(t *testing.T) {
		sub := failure
		sub.AgentID = "a1"
		assert.Empty(t, TurnError([]Event{sub}, map[string]any{}, false))
	})
	t.Run("an error with no message or category", func(t *testing.T) {
		bare := Event{Type: "session.error", Timestamp: ts, Data: map[string]any{"errorType": "quota"}}
		assert.Equal(t, "quota", TurnError([]Event{bare}, map[string]any{}, false))
		bare.Data = map[string]any{}
		assert.Equal(t, "error", TurnError([]Event{bare}, map[string]any{}, false))
	})
	t.Run("an aborted turn", func(t *testing.T) {
		assert.Equal(t, "turn aborted", TurnError([]Event{reply}, map[string]any{"aborted": true}, true))
		abort := Event{Type: "abort", Timestamp: ts, Data: map[string]any{"reason": "user_initiated"}}
		assert.Equal(t, "turn aborted: user_initiated", TurnError([]Event{abort}, map[string]any{"aborted": true}, true))
	})
}

// A sub-agent's auto-mode usage keeps the request "auto" on its own usage, and
// leaves the turn's models alone.
func TestBuildTurn_autoSubAgentKeepsAuto(t *testing.T) {
	turn := BuildTurn([]Event{
		{Type: "subagent.started", Timestamp: "2026-10-02T12:00:00Z",
			Data: map[string]any{"toolCallId": "t1", "agentName": "explore", "model": "gpt-5.6-luna"}},
		{Type: "assistant.usage", Timestamp: "2026-10-02T12:00:01Z", AgentID: "a1",
			Data: map[string]any{"model": "gpt-5.6-luna", "isAuto": true, "inputTokens": 10.0, "parentToolCallId": "t1"}},
	}, time.Now())
	require.NotNil(t, turn)
	require.Len(t, turn.Agents, 1)
	require.NotNil(t, turn.Agents[0].Usage)
	assert.Equal(t, "auto", turn.Agents[0].Usage.Model)
	assert.Equal(t, "gpt-5.6-luna", turn.Agents[0].Usage.ResponseModel)
	if turn.Usage != nil {
		assert.Empty(t, turn.Usage.Model)
	}
}

// A turn whose usage events never reached the extension still names its model,
// from the reply, and leaves the token counts out rather than report zeros.
func TestBuildTurn_turnWithoutUsageNamesTheModelAndNoTokens(t *testing.T) {
	turn := BuildTurn([]Event{
		{Type: "assistant.message", Timestamp: "2026-10-02T12:00:00Z", AgentID: "a1",
			Data: map[string]any{"content": "sub", "model": "sub-model", "parentToolCallId": "t1"}},
		{Type: "assistant.message", Timestamp: "2026-10-02T12:00:01Z",
			Data: map[string]any{"content": "done", "model": "qa-fake"}},
	}, time.Now())
	require.NotNil(t, turn)
	assert.Equal(t, "qa-fake", turn.Usage.Model)
	assert.Equal(t, "qa-fake", turn.Usage.ResponseModel)
	event := map[string]any{}
	copilot.AttachUsage(event, turn.Usage)
	assert.Equal(t, "qa-fake", event["model"])
	assert.Equal(t, "qa-fake", event["response_model"])
	for k := range event {
		assert.NotContains(t, k, "token", "no usage was seen, so no count is known")
	}
}

// parentToolCallId is deprecated. Without it, a sub-agent's usage and tools
// still find their sub-agent through agentId.
func TestBuildTurn_subAgentFoundByAgentID(t *testing.T) {
	end := time.Date(2026, 10, 2, 12, 54, 40, 0, time.UTC)
	want := BuildTurn(loadFixture(t), end)
	events := loadFixture(t)
	for _, e := range events {
		delete(e.Data, "parentToolCallId")
	}
	got := BuildTurn(events, end)
	require.NotNil(t, got)
	require.Len(t, got.Agents, 1)
	assert.Equal(t, want.Usage, got.Usage)
	assert.Equal(t, want.Agents[0].Usage, got.Agents[0].Usage)
	assert.Equal(t, want.Tools, got.Tools)
}

// The turn's response is the main agent's, even when a sub-agent replies last.
func TestBuildTurn_responseIgnoresALaterSubAgentMessage(t *testing.T) {
	turn := BuildTurn([]Event{
		{Type: "assistant.message", Timestamp: "2026-10-02T12:00:00Z", Data: map[string]any{"content": "main"}},
		{Type: "assistant.message", Timestamp: "2026-10-02T12:00:01Z", AgentID: "a1",
			Data: map[string]any{"content": "sub", "parentToolCallId": "t1"}},
	}, time.Now())
	require.NotNil(t, turn)
	assert.Equal(t, "main", turn.Usage.ResponseText)
}

// Usage from a sub-agent the turn never saw start keeps its tokens on the chat
// span, priced at that sub-agent's own model. The turn's model, which names the
// span, is still the main agent's, from its reply.
func TestBuildTurn_unknownSubAgentUsageKeepsTheMainModel(t *testing.T) {
	turn := BuildTurn([]Event{
		{Type: "assistant.usage", Timestamp: "2026-10-02T12:00:00Z", AgentID: "a1",
			Data: map[string]any{"model": "gpt-5.6-luna", "inputTokens": 10.0, "parentToolCallId": "gone"}},
		{Type: "assistant.message", Timestamp: "2026-10-02T12:00:01Z",
			Data: map[string]any{"content": "done", "model": "claude-opus-5.5"}},
	}, time.Now())
	require.NotNil(t, turn)
	assert.Equal(t, int64(10), turn.Usage.InputTokens)
	assert.Equal(t, "claude-opus-5.5", turn.Usage.Model)
	assert.Equal(t, "gpt-5.6-luna", turn.Usage.ResponseModel)
	assert.Equal(t, "claude-opus-5.5", turn.Usage.ReplyModel)

	chat := map[string]any{}
	copilot.AttachUsage(chat, turn.Usage)
	assert.Equal(t, "gpt-5.6-luna", chat["response_model"])
}

// When the main agent also reported usage, with no model, the chat span's
// tokens are priced at its reply model, not the sub-agent's.
func TestBuildTurn_mainUsageWithoutModelBesideUnknownSubAgentUsage(t *testing.T) {
	turn := BuildTurn([]Event{
		{Type: "assistant.usage", Timestamp: "2026-10-02T12:00:00Z", Data: map[string]any{"inputTokens": 5.0}},
		{Type: "assistant.usage", Timestamp: "2026-10-02T12:00:00Z", AgentID: "a1",
			Data: map[string]any{"model": "gpt-5.6-luna", "inputTokens": 10.0, "parentToolCallId": "gone"}},
		{Type: "assistant.message", Timestamp: "2026-10-02T12:00:01Z",
			Data: map[string]any{"content": "done", "model": "claude-opus-5.5"}},
	}, time.Now())
	require.NotNil(t, turn)
	assert.Equal(t, "claude-opus-5.5", turn.Usage.ResponseModel)
}

// The main agent's usage may arrive without a model; its reply still names one.
func TestBuildTurn_usageWithoutModelTakesTheReplyModel(t *testing.T) {
	turn := BuildTurn([]Event{
		{Type: "assistant.usage", Timestamp: "2026-10-02T12:00:00Z", Data: map[string]any{"inputTokens": 10.0}},
		{Type: "assistant.message", Timestamp: "2026-10-02T12:00:01Z",
			Data: map[string]any{"content": "done", "model": "claude-opus-5.5"}},
	}, time.Now())
	require.NotNil(t, turn)
	assert.Equal(t, "claude-opus-5.5", turn.Usage.Model)
	assert.Equal(t, "claude-opus-5.5", turn.Usage.ResponseModel)
	assert.False(t, turn.Usage.NoTokens)
}
