// SPDX-FileCopyrightText: Copyright 2026 Dash0 Inc.
// SPDX-License-Identifier: Apache-2.0

package pipeline

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/dash0hq/dash0-agent-plugin/internal/otlp"
)

// startTurn feeds the two events every tool span needs a trace context from.
func (s *setup) startTurn(t *testing.T) {
	t.Helper()
	s.feed(t, map[string]any{"hook_event_name": "SessionStart", "session_id": "sess-1", "model": "opus"})
	s.feed(t, map[string]any{"hook_event_name": "UserPromptSubmit", "session_id": "sess-1", "prompt": "do thing"})
}

func bashCall() map[string]any {
	return map[string]any{
		"hook_event_name": "PostToolUse",
		"session_id":      "sess-1",
		"tool_name":       "Bash",
		"tool_use_id":     "tu-bash",
		"tool_input":      map[string]any{"command": "git status --short"},
		"tool_response":   "M internal/otlp/otlp.go",
	}
}

func skillCall() map[string]any {
	return map[string]any{
		"hook_event_name": "PostToolUse",
		"session_id":      "sess-1",
		"tool_name":       "Skill",
		"tool_use_id":     "tu-skill",
		"tool_input":      map[string]any{"skill": "otel-ottl"},
		"tool_response":   "loaded",
	}
}

func spanNames(spans []otlp.Span) []string {
	names := make([]string, 0, len(spans))
	for _, s := range spans {
		names = append(names, s.Name)
	}
	return names
}

// TestProcess_ToolsDisabled_EmitsNoToolSpan covers the spec scenario "Disabled
// emits no span at all": the tool call produces nothing, and the turn it
// belongs to is still reported.
func TestProcess_ToolsDisabled_EmitsNoToolSpan(t *testing.T) {
	url, spans, mu := mockOTLPServer(t)
	s := newSetup(t, url)
	s.cfg.Tools = otlp.LevelDisabled
	s.cfg.Dimensions = true

	s.startTurn(t)
	s.feed(t, bashCall())
	s.feed(t, map[string]any{"hook_event_name": "Stop", "session_id": "sess-1", "transcript_path": claudeTranscript(t)})

	mu.Lock()
	defer mu.Unlock()
	require.Len(t, *spans, 1, "spans: %v", spanNames(*spans))
	assert.Contains(t, (*spans)[0].Name, "chat", "the turn's chat span is still exported")
}

// TestProcess_ToolsLimited_KeepsTheDerivedAttributes covers the enrichment
// order: bash_command_family, skill_name and mcp_server are derived from the
// full tool_input, so they have to be there at limited — where the input
// itself is not.
func TestProcess_ToolsLimited_KeepsTheDerivedAttributes(t *testing.T) {
	url, spans, mu := mockOTLPServer(t)
	s := newSetup(t, url)
	s.cfg.Tools = otlp.LevelLimited
	s.cfg.Dimensions = true

	s.startTurn(t)
	s.feed(t, bashCall())
	s.feed(t, skillCall())
	s.feed(t, map[string]any{
		"hook_event_name": "PostToolUse",
		"session_id":      "sess-1",
		"tool_name":       "mcp__dash0__getSpans",
		"tool_use_id":     "tu-mcp",
		"tool_input":      map[string]any{"dataset": "production"},
		"tool_response":   "3 spans",
	})

	mu.Lock()
	defer mu.Unlock()
	require.Len(t, *spans, 3, "spans: %v", spanNames(*spans))

	for _, span := range *spans {
		assert.False(t, hasAttr(span.Attributes, "gen_ai.tool.call.arguments"), "span %s", span.Name)
		assert.False(t, hasAttr(span.Attributes, "gen_ai.tool.call.result"), "span %s", span.Name)
	}

	assert.True(t, hasStringAttr((*spans)[0].Attributes, "dash0.gen_ai.tool.bash.command_family", "git status"))
	assert.True(t, hasStringAttr((*spans)[1].Attributes, "dash0.gen_ai.tool.skill.name", "otel-ottl"))
	assert.True(t, hasStringAttr((*spans)[2].Attributes, "dash0.gen_ai.tool.mcp_server", "dash0"))
}

// TestProcess_SkillsStayVisibleWhenToolsAreDisabled covers the spec scenario of
// the same name: skills is the dimension that governs a skill invocation, so
// tools: disabled says nothing about it.
func TestProcess_SkillsStayVisibleWhenToolsAreDisabled(t *testing.T) {
	url, spans, mu := mockOTLPServer(t)
	s := newSetup(t, url)
	s.cfg.Tools = otlp.LevelDisabled
	s.cfg.Skills = otlp.LevelLimited
	s.cfg.Dimensions = true

	s.startTurn(t)
	s.feed(t, skillCall())
	s.feed(t, bashCall())

	mu.Lock()
	defer mu.Unlock()
	require.Len(t, *spans, 1, "spans: %v", spanNames(*spans))
	assert.Equal(t, "execute_tool Skill", (*spans)[0].Name)
	assert.True(t, hasStringAttr((*spans)[0].Attributes, "dash0.gen_ai.tool.skill.name", "otel-ottl"))
}

