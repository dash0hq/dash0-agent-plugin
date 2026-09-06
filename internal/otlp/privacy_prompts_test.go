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
	promptText       = "summarize the outage for customer 4711"
	responseText     = "The outage lasted 12 minutes."
	conversationName = "Customer 4711 outage"
)

func promptTurnEvent() map[string]any {
	return map[string]any{
		"hook_event_name":            "Stop",
		"session_id":                 "sess-privacy",
		"model":                      "claude-sonnet-4-20250514",
		"prompt":                     promptText,
		"last_assistant_message":     responseText,
		"gen_ai.usage.input_tokens":  int64(1200),
		"gen_ai.usage.output_tokens": int64(340),
		"gen_ai.conversation.name":   conversationName,
	}
}

func promptTurnSpan(level Level) Span {
	start := time.Date(2025, 6, 15, 12, 0, 0, 0, time.UTC)
	end := time.Date(2025, 6, 15, 12, 0, 45, 0, time.UTC)
	return NewLLMSpan("abc123traceabc123traceabc123tr", "span1234span1234", "parentidparentid",
		start, end, promptTurnEvent(), false,
		Config{Prompts: level, Provider: "anthropic", Dimensions: true})
}

// TestPromptsDisabledOmitsMessageAttributes covers the spec scenario "Disabled
// omits the attributes entirely": at prompts: disabled the message attributes
// are absent rather than carrying a placeholder, and no character count is
// exported in their place.
func TestPromptsDisabledOmitsMessageAttributes(t *testing.T) {
	span := promptTurnSpan(LevelDisabled)

	assertNoAttr(t, span.Attributes, "gen_ai.input.messages")
	assertNoAttr(t, span.Attributes, "gen_ai.output.messages")
	assertNoAttr(t, span.Attributes, "dash0.gen_ai.input.messages.withheld_characters")
	assertNoAttr(t, span.Attributes, "dash0.gen_ai.output.messages.withheld_characters")
	assertNoAttr(t, span.Attributes, "gen_ai.conversation.name")

	for _, a := range span.Attributes {
		if a.Value.StringValue == nil {
			continue
		}
		assert.NotContains(t, *a.Value.StringValue, "REDACTED",
			"attribute %s must not carry a placeholder at prompts: disabled", a.Key)
	}
}

// TestPromptsLimitedReportsWithheldCharacters covers the spec scenario "Limited
// preserves structure and reports size": the envelope and role survive, the
// content does not, and the count says how much was withheld.
func TestPromptsLimitedReportsWithheldCharacters(t *testing.T) {
	span := promptTurnSpan(LevelLimited)

	assertAttrContains(t, span.Attributes, "gen_ai.input.messages", `"role":"user"`)
	assertAttrContains(t, span.Attributes, "gen_ai.input.messages", "REDACTED")
	assertAttrContains(t, span.Attributes, "gen_ai.output.messages", `"role":"assistant"`)
	assertAttrContains(t, span.Attributes, "gen_ai.output.messages", "REDACTED")

	assertIntAttr(t, span.Attributes, "dash0.gen_ai.input.messages.withheld_characters",
		int64(len([]rune(promptText))))
	assertIntAttr(t, span.Attributes, "dash0.gen_ai.output.messages.withheld_characters",
		int64(len([]rune(responseText))))
}

// TestWithheldCharacterCountIsRunesNotBytes pins the count to characters, which
// is what the attribute name promises: a multi-byte prompt would otherwise
// report a number nobody can compare against a character limit.
func TestWithheldCharacterCountIsRunesNotBytes(t *testing.T) {
	event := promptTurnEvent()
	event["prompt"] = "grüße 世界"

	span := NewLLMSpan("abc123traceabc123traceabc123tr", "span1234span1234", "",
		time.Now(), time.Now(), event, false, Config{Prompts: LevelLimited, Dimensions: true})

	assertIntAttr(t, span.Attributes, "dash0.gen_ai.input.messages.withheld_characters", 8)
}

// TestWithheldCharacterCountIsOptIn pins the counts to the runtimes that asked
// for them. Every runtime defaults to prompts: limited through omit_io, so an
// ungated count would add two attributes to spans the spec requires to be
// unchanged.
func TestWithheldCharacterCountIsOptIn(t *testing.T) {
	span := NewLLMSpan("abc123traceabc123traceabc123tr", "span1234span1234", "",
		time.Now(), time.Now(), promptTurnEvent(), false, Config{Prompts: LevelLimited})

	assertNoAttr(t, span.Attributes, "dash0.gen_ai.input.messages.withheld_characters")
	assertNoAttr(t, span.Attributes, "dash0.gen_ai.output.messages.withheld_characters")
	assertAttrContains(t, span.Attributes, "gen_ai.input.messages", "REDACTED")
}

// TestPromptLevelsLeaveTheTurnMetadataAlone covers the spec scenario "Token
// usage survives every level": the prompts dimension governs content, so
// everything the chat span reports about the turn itself is identical at all
// three levels.
func TestPromptLevelsLeaveTheTurnMetadataAlone(t *testing.T) {
	metadataKeys := []string{
		"gen_ai.request.model",
		"gen_ai.provider.name",
		"gen_ai.usage.input_tokens",
		"gen_ai.usage.output_tokens",
		"gen_ai.conversation.id",
		"gen_ai.operation.name",
	}

	disabled := promptTurnSpan(LevelDisabled)
	limited := promptTurnSpan(LevelLimited)
	full := promptTurnSpan(LevelFull)

	for _, other := range []Span{limited, full} {
		assert.Equal(t, disabled.StartTimeUnixNano, other.StartTimeUnixNano)
		assert.Equal(t, disabled.EndTimeUnixNano, other.EndTimeUnixNano)
		assert.Equal(t, disabled.Status, other.Status)
		assert.Equal(t, disabled.Name, other.Name)

		for _, key := range metadataKeys {
			want, ok := attrValue(disabled.Attributes, key)
			require.True(t, ok, "attribute %s missing at prompts: disabled", key)
			got, ok := attrValue(other.Attributes, key)
			require.True(t, ok, "attribute %s missing", key)
			assert.Equal(t, want, got, "attribute %s", key)
		}
	}

	assertAttrContains(t, full.Attributes, "gen_ai.input.messages", promptText)
	assertAttrContains(t, full.Attributes, "gen_ai.output.messages", responseText)
}

// TestConversationNameFollowsThePromptsDimension pins the conversation name as
// governed content rather than turn metadata: the title is derived from the
// user's first prompt, so it moves with the prompts level.
func TestConversationNameFollowsThePromptsDimension(t *testing.T) {
	assertNoAttr(t, promptTurnSpan(LevelDisabled).Attributes, "gen_ai.conversation.name")
	assertAttr(t, promptTurnSpan(LevelLimited).Attributes, "gen_ai.conversation.name", redactedValue)
	assertAttr(t, promptTurnSpan(LevelFull).Attributes, "gen_ai.conversation.name", conversationName)
}

func attrValue(attrs []Attribute, key string) (AttrValue, bool) {
	for _, a := range attrs {
		if a.Key == key {
			return a.Value, true
		}
	}
	return AttrValue{}, false
}
