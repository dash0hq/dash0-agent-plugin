// SPDX-FileCopyrightText: Copyright 2026 Dash0 Inc.
// SPDX-License-Identifier: Apache-2.0

package copilot

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/dash0hq/dash0-agent-plugin/internal/otlp"
)

func TestAttachUsage_carriesBothModels(t *testing.T) {
	event := map[string]any{}
	AttachUsage(event, &Usage{
		InputTokens: 24085, OutputTokens: 47,
		Model:         "auto",
		ResponseModel: "gpt-5.6-luna",
	})

	assert.Equal(t, "auto", event["model"], "what was asked for must survive")
	assert.Equal(t, "gpt-5.6-luna", event["response_model"])
}

// A model already on the event came from the hook payload, which is closer to
// the turn than the file rollup is.
func TestAttachUsage_doesNotOverwriteEitherModel(t *testing.T) {
	event := map[string]any{"model": "pinned", "response_model": "pinned-response"}
	AttachUsage(event, &Usage{
		InputTokens: 1,
		Model:       "auto", ResponseModel: "gpt-5.6-luna",
	})

	assert.Equal(t, "pinned", event["model"])
	assert.Equal(t, "pinned-response", event["response_model"])
}

// An empty gen_ai.response.model would shadow the request model for a reader
// that prefers the response and falls back only on absence.
func TestAttachUsage_omitsAnAbsentResponseModel(t *testing.T) {
	event := map[string]any{}
	AttachUsage(event, &Usage{InputTokens: 10, Model: "gpt"})

	assert.Equal(t, "gpt", event["model"])
	_, present := event["response_model"]
	assert.False(t, present, "an absent responding model must add no key")
}

// Cache writes are reported by the desktop app only, so a zero means "not
// reported" and must not be sent as a measured zero.
func TestAttachUsage_cacheCreationOnlyWhenReported(t *testing.T) {
	event := map[string]any{}
	AttachUsage(event, &Usage{InputTokens: 10})
	_, present := event["gen_ai.usage.cache_creation.input_tokens"]
	assert.False(t, present)

	AttachUsage(event, &Usage{InputTokens: 10, CacheCreationInputTokens: 7})
	assert.Equal(t, int64(7), event["gen_ai.usage.cache_creation.input_tokens"])
}

// emitted runs emit with the debug log as the only sink and returns each span's
// attributes, keyed by span name.
func emitted(t *testing.T, emit func(cfg otlp.Config)) map[string]map[string]string {
	t.Helper()
	file := filepath.Join(t.TempDir(), "debug.log")
	emit(otlp.Config{Debug: true, DebugFile: file, AgentName: "configured"})
	body, err := os.ReadFile(file)
	require.NoError(t, err)

	spans := map[string]map[string]string{}
	for _, line := range strings.Split(strings.TrimSpace(string(body)), "\n") {
		_, payload, _ := strings.Cut(line, "] ")
		var req otlp.ExportTracesRequest
		require.NoError(t, json.Unmarshal([]byte(payload), &req))
		span := req.ResourceSpans[0].ScopeSpans[0].Spans[0]
		attrs := map[string]string{}
		for _, a := range span.Attributes {
			switch {
			case a.Value.StringValue != nil:
				attrs[a.Key] = *a.Value.StringValue
			case a.Value.IntValue != nil:
				attrs[a.Key] = *a.Value.IntValue
			}
		}
		spans[span.Name] = attrs
	}
	return spans
}

func subAgentTurn(usage *Usage) *Turn {
	return &Turn{
		Usage: &Usage{Model: "claude-opus-5.5", ResponseModel: "claude-opus-5.5"},
		Tools: []ToolCall{
			{SpanID: "aaaaaaaaaaaaaaa1", Name: "task", CallID: "c1"},
			{SpanID: "aaaaaaaaaaaaaaa2", ParentSpanID: "bbbbbbbbbbbbbbb1", Name: "view", CallID: "c2"},
		},
		Agents: []SubAgent{{SpanID: "bbbbbbbbbbbbbbb1", ParentSpanID: "aaaaaaaaaaaaaaa1",
			AgentType: "explore", CallID: "c1", Model: "gpt-5.6-luna", Usage: usage}},
	}
}

