// SPDX-FileCopyrightText: Copyright 2026 Dash0 Inc.
// SPDX-License-Identifier: Apache-2.0

package pipeline

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/dash0hq/dash0-agent-plugin/internal/otlp"
)

// delegate feeds the events one top-level delegation produces: the sub-agent's
// start, the Agent tool call that launched it, the tools it ran, and its stop.
func (s *setup) delegate(t *testing.T, agentID string, inner ...map[string]any) {
	t.Helper()
	s.startDelegation(t, "", agentID)
	s.runInAgent(t, agentID, inner...)
	s.stopDelegation(t, agentID)
}

// startDelegation feeds the sub-agent's start and the Agent tool call that
// launched it. callerAgentID is empty for a top-level delegation and names the
// spawning agent for a nested one: the launching Agent call carries the caller's
// id, while the callee's arrives in the response.
func (s *setup) startDelegation(t *testing.T, callerAgentID, agentID string) {
	t.Helper()
	s.feed(t, map[string]any{"hook_event_name": "SubagentStart", "session_id": "sess-1", "agent_id": agentID})
	launch := map[string]any{
		"hook_event_name": "PostToolUse",
		"session_id":      "sess-1",
		"tool_name":       "Agent",
		"tool_use_id":     "tu-agent-" + agentID,
		"tool_input":      map[string]any{"prompt": "find the outage"},
		"tool_response":   map[string]any{"agentId": agentID},
	}
	if callerAgentID != "" {
		launch["agent_id"] = callerAgentID
	}
	s.feed(t, launch)
}

func (s *setup) runInAgent(t *testing.T, agentID string, inner ...map[string]any) {
	t.Helper()
	for _, event := range inner {
		event["agent_id"] = agentID
		s.feed(t, event)
	}
}

func (s *setup) stopDelegation(t *testing.T, agentID string) {
	t.Helper()
	s.feed(t, map[string]any{
		"hook_event_name": "SubagentStop",
		"session_id":      "sess-1",
		"agent_id":        agentID,
		"agent_type":      "Explore",
		"prompt":          "find the outage",
	})
}

func fileRead() map[string]any {
	return map[string]any{
		"hook_event_name": "PostToolUse",
		"session_id":      "sess-1",
		"tool_name":       "Read",
		"tool_use_id":     "tu-read",
		"tool_input":      map[string]any{"file_path": "/home/alice/.env"},
		"tool_response":   "SECRET=hunter2",
	}
}

// TestProcess_AgentsDisabled_ReparentsChildrenOntoTheChatSpan covers the spec
// scenario "Delegation suppressed without orphaning its children": the
// invoke_agent span goes, and the work beneath it lands on the delegating turn.
func TestProcess_AgentsDisabled_ReparentsChildrenOntoTheChatSpan(t *testing.T) {
	url, spans, mu := mockOTLPServer(t)
	s := newSetup(t, url)
	s.cfg.Agents = otlp.LevelDisabled
	s.cfg.Tools = otlp.LevelLimited
	s.cfg.Dimensions = true

	s.startTurn(t)
	chat, err := otlp.LoadTraceContext(s.sessionDir("sess-1"))
	require.NoError(t, err)
	require.NotNil(t, chat)

	s.delegate(t, "agent1", bashCall(), fileRead())
	s.feed(t, map[string]any{"hook_event_name": "Stop", "session_id": "sess-1", "transcript_path": claudeTranscript(t)})

	mu.Lock()
	defer mu.Unlock()
	names := spanNames(*spans)
	assert.NotContains(t, names, "invoke_agent Explore", "spans: %v", names)
	require.Len(t, *spans, 4, "spans: %v", names)

	for _, span := range *spans {
		if span.Name != "execute_tool Bash" && span.Name != "execute_tool Read" {
			continue
		}
		assert.Equal(t, chat.SpanID, span.ParentSpanID,
			"span %s parents to the delegating turn's chat span", span.Name)
	}
}

// TestProcess_AgentsDisabled_ExportsNoUnexportedParent replays a full delegated
// session — a nested delegation included — and holds the invariant the spec
// states outright: suppressing a span must not leave anything pointing at it.
func TestProcess_AgentsDisabled_ExportsNoUnexportedParent(t *testing.T) {
	url, spans, mu := mockOTLPServer(t)
	s := newSetup(t, url)
	s.cfg.Agents = otlp.LevelDisabled
	s.cfg.Tools = otlp.LevelLimited
	s.cfg.Skills = otlp.LevelLimited
	s.cfg.Dimensions = true

	s.startTurn(t)
	s.feed(t, bashCall())
	s.startDelegation(t, "", "agent1")
	s.runInAgent(t, "agent1", bashCall(), skillCall())
	s.startDelegation(t, "agent1", "agent2")
	s.runInAgent(t, "agent2", fileRead())
	s.stopDelegation(t, "agent2")
	s.stopDelegation(t, "agent1")
	s.feed(t, map[string]any{"hook_event_name": "Stop", "session_id": "sess-1", "transcript_path": claudeTranscript(t)})

	mu.Lock()
	defer mu.Unlock()
	names := spanNames(*spans)
	for _, name := range names {
		assert.NotContains(t, name, "invoke_agent", "spans: %v", names)
	}

	exported := map[string]bool{}
	for _, span := range *spans {
		exported[span.SpanID] = true
	}
	for _, span := range *spans {
		if span.ParentSpanID == "" {
			continue
		}
		assert.True(t, exported[span.ParentSpanID],
			"span %s references an unexported parent", span.Name)
	}
}

// TestProcess_AgentsDisabledElsewhereKeepsTheSpan holds the other four runtimes
// still: they never expose the dimensions, so whatever a shared DASH0_*
// fallback resolves Agents to changes nothing they export.
func TestProcess_AgentsDisabledElsewhereKeepsTheSpan(t *testing.T) {
	url, spans, mu := mockOTLPServer(t)
	s := newSetup(t, url)
	s.cfg.Agents = otlp.LevelDisabled

	s.startTurn(t)
	s.delegate(t, "agent1", bashCall())

	mu.Lock()
	defer mu.Unlock()
	assert.Contains(t, spanNames(*spans), "invoke_agent Explore")
}
