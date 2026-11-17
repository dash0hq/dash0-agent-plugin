// SPDX-FileCopyrightText: Copyright 2026 Dash0 Inc.
// SPDX-License-Identifier: Apache-2.0

package pi

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func toolUsageTurn(tools ...Tool) Turn {
	return Turn{SessionID: "s1", Start: at(0), End: at(9), Status: "done",
		Calls: []Call{{Provider: "claude-code", Model: "claude-sonnet-5", Start: at(0), End: at(1), StopReason: "toolUse",
			Usage: Usage{Input: 1, Output: 1}}},
		Tools: tools}
}

// A tool can call a model itself. Its tokens are a chat span under the tool,
// so Dash0's token metrics count them. Pi gives no provider or model for
// them, so the span has neither.
func TestToolUsageIsAChatSpanUnderTheTool(t *testing.T) {
	spans := build(t, toolUsageTurn(Tool{ID: "t1", Name: "web_search", Start: at(1), End: at(5), Call: ptr(0),
		Usage: &Usage{Input: 10, Output: 20, CacheRead: 30, CacheWrite: 40, Reasoning: ptr[int64](5)}}), true)
	require.Len(t, spans, 3)
	root, tool, usage := spans[0], spans[1], spans[2]
	require.Equal(t, "execute_tool web_search", tool.Name)
	require.Equal(t, "chat", usage.Name)
	require.Equal(t, tool.SpanID, usage.ParentSpanID)
	require.Equal(t, root.TraceID, usage.TraceID)
	require.Equal(t, tool.StartTimeUnixNano, usage.StartTimeUnixNano)
	require.Equal(t, tool.EndTimeUnixNano, usage.EndTimeUnixNano)

	u := attrs(usage)
	require.Equal(t, "chat", u["gen_ai.operation.name"])
	require.Equal(t, "tool", u["dash0.pi.usage.source"])
	require.Equal(t, "80", u["gen_ai.usage.input_tokens"])
	require.Equal(t, "20", u["gen_ai.usage.output_tokens"])
	require.Equal(t, "30", u["gen_ai.usage.cache_read.input_tokens"])
	require.Equal(t, "40", u["gen_ai.usage.cache_creation.input_tokens"])
	require.Equal(t, "5", u["gen_ai.usage.reasoning.output_tokens"])
	require.Equal(t, "s1", u["gen_ai.conversation.id"])
	for _, k := range []string{"gen_ai.provider.name", "gen_ai.request.model", "dash0.pi.provider", "dash0.gen_ai.billing_mode"} {
		require.NotContains(t, u, k, "the usage names no provider or model")
	}
	// The root and the tool keep only their own counts.
	require.Equal(t, "1", attrs(root)["gen_ai.usage.input_tokens"])
	for k := range attrs(tool) {
		require.NotContains(t, k, "gen_ai.usage.")
	}
}

// Each nested tool reports its own usage, and a parent tool reports only its
// own, so a sum over the chat spans counts each token once.
func TestNestedToolUsageIsUnderEachTool(t *testing.T) {
	spans := build(t, toolUsageTurn(
		Tool{ID: "c/1", ParentID: "c", Name: "summarize", Start: at(2), End: at(3), Usage: &Usage{Input: 5, Output: 1}},
		Tool{ID: "c", Name: "codemode", Start: at(1), End: at(4), Call: ptr(0), Usage: &Usage{Input: 7, Output: 2}},
	), true)
	require.Len(t, spans, 5)
	byParent := map[string]map[string]string{}
	ids := map[string]string{}
	for _, s := range spans {
		a := attrs(s)
		if a["dash0.pi.usage.source"] == "tool" {
			byParent[s.ParentSpanID] = a
		}
		if id := a["gen_ai.tool.call.id"]; id != "" {
			ids[id] = s.SpanID
		}
	}
	require.Len(t, byParent, 2)
	require.Equal(t, "5", byParent[ids["c/1"]]["gen_ai.usage.input_tokens"])
	require.Equal(t, "7", byParent[ids["c"]]["gen_ai.usage.input_tokens"])
}

// No span when the tool reported no usage, all zeros, or a bad count. A
// subagent tool reports the total of its children, which their own chat
// spans already carry.
func TestToolUsageSpanIsSkipped(t *testing.T) {
	for name, tool := range map[string]Tool{
		"no usage":       {ID: "t1", Name: "bash", Start: at(1), End: at(2)},
		"zero usage":     {ID: "t1", Name: "bash", Start: at(1), End: at(2), Usage: &Usage{}},
		"negative count": {ID: "t1", Name: "bash", Start: at(1), End: at(2), Usage: &Usage{Input: -1, Output: 5}},
		"subagent total": {ID: "t1", Name: "subagent", Start: at(1), End: at(2), Usage: &Usage{Input: 4, Output: 460},
			Subagents: []Subagent{{Agent: "delegate", Model: "claude-code/claude-haiku-4-5", Turns: 1, Usage: Usage{Input: 4, Output: 460}}}},
	} {
		t.Run(name, func(t *testing.T) {
			spans := build(t, toolUsageTurn(tool), true)
			for _, s := range spans {
				require.NotEqual(t, "tool", attrs(s)["dash0.pi.usage.source"])
			}
			require.Equal(t, "execute_tool "+tool.Name, spans[1].Name, "a bad usage costs only its own span")
		})
	}
}

// The envelope names the field in snake_case, as the call usage does.
func TestToolUsageDecodesFromTheEnvelope(t *testing.T) {
	var tool Tool
	require.NoError(t, json.Unmarshal([]byte(`{"id":"t1","name":"bash","usage":{"input":3,"output":4,"cache_read":5,"cache_write":6,"cache_write_1h":2}}`), &tool))
	require.Equal(t, &Usage{Input: 3, Output: 4, CacheRead: 5, CacheWrite: 6, CacheWrite1h: ptr[int64](2)}, tool.Usage)
}