// TestProcess_SkillsSuppressedWhenToolsAreFull covers the other direction of the
// same precedence: the bash call keeps its content, the skill produces no span.
func TestProcess_SkillsSuppressedWhenToolsAreFull(t *testing.T) {
	url, spans, mu := mockOTLPServer(t)
	s := newSetup(t, url)
	s.cfg.Tools = otlp.LevelFull
	s.cfg.Skills = otlp.LevelDisabled
	s.cfg.Dimensions = true

	s.startTurn(t)
	s.feed(t, skillCall())
	s.feed(t, bashCall())

	mu.Lock()
	defer mu.Unlock()
	require.Len(t, *spans, 1, "spans: %v", spanNames(*spans))
	span := (*spans)[0]
	assert.Equal(t, "execute_tool Bash", span.Name)
	assert.True(t, hasAttr(span.Attributes, "gen_ai.tool.call.arguments"))
	assert.True(t, hasStringAttr(span.Attributes, "gen_ai.tool.call.result", "M internal/otlp/otlp.go"))
}

// TestProcess_ToolsDisabledLeavesNoOrphanedParent guards the invariant the spec
// states for agents: disabled from the other dimension. tools: disabled drops
// the Agent tool call's execute_tool span, which is what a sub-agent's spans
// derive their parent id from, so everything beneath the delegation has to
// reparent onto the delegating turn's chat span.
func TestProcess_ToolsDisabledLeavesNoOrphanedParent(t *testing.T) {
	url, spans, mu := mockOTLPServer(t)
	s := newSetup(t, url)
	s.cfg.Tools = otlp.LevelDisabled
	s.cfg.Skills = otlp.LevelLimited
	s.cfg.Agents = otlp.LevelLimited
	s.cfg.Dimensions = true

	s.startTurn(t)
	chat, err := otlp.LoadTraceContext(s.sessionDir("sess-1"))
	require.NoError(t, err)
	require.NotNil(t, chat)

	s.feed(t, map[string]any{"hook_event_name": "SubagentStart", "session_id": "sess-1", "agent_id": "agent1"})
	s.feed(t, map[string]any{
		"hook_event_name": "PostToolUse",
		"session_id":      "sess-1",
		"tool_name":       "Agent",
		"tool_use_id":     "tu-agent",
		"tool_response":   map[string]any{"agentId": "agent1"},
	})
	inner := skillCall()
	inner["agent_id"] = "agent1"
	s.feed(t, inner)
	s.feed(t, map[string]any{
		"hook_event_name": "SubagentStop",
		"session_id":      "sess-1",
		"agent_id":        "agent1",
		"agent_type":      "Explore",
	})
	s.feed(t, map[string]any{"hook_event_name": "Stop", "session_id": "sess-1", "transcript_path": claudeTranscript(t)})

	mu.Lock()
	defer mu.Unlock()
	require.Len(t, *spans, 3, "spans: %v", spanNames(*spans))

	exported := map[string]bool{}
	for _, span := range *spans {
		exported[span.SpanID] = true
	}
	for _, span := range *spans {
		if span.ParentSpanID == "" {
			continue
		}
		assert.True(t, exported[span.ParentSpanID], "span %s references an unexported parent", span.Name)
	}
	for _, span := range *spans {
		if span.Name != "execute_tool Skill" && span.Name != "invoke_agent Explore" {
			continue
		}
		assert.Equal(t, chat.SpanID, span.ParentSpanID, "span %s reparents onto the delegating turn", span.Name)
	}
}

// TestProcess_ToolsDisabledElsewhereKeepsTheSpan is the counterpart of the
// otlp package's "survives elsewhere" tests: a runtime that does not expose the
// dimensions must export the same spans it always has, whatever a shared DASH0_*
// fallback resolved Tools to.
func TestProcess_ToolsDisabledElsewhereKeepsTheSpan(t *testing.T) {
	url, spans, mu := mockOTLPServer(t)
	s := newSetup(t, url)
	s.cfg.Tools = otlp.LevelDisabled

	s.startTurn(t)
	s.feed(t, bashCall())

	mu.Lock()
	defer mu.Unlock()
	require.Len(t, *spans, 1, "spans: %v", spanNames(*spans))
	assert.Equal(t, "execute_tool Bash", (*spans)[0].Name)
}

func hasAttr(attrs []otlp.Attribute, key string) bool {
	for _, a := range attrs {
		if a.Key == key {
			return true
		}
	}
	return false
}
