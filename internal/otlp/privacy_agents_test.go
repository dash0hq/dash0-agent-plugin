// SPDX-FileCopyrightText: Copyright 2026 Dash0 Inc.
// SPDX-License-Identifier: Apache-2.0

package otlp

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func subAgentTurnEvent() map[string]any {
	return map[string]any{
		"hook_event_name":            "SubagentStop",
		"session_id":                 "sess-privacy",
		"agent_id":                   "agent-1",
		"agent_type":                 "Explore",
		"model":                      "claude-sonnet-4-20250514",
		"prompt":                     promptText,
		"last_assistant_message":     responseText,
		"gen_ai.usage.input_tokens":  int64(900),
		"gen_ai.usage.output_tokens": int64(210),
	}
}

// Prompts is deliberately LevelFull: the sub-agent's content follows Agents,
// so the level governing the user's own turn must not reach it.
func subAgentSpan(level Level) Span {
	start := time.Date(2025, 6, 15, 12, 0, 0, 0, time.UTC)
	end := time.Date(2025, 6, 15, 12, 0, 30, 0, time.UTC)
	return NewLLMSpan("abc123traceabc123traceabc123tr", "span1234span1234", "parentidparentid",
		start, end, subAgentTurnEvent(), false,
		Config{Agents: level, Prompts: LevelFull, Provider: "anthropic", Dimensions: true})
}

// TestAgentsLimitedNamesTheAgentWithoutItsContent covers the spec scenario
// "Agent name without sub-agent content": the delegation is reported, what it
// said is not.
func TestAgentsLimitedNamesTheAgentWithoutItsContent(t *testing.T) {
	span := subAgentSpan(LevelLimited)

	assert.Equal(t, "invoke_agent Explore", span.Name)
	assertAttr(t, span.Attributes, "gen_ai.agent.name", "Explore")
	assertAttr(t, span.Attributes, "gen_ai.operation.name", "invoke_agent")
	assertIntAttr(t, span.Attributes, "gen_ai.usage.input_tokens", 900)

	assertNoAttr(t, span.Attributes, "gen_ai.input.messages")
	assertNoAttr(t, span.Attributes, "gen_ai.output.messages")
	assertNoAttr(t, span.Attributes, "dash0.gen_ai.input.messages.withheld_characters")
	assertNoAttr(t, span.Attributes, "dash0.gen_ai.output.messages.withheld_characters")

	for _, a := range span.Attributes {
		if a.Value.StringValue == nil {
			continue
		}
		assert.NotContains(t, *a.Value.StringValue, promptText, "attribute %s", a.Key)
		assert.NotContains(t, *a.Value.StringValue, responseText, "attribute %s", a.Key)
	}
}

// TestAgentsFullCarriesTheSubAgentContent is the other side of the same
// override: at agents: full the delegation reports what it was asked and what
// it answered.
func TestAgentsFullCarriesTheSubAgentContent(t *testing.T) {
	span := subAgentSpan(LevelFull)

	assertAttrContains(t, span.Attributes, "gen_ai.input.messages", promptText)
	assertAttrContains(t, span.Attributes, "gen_ai.output.messages", responseText)
}

// TestAgentsDisabledOmitsTheSubAgentContent pins the attribute-level behaviour
// of the level whose span the pipeline suppresses outright: were the span built
// anyway, it would still carry nothing of the delegation's content.
func TestAgentsDisabledOmitsTheSubAgentContent(t *testing.T) {
	span := subAgentSpan(LevelDisabled)

	assertNoAttr(t, span.Attributes, "gen_ai.input.messages")
	assertNoAttr(t, span.Attributes, "gen_ai.output.messages")
	assertAttr(t, span.Attributes, "gen_ai.agent.name", "Explore")
}

// TestSubAgentConversationNameFollowsPrompts pins the session title to the
// prompts dimension on a sub-agent span too: the title is derived from the
// user's own first prompt, so the Agents override must not reach it.
func TestSubAgentConversationNameFollowsPrompts(t *testing.T) {
	spanAt := func(prompts, agents Level) Span {
		event := subAgentTurnEvent()
		event["gen_ai.conversation.name"] = "Refactor the exporter"
		return NewLLMSpan("abc123traceabc123traceabc123tr", "span1234span1234", "parentidparentid",
			time.Now(), time.Now(), event, false,
			Config{Prompts: prompts, Agents: agents, Dimensions: true})
	}

	assertNoAttr(t, spanAt(LevelDisabled, LevelFull).Attributes, "gen_ai.conversation.name")
	assertAttr(t, spanAt(LevelLimited, LevelFull).Attributes, "gen_ai.conversation.name", redactedValue)
	assertAttr(t, spanAt(LevelFull, LevelLimited).Attributes, "gen_ai.conversation.name", "Refactor the exporter")
}

// TestSubAgentContentFollowsPromptsElsewhere holds the other four runtimes
// still: without the dimensions a sub-agent's chat span is prompt content like
// any other, so it keeps the envelope omit_io has always exported.
func TestSubAgentContentFollowsPromptsElsewhere(t *testing.T) {
	span := NewLLMSpan("abc123traceabc123traceabc123tr", "span1234span1234", "",
		time.Now(), time.Now(), subAgentTurnEvent(), false,
		Config{Prompts: LevelLimited, Agents: LevelDisabled})

	assertAttrContains(t, span.Attributes, "gen_ai.input.messages", "REDACTED")
	assertAttrContains(t, span.Attributes, "gen_ai.output.messages", "REDACTED")
}
