// SPDX-FileCopyrightText: Copyright 2026 Dash0 Inc.
// SPDX-License-Identifier: Apache-2.0

package otlp

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	toolArguments = `{"command":"git commit -m \"fix the customer 4711 outage\""}`
	toolResult    = "[main 82717dc] fix the customer 4711 outage"
	toolFailure   = `git commit -m "fix the customer 4711 outage": nothing to commit`
)

func toolCallEvent() map[string]any {
	return map[string]any{
		"hook_event_name": "PostToolUse",
		"session_id":      "sess-privacy",
		"tool_name":       "Bash",
		"tool_use_id":     "tu-1",
		"tool_input":      toolArguments,
		"tool_response":   toolResult,
	}
}

func toolCallSpan(t *testing.T, event map[string]any, cfg Config, failed bool) Span {
	t.Helper()
	start := time.Date(2025, 6, 15, 12, 0, 30, 0, time.UTC)
	end := time.Date(2025, 6, 15, 12, 1, 0, 0, time.UTC)
	return NewToolSpan("aabbccddeeff00112233445566778899", "span1234span1234", "parentidparentid",
		start, end, event, failed, cfg)
}

// TestToolsLimitedOmitsTheOptInContentAttributes covers the spec scenario
// "Limited omits the opt-in content attributes": the execute_tool convention
// makes the arguments and the result Opt-In, so limited drops them rather than
// exporting a placeholder, and everything the convention requires stays.
func TestToolsLimitedOmitsTheOptInContentAttributes(t *testing.T) {
	span := toolCallSpan(t, toolCallEvent(), Config{Tools: LevelLimited, Dimensions: true}, false)

	assertNoAttr(t, span.Attributes, "gen_ai.tool.call.arguments")
	assertNoAttr(t, span.Attributes, "gen_ai.tool.call.result")

	assertAttr(t, span.Attributes, "gen_ai.operation.name", "execute_tool")
	assertAttr(t, span.Attributes, "gen_ai.tool.name", "Bash")
	assertAttr(t, span.Attributes, "gen_ai.tool.type", "function")
	assertAttr(t, span.Attributes, "gen_ai.tool.call.id", "tu-1")

	full := toolCallSpan(t, toolCallEvent(), Config{Tools: LevelFull, Dimensions: true}, false)
	assert.Equal(t, full.StartTimeUnixNano, span.StartTimeUnixNano)
	assert.Equal(t, full.EndTimeUnixNano, span.EndTimeUnixNano)
	assert.Equal(t, full.Status, span.Status)
	assertAttr(t, full.Attributes, "gen_ai.tool.call.arguments", toolArguments)
	assertAttr(t, full.Attributes, "gen_ai.tool.call.result", toolResult)
}

// TestToolsLimitedKeepsTheLegacyPlaceholderElsewhere pins the omission to the
// runtimes that expose the dimensions. Every other runtime reaches
// tools: limited through omit_io, and its spans have carried the placeholder
// since long before this capability existed.
func TestToolsLimitedKeepsTheLegacyPlaceholderElsewhere(t *testing.T) {
	span := toolCallSpan(t, toolCallEvent(), Config{Tools: LevelLimited}, false)

	assertAttr(t, span.Attributes, "gen_ai.tool.call.arguments", redactedValue)
	assertAttr(t, span.Attributes, "gen_ai.tool.call.result", redactedValue)
}

// TestFailedCallReportsItsErrorAtLimited covers the spec scenario "A failed call
// reports its error at limited": the status and the error type survive, the
// message does not, because it quotes the arguments.
func TestFailedCallReportsItsErrorAtLimited(t *testing.T) {
	event := toolCallEvent()
	event["hook_event_name"] = "PostToolUseFailure"
	event["error"] = toolFailure

	span := toolCallSpan(t, event, Config{Tools: LevelLimited, Dimensions: true}, true)

	assert.Equal(t, StatusCodeError, span.Status.Code)
	assertAttr(t, span.Attributes, "error.type", "_OTHER")

	assert.Empty(t, span.Status.Message)
	assertNoAttr(t, span.Attributes, "exception.message")
	assertNoSubstring(t, span, "customer 4711")
}

