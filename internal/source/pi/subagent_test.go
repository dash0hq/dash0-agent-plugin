// SPDX-FileCopyrightText: Copyright 2026 Dash0 Inc.
// SPDX-License-Identifier: Apache-2.0

package pi

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/dash0hq/dash0-agent-plugin/internal/otlp"
)

func subagentTurn(subs ...Subagent) Turn {
	return Turn{SessionID: "s1", Start: at(0), End: at(9), Status: "done",
		Calls: []Call{{Provider: "claude-code", Model: "claude-sonnet-5", Start: at(0), End: at(1), StopReason: "toolUse",
			Usage: Usage{Input: 1, Output: 1}}},
		Tools: []Tool{{ID: "call_sub", Name: "subagent", Start: at(1), End: at(8), Call: ptr(0), Subagents: subs}}}
}

// A foreground child runs inside the Pi process, so the parent's subagent
// tool result is the only record of it. It is an invoke_agent span under
// that tool, as on OpenCode V2 and Codex, and its tokens are on one chat span
// under the agent, which is where Dash0's cost metrics read them.
func TestForegroundSubagentIsAnAgentUnderItsToolWithItsTokensOnce(t *testing.T) {
	spans := build(t, subagentTurn(Subagent{
		Agent: "delegate", Model: "claude-code/claude-haiku-4-5", Turns: 2, BillingMode: "subscription",
		Usage: Usage{Input: 4, Output: 460, CacheRead: 5145, CacheWrite: 5211},
	}), true)
	require.Len(t, spans, 4)
	root, tool, agent, usage := spans[0], spans[1], spans[2], spans[3]
	require.Equal(t, "execute_tool subagent", tool.Name)
	require.Equal(t, "invoke_agent delegate", agent.Name)
	require.Equal(t, "chat claude-haiku-4-5", usage.Name)
	require.Equal(t, tool.SpanID, agent.ParentSpanID)
	require.Equal(t, agent.SpanID, usage.ParentSpanID)
	for _, s := range spans {
		require.Equal(t, root.TraceID, s.TraceID)
	}
	require.Equal(t, tool.StartTimeUnixNano, agent.StartTimeUnixNano)
	require.Equal(t, tool.EndTimeUnixNano, usage.EndTimeUnixNano)

	a := attrs(agent)
	require.Equal(t, "invoke_agent", a["gen_ai.operation.name"])
	require.Equal(t, "delegate", a["gen_ai.agent.name"])
	require.Equal(t, "anthropic", a["gen_ai.provider.name"])
	require.Equal(t, "claude-haiku-4-5", a["gen_ai.request.model"])
	require.Equal(t, "s1", a["gen_ai.conversation.id"])
	for k := range a {
		require.NotContains(t, k, "gen_ai.usage.", "the tokens are on the chat child")
	}

	u := attrs(usage)
	require.Equal(t, "chat", u["gen_ai.operation.name"])
	require.Equal(t, "delegate", u["gen_ai.agent.name"])
	require.Equal(t, "claude-code", u["dash0.pi.provider"])
	require.Equal(t, "10360", u["gen_ai.usage.input_tokens"])
	require.Equal(t, "460", u["gen_ai.usage.output_tokens"])
	require.Equal(t, "5145", u["gen_ai.usage.cache_read.input_tokens"])
	require.Equal(t, "5211", u["gen_ai.usage.cache_creation.input_tokens"])
	require.Equal(t, "subscription", u["dash0.gen_ai.billing_mode"])
	require.Equal(t, "aggregate", u["dash0.pi.subagent.timing"])
	require.Equal(t, "2", u["dash0.pi.subagent.turns"])

	// The parent's own spans keep the configured agent name.
	require.Equal(t, "pi", attrs(root)["gen_ai.agent.name"])
	require.Equal(t, "pi", attrs(tool)["gen_ai.agent.name"])
}

// A parallel call runs several children, and each one is its own agent.
func TestEveryChildOfAParallelCallIsItsOwnAgent(t *testing.T) {
	spans := build(t, subagentTurn(
		Subagent{Agent: "scout", Model: "openai-codex/gpt-5.5", Turns: 1, Usage: Usage{Input: 10, Output: 1}},
		Subagent{Agent: "scout", Model: "openai-codex/gpt-5.5", Turns: 1, Usage: Usage{Input: 20, Output: 2}, Failed: true},
	), true)
	require.Len(t, spans, 6)
	require.NotEqual(t, spans[2].SpanID, spans[4].SpanID)
	require.Equal(t, "openai", attrs(spans[2])["gen_ai.provider.name"])
	require.Equal(t, otlp.StatusCodeUnset, spans[2].Status.Code)
	require.Equal(t, otlp.StatusCodeError, spans[4].Status.Code)
	require.Equal(t, "Pi sub-agent failed", spans[4].Status.Message)
	require.Equal(t, "20", attrs(spans[5])["gen_ai.usage.input_tokens"])
}

// A child that never answered has no model and no tokens to report. Its
// agent span still records that it ran.
func TestChildWithoutAModelHasNoUsageSpan(t *testing.T) {
	spans := build(t, subagentTurn(Subagent{Agent: "worker", Failed: true}), true)
	require.Len(t, spans, 3)
	require.Equal(t, "invoke_agent worker", spans[2].Name)
}

// The child data comes from another extension's tool result, so a bad child
// loses its own spans, as a bad tool does, not the turn.
func TestInvalidChildIsSkippedNotFatal(t *testing.T) {
	for name, sub := range map[string]Subagent{
		"bad agent":       {Agent: "a b", Model: "p/m"},
		"bad model":       {Agent: "a", Model: "p/m m"},
		"negative tokens": {Agent: "a", Model: "p/m", Usage: Usage{Input: -1}},
		"negative turns":  {Agent: "a", Model: "p/m", Turns: -1},
		"bad billing":     {Agent: "a", Model: "p/m", BillingMode: "api"},
	} {
		t.Run(name, func(t *testing.T) {
			require.Len(t, build(t, subagentTurn(sub), true), 2)
		})
	}
}

// pi-subagents names a package or nested agent with a scope, for example
// "team/reviewer". The agent name is free text to it.
func TestScopedAgentNameIsKept(t *testing.T) {
	spans := build(t, subagentTurn(Subagent{Agent: "team/reviewer", Model: "p/m", Usage: Usage{Input: 1}}), true)
	require.Equal(t, "invoke_agent team/reviewer", spans[2].Name)
}

func TestSplitModelKeepsSlashesInTheModel(t *testing.T) {
	p, m := splitModel("openrouter/anthropic/claude-sonnet-5")
	require.Equal(t, "openrouter", p)
	require.Equal(t, "anthropic/claude-sonnet-5", m)
	p, m = splitModel("bare")
	require.Empty(t, p)
	require.Equal(t, "bare", m)
}