var testCtx = &otlp.TraceContext{TraceID: "0123456789abcdef0123456789abcdef", SpanID: "cccccccccccccccc", SessionID: "s1"}

// A sub-agent's tool names the sub-agent and its model, as its invoke_agent span
// does; the parent's tools keep the configured agent and the turn's model.
func TestEmitToolSpans_subAgentToolNamesTheSubAgent(t *testing.T) {
	spans := emitted(t, func(cfg otlp.Config) { EmitToolSpans(subAgentTurn(nil), testCtx, cfg, "test") })

	view := spans["execute_tool view"]
	assert.Equal(t, "explore", view["gen_ai.agent.name"])
	assert.Equal(t, "c1", view["gen_ai.agent.id"])
	assert.Equal(t, "gpt-5.6-luna", view["gen_ai.request.model"])

	task := spans["execute_tool task"]
	assert.Equal(t, "configured", task["gen_ai.agent.name"])
	assert.NotContains(t, task, "gen_ai.agent.id")
	assert.Equal(t, "claude-opus-5.5", task["gen_ai.request.model"])
}

// On the CLI, a sub-agent has no model or usage of its own. Its tools still
// name it, as on the other runtimes, and keep the turn's model.
func TestEmitToolSpans_cliSubAgentToolNamesTheSubAgent(t *testing.T) {
	turn := subAgentTurn(nil)
	turn.Agents[0].Model = ""
	spans := emitted(t, func(cfg otlp.Config) { EmitToolSpans(turn, testCtx, cfg, "test") })

	view := spans["execute_tool view"]
	assert.Equal(t, "explore", view["gen_ai.agent.name"])
	assert.Equal(t, "c1", view["gen_ai.agent.id"])
	assert.Equal(t, "claude-opus-5.5", view["gen_ai.request.model"])
}

func TestEmitAgentSpans_usageOnlyWhenAttributed(t *testing.T) {
	spans := emitted(t, func(cfg otlp.Config) { EmitAgentSpans(subAgentTurn(nil), testCtx, cfg, "test") })
	assert.NotContains(t, spans["invoke_agent explore"], "gen_ai.usage.input_tokens",
		"the CLI folds sub-agent tokens into the chat span; repeating them here would double them")

	spans = emitted(t, func(cfg otlp.Config) {
		EmitAgentSpans(subAgentTurn(&Usage{InputTokens: 70, OutputTokens: 5, CacheReadInputTokens: 60}), testCtx, cfg, "test")
	})
	agent := spans["invoke_agent explore"]
	assert.Equal(t, "70", agent["gen_ai.usage.input_tokens"])
	assert.Equal(t, "5", agent["gen_ai.usage.output_tokens"])
	assert.Equal(t, "60", agent["gen_ai.usage.cache_read.input_tokens"])
	assert.Equal(t, "gpt-5.6-luna", agent["gen_ai.request.model"])
}

// An auto-mode sub-agent keeps "auto" as its request and its resolved model as
// the response, on its invoke_agent span and its tools, as the turn's chat does.
func TestEmitAgentSpans_autoSubAgentKeepsBothModels(t *testing.T) {
	turn := subAgentTurn(&Usage{InputTokens: 70, Model: "auto", ResponseModel: "gpt-5.6-luna"})
	spans := emitted(t, func(cfg otlp.Config) {
		EmitAgentSpans(turn, testCtx, cfg, "test")
		EmitToolSpans(turn, testCtx, cfg, "test")
	})
	for _, name := range []string{"invoke_agent explore", "execute_tool view"} {
		assert.Equal(t, "auto", spans[name]["gen_ai.request.model"], name)
		assert.Equal(t, "gpt-5.6-luna", spans[name]["gen_ai.response.model"], name)
	}
}