// TestFailedCallReportsItsMessageAtFull keeps the message where the arguments
// are exported anyway — withholding it there would cost the diagnostic and
// protect nothing.
func TestFailedCallReportsItsMessageAtFull(t *testing.T) {
	event := toolCallEvent()
	event["hook_event_name"] = "PostToolUseFailure"
	event["error"] = toolFailure

	span := toolCallSpan(t, event, Config{Tools: LevelFull, Dimensions: true}, true)

	assert.Equal(t, toolFailure, span.Status.Message)
	assertAttr(t, span.Attributes, "exception.message", toolFailure)
	assertAttr(t, span.Attributes, "error.type", "_OTHER")
}

// TestFailureMessageSurvivesElsewhere pins the withholding to the runtimes that
// expose the dimensions: the other four have exported exception.message on a
// failed tool span, at their omit_io default, all along.
func TestFailureMessageSurvivesElsewhere(t *testing.T) {
	event := toolCallEvent()
	event["hook_event_name"] = "PostToolUseFailure"
	event["error"] = toolFailure

	span := toolCallSpan(t, event, Config{Tools: LevelLimited}, true)

	assert.Equal(t, toolFailure, span.Status.Message)
	assertAttr(t, span.Attributes, "exception.message", toolFailure)
	assertNoAttr(t, span.Attributes, "error.type")
}

// TestSkillCallFollowsTheSkillsDimension covers both spec scenarios for the
// precedence between the two dimensions at the attribute level. The span
// suppression half is covered in internal/pipeline.
func TestSkillCallFollowsTheSkillsDimension(t *testing.T) {
	event := map[string]any{
		"hook_event_name": "PostToolUse",
		"session_id":      "sess-privacy",
		"tool_name":       "Skill",
		"tool_use_id":     "tu-2",
		"skill_name":      "otel-ottl",
		"tool_input":      toolArguments,
		"tool_response":   toolResult,
	}

	full := toolCallSpan(t, event, Config{Tools: LevelLimited, Skills: LevelFull, Dimensions: true}, false)
	assertAttr(t, full.Attributes, "gen_ai.tool.call.arguments", toolArguments)
	assertAttr(t, full.Attributes, "gen_ai.tool.call.result", toolResult)

	limited := toolCallSpan(t, event, Config{Tools: LevelFull, Skills: LevelLimited, Dimensions: true}, false)
	assertAttr(t, limited.Attributes, "dash0.gen_ai.tool.skill.name", "otel-ottl")
	assertNoAttr(t, limited.Attributes, "gen_ai.tool.call.arguments")
	assertNoAttr(t, limited.Attributes, "gen_ai.tool.call.result")
}

// A skill invocation is recognized by either spelling: Claude and OpenCode name
// the tool, Copilot ships no arguments and names the skill instead.
func TestIsSkillEvent(t *testing.T) {
	assert.True(t, IsSkillEvent(map[string]any{"tool_name": "Skill"}))
	assert.True(t, IsSkillEvent(map[string]any{"tool_name": "skill"}))
	assert.True(t, IsSkillEvent(map[string]any{"tool_name": "invoke_skill", "skill_name": "otel-ottl"}))
	assert.False(t, IsSkillEvent(map[string]any{"tool_name": "Bash"}))
}

// assertNoSubstring fails when any attribute value, or the status message,
// quotes text a level withheld.
func assertNoSubstring(t *testing.T, span Span, text string) {
	t.Helper()
	require.NotContains(t, span.Status.Message, text)
	for _, a := range span.Attributes {
		if a.Value.StringValue == nil {
			continue
		}
		assert.NotContains(t, *a.Value.StringValue, text, "attribute %s", a.Key)
	}
}
