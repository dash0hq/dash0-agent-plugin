// SPDX-FileCopyrightText: Copyright 2026 Dash0 Inc.
// SPDX-License-Identifier: Apache-2.0

package pipeline

import (
	"strconv"
	"testing"
	"time"

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

// Each task of a reused agent is timed from the previous stop, not from the one
// SubagentStart that created the agent. Keeping the snapshot alive fixed the
// missing spans and left every task's span starting at the same instant:
// measured on qa/runs/spec-codex-agent-reuse, two invoke_agent spans both
// starting at 0.00s, the second running 16.9s for a task that took 8 and fully
// overlapping the first.
func TestProcess_Codex_ReusedAgentTasksDoNotOverlap(t *testing.T) {
	url, spans, mu := mockOTLPServer(t)
	s := newSetup(t, url)
	s.cfg.HarnessName = "codex"

	const agentID = "01a03cbf-0505-f358-3743-000000000000"
	start := time.Now().UTC()

	s.feedAt(t, map[string]any{"hook_event_name": "SessionStart", "session_id": "sess-1", "model": "gpt-5.6"}, start)
	s.feedAt(t, map[string]any{"hook_event_name": "UserPromptSubmit", "session_id": "sess-1", "prompt": "delegate"}, start)
	s.feedAt(t, map[string]any{"hook_event_name": "SubagentStart", "session_id": "sess-1", "agent_id": agentID},
		start.Add(1*time.Second))
	// Task one ends here, task two ends eight seconds later.
	firstStop := start.Add(5 * time.Second)
	secondStop := start.Add(13 * time.Second)
	s.feedAt(t, map[string]any{"hook_event_name": "SubagentStop", "session_id": "sess-1", "agent_id": agentID}, firstStop)
	s.feedAt(t, map[string]any{"hook_event_name": "SubagentStop", "session_id": "sess-1", "agent_id": agentID}, secondStop)

	mu.Lock()
	defer mu.Unlock()
	var agents []otlp.Span
	for _, sp := range *spans {
		if sp.Name == "invoke_agent" || hasStringAttr(sp.Attributes, "gen_ai.agent.id", agentID) {
			agents = append(agents, sp)
		}
	}
	require.Len(t, agents, 2, "one span per completed task")
	// The wire carries nanoseconds as strings; compare them as numbers.
	nanos := func(v string) int64 {
		n, err := strconv.ParseInt(v, 10, 64)
		require.NoError(t, err)
		return n
	}
	assert.NotEqual(t, agents[0].StartTimeUnixNano, agents[1].StartTimeUnixNano,
		"the second task must not start where the first did")
	assert.GreaterOrEqual(t, nanos(agents[1].StartTimeUnixNano), nanos(agents[0].EndTimeUnixNano),
		"the second task starts at or after the first one's end, so the spans do not overlap")
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
