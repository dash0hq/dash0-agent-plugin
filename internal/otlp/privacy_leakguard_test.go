// SPDX-FileCopyrightText: Copyright 2026 Dash0 Inc.
// SPDX-License-Identifier: Apache-2.0

package otlp

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// contentFixture carries a value for every contentKeys field. A content key
// added without a value here fails TestContentAttributesAreExactlyTheGoverned
// ones rather than slipping past the guard untested.
func contentFixture() map[string]any {
	return map[string]any{
		"tool_input":               map[string]any{"command": "git status --short"},
		"tool_response":            "M internal/otlp/otlp.go",
		"prompt":                   "summarize the outage for customer 4711",
		"last_assistant_message":   "The outage lasted 12 minutes.",
		"gen_ai.conversation.name": "Customer 4711 outage",
	}
}

func allLevels(l Level) Config {
	return Config{Prompts: l, Tools: l, Skills: l, Agents: l, Dimensions: true}
}

// TestEveryContentKeyIsGovernedByADimension is the guard against an ungoverned
// content field: a new contentKeys entry whose dimension is omitted resolves to
// dimUnset, and a new dimension without a levelFor case resolves to whatever
// that switch's default says rather than to what was configured.
func TestEveryContentKeyIsGovernedByADimension(t *testing.T) {
	full := allLevels(LevelFull)
	for key, dim := range contentKeys {
		require.NotEqual(t, dimUnset, dim, "content key %q is governed by no dimension", key)
		assert.Equal(t, LevelFull, full.levelFor(dim),
			"content key %q's dimension is not wired into levelFor", key)
	}
}

// TestContentAttributesAreExactlyTheGovernedOnes pins the set of attributes the
// levels can remove. An attribute that appears at full and not at disabled is
// content by definition, so one showing up here that is not in the expected set
// is a field the dimensions started governing without anyone saying so — and
// one missing from it is content that survives disabled.
func TestContentAttributesAreExactlyTheGovernedOnes(t *testing.T) {
	event := contentFixture()
	for key := range contentKeys {
		require.Contains(t, event, key,
			"content key %q has no contentFixture value, so this guard does not cover it", key)
	}

	full := attrKeySet(eventAttributes(contentFixture(), allLevels(LevelFull)))
	disabled := attrKeySet(eventAttributes(contentFixture(), allLevels(LevelDisabled)))

	var removed, added []string
	for key := range full {
		if !disabled[key] {
			removed = append(removed, key)
		}
	}
	for key := range disabled {
		if !full[key] {
			added = append(added, key)
		}
	}

	assert.ElementsMatch(t, []string{
		"gen_ai.tool.call.arguments",
		"gen_ai.tool.call.result",
		"gen_ai.input.messages",
		"gen_ai.output.messages",
		"gen_ai.conversation.name",
	}, removed)
	assert.Empty(t, added, "disabled must never add an attribute full does not carry")
}

func attrKeySet(attrs []Attribute) map[string]bool {
	keys := make(map[string]bool, len(attrs))
	for _, a := range attrs {
		keys[a.Key] = true
	}
	return keys
}
