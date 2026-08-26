// SPDX-FileCopyrightText: Copyright 2026 Dash0 Inc.
// SPDX-License-Identifier: Apache-2.0

package pipeline

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/dash0hq/dash0-agent-plugin/internal/otlp"
)

// Codex reuses a sub-agent: SubagentStop ends a TASK, and the same agent_id then
// runs more tools and stops again, with no second SubagentStart. Consuming its
// trace context at the first stop dropped every span of that later work —
// measured on qa/runs/probe-codex-nested-anchored, where 7 PostToolUse hooks
// produced 5 execute_tool spans, the two missing ones being calls the agent made
// after its stop. Worse, the dropped nested spawn was itself the anchor for the
// agent it created, so that agent's spans had no parent in the trace either.
func TestProcess_Codex_ToolCallAfterSubagentStopStillGetsASpan(t *testing.T) {
	url, spans, mu := mockOTLPServer(t)
	s := newSetup(t, url)
	s.cfg.HarnessName = "codex"

	const agentID = "01a03cbf-0505-f358-3743-000000000000"

	s.feed(t, map[string]any{"hook_event_name": "SessionStart", "session_id": "sess-1", "model": "gpt-5.6"})
	s.feed(t, map[string]any{"hook_event_name": "UserPromptSubmit", "session_id": "sess-1", "prompt": "delegate"})
	s.feed(t, map[string]any{"hook_event_name": "SubagentStart", "session_id": "sess-1", "agent_id": agentID})
	s.feed(t, map[string]any{"hook_event_name": "SubagentStop", "session_id": "sess-1", "agent_id": agentID})

	// The agent keeps working after its stop. No SubagentStart precedes this:
	// Codex does not emit one, which is why re-arming on that event is not an
	// option.
	s.feed(t, map[string]any{
		"hook_event_name": "PostToolUse", "session_id": "sess-1", "agent_id": agentID,
		"tool_name": "Bash", "tool_use_id": "call-after-stop",
		"tool_input": map[string]any{"command": "echo late"},
	})

	mu.Lock()
	defer mu.Unlock()
	var tool *otlp.Span
	for i := range *spans {
		if hasStringAttr((*spans)[i].Attributes, "gen_ai.tool.call.id", "call-after-stop") {
			tool = &(*spans)[i]
		}
	}
	require.NotNil(t, tool, "the tool call after SubagentStop must still produce a span")
	assert.NotEmpty(t, tool.ParentSpanID,
		"and it must have a parent: falling back to no parent would orphan it in the trace")
}

// Claude sub-agents stop once, so the opposite must hold there: a tool hook
// arriving after SubagentStop has no snapshot left and must fail closed rather
// than fall back to whichever session turn is current and invent a parent. That
// is what qa/specs/claude/session/sub-agent-tool-call-produces-a-span.md guards, and
// the Codex change above must not weaken it.
func TestProcess_Claude_ToolCallAfterSubagentStopStaysDropped(t *testing.T) {
	url, spans, mu := mockOTLPServer(t)
	s := newSetup(t, url)
	s.cfg.HarnessName = "claude-code"

	const agentID = "a4ace09206cc065bd"

	s.feed(t, map[string]any{"hook_event_name": "SessionStart", "session_id": "sess-1", "model": "haiku"})
	s.feed(t, map[string]any{"hook_event_name": "UserPromptSubmit", "session_id": "sess-1", "prompt": "delegate"})
	s.feed(t, map[string]any{"hook_event_name": "SubagentStart", "session_id": "sess-1", "agent_id": agentID})
	s.feed(t, map[string]any{"hook_event_name": "SubagentStop", "session_id": "sess-1", "agent_id": agentID})
	s.feed(t, map[string]any{
		"hook_event_name": "PostToolUse", "session_id": "sess-1", "agent_id": agentID,
		"tool_name": "Bash", "tool_use_id": "call-after-stop",
		"tool_input": map[string]any{"command": "echo stale"},
	})

	mu.Lock()
	defer mu.Unlock()
	for _, sp := range *spans {
		assert.False(t, hasStringAttr(sp.Attributes, "gen_ai.tool.call.id", "call-after-stop"),
			"a stale Claude sub-agent call must not be exported")
	}
}
