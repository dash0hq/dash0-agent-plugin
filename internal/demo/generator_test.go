// SPDX-FileCopyrightText: Copyright 2026 Dash0 Inc.
// SPDX-License-Identifier: Apache-2.0

package demo

import (
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/dash0hq/dash0-agent-plugin/internal/otlp"
)

// attrMap flattens a span's attributes into a key/value map for assertions.
func attrMap(span otlp.Span) map[string]otlp.AttrValue {
	m := make(map[string]otlp.AttrValue, len(span.Attributes))
	for _, a := range span.Attributes {
		m[a.Key] = a.Value
	}
	return m
}

func TestGenerateTurnStructure(t *testing.T) {
	now := time.Now().UTC()

	// Run many times so the random tool-variant branches are all exercised.
	for i := 0; i < 200; i++ {
		req, err := GenerateTurn(now)
		require.NoError(t, err)

		require.Len(t, req.ResourceSpans, 1)
		scope := req.ResourceSpans[0].ScopeSpans
		require.Len(t, scope, 1)
		spans := scope[0].Spans
		require.Len(t, spans, 2, "a turn is exactly one chat span plus one tool span")

		chat, tool := spans[0], spans[1]

		// Chat is the root; tool is its child; both share the trace.
		assert.Empty(t, chat.ParentSpanID)
		assert.Equal(t, chat.SpanID, tool.ParentSpanID)
		assert.Equal(t, chat.TraceID, tool.TraceID)
		assert.NotEqual(t, chat.SpanID, tool.SpanID)

		chatAttrs := attrMap(chat)
		toolAttrs := attrMap(tool)

		// Operations are correct.
		assert.Equal(t, "chat", *chatAttrs["gen_ai.operation.name"].StringValue)
		assert.Equal(t, "execute_tool", *toolAttrs["gen_ai.operation.name"].StringValue)

		// Cost is enriched in the backend and must never be sent.
		for _, span := range spans {
			for _, a := range span.Attributes {
				assert.NotEqual(t, "dash0.gen_ai.usage.cost", a.Key, "cost must not be emitted")
			}
		}

		// Token usage is present on the chat span.
		for _, k := range []string{
			"gen_ai.usage.input_tokens",
			"gen_ai.usage.output_tokens",
			"gen_ai.usage.cache_creation.input_tokens",
			"gen_ai.usage.cache_read.input_tokens",
		} {
			require.NotNil(t, chatAttrs[k].IntValue, "missing %s", k)
		}

		// The tool span identifies exactly one variant.
		_, isBash := toolAttrs["dash0.gen_ai.tool.bash.command_family"]
		_, isMCP := toolAttrs["dash0.gen_ai.tool.mcp_server"]
		_, isSkill := toolAttrs["dash0.gen_ai.tool.skill.name"]
		variants := 0
		for _, b := range []bool{isBash, isMCP, isSkill} {
			if b {
				variants++
			}
		}
		assert.Equal(t, 1, variants, "exactly one tool variant per turn")
		require.NotNil(t, toolAttrs["gen_ai.tool.name"].StringValue)
		require.NotNil(t, toolAttrs["gen_ai.tool.call.id"].StringValue)

		// VCS + user + team attributes are shared and consistent across spans.
		assert.Equal(t, *chatAttrs["user.name"].StringValue, *toolAttrs["user.name"].StringValue)
		require.NotNil(t, chatAttrs["dash0.team.name"].StringValue)
		assert.Equal(t, *chatAttrs["dash0.team.name"].StringValue, *toolAttrs["dash0.team.name"].StringValue)
		assert.Contains(t, teams, *chatAttrs["dash0.team.name"].StringValue)
		assert.Equal(t, *chatAttrs["dash0.gen_ai.vcs.ref.head.name"].StringValue,
			*toolAttrs["dash0.gen_ai.vcs.ref.head.name"].StringValue)
		assert.Contains(t, *chatAttrs["dash0.gen_ai.vcs.ref.head.name"].StringValue, "ENG-")

		// The conversation id is shared and looks like a UUID.
		conv := *chatAttrs["gen_ai.conversation.id"].StringValue
		assert.Equal(t, conv, *toolAttrs["gen_ai.conversation.id"].StringValue)
		assert.Len(t, conv, 36)

		// Tool span is nested within the chat span's time window.
		chatStart, _ := strconv.ParseInt(chat.StartTimeUnixNano, 10, 64)
		chatEnd, _ := strconv.ParseInt(chat.EndTimeUnixNano, 10, 64)
		toolStart, _ := strconv.ParseInt(tool.StartTimeUnixNano, 10, 64)
		toolEnd, _ := strconv.ParseInt(tool.EndTimeUnixNano, 10, 64)
		assert.GreaterOrEqual(t, toolStart, chatStart)
		assert.LessOrEqual(t, toolEnd, chatEnd)
	}
}

func TestGenerateTurnUniqueSessions(t *testing.T) {
	now := time.Now().UTC()
	seen := make(map[string]bool)
	for i := 0; i < 50; i++ {
		req, err := GenerateTurn(now)
		require.NoError(t, err)
		conv := ""
		for _, a := range req.ResourceSpans[0].ScopeSpans[0].Spans[0].Attributes {
			if a.Key == "gen_ai.conversation.id" {
				conv = *a.Value.StringValue
			}
		}
		assert.False(t, seen[conv], "each turn gets a fresh session id")
		seen[conv] = true
	}
}
