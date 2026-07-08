// SPDX-FileCopyrightText: Copyright 2026 Dash0 Inc.
// SPDX-License-Identifier: Apache-2.0

package otlp

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestProviderForModel(t *testing.T) {
	cases := []struct {
		model string
		want  string
	}{
		{"", ""},
		{"claude-sonnet-4-5", "anthropic"},
		{"claude-opus-4-7", "anthropic"},
		{"claude-haiku-4-5-20251001", "anthropic"},
		{"gpt-5", "openai"},
		{"gpt-4o-mini", "openai"},
		{"o1", "openai"},
		{"o1-preview", "openai"},
		{"o3", "openai"},
		{"o3-mini", "openai"},
		{"o3-pro", "openai"},
		{"o4", "openai"},
		{"o4-mini", "openai"},
		{"codex-mini", "openai"},
		{"gemini-2.5-pro", "gcp.gemini"},
		{"grok-4", "x_ai"},
		{"deepseek-v3", "deepseek"},
		{"mistral-large", "mistral_ai"},
		{"cursor-auto", "cursor"},
		{"default", ""},
		{"some-unknown-model", ""},
	}
	for _, c := range cases {
		t.Run(c.model, func(t *testing.T) {
			assert.Equal(t, c.want, ProviderForModel(c.model))
		})
	}
}

func TestResolveProviderPrefersModelOverConfig(t *testing.T) {
	event := map[string]any{"model": "gpt-5"}
	cfg := Config{Provider: "anthropic"}
	assert.Equal(t, "openai", resolveProvider(event, cfg))
}

func TestResolveProviderFallsBackToConfig(t *testing.T) {
	event := map[string]any{"hook_event_name": "SessionStart"}
	cfg := Config{Provider: "anthropic"}
	assert.Equal(t, "anthropic", resolveProvider(event, cfg))
}

func TestResolveProviderUnknownModelFallsBackToConfig(t *testing.T) {
	event := map[string]any{"model": "weird-model"}
	cfg := Config{Provider: "anthropic"}
	assert.Equal(t, "anthropic", resolveProvider(event, cfg))
}

func TestResolveProviderEmptyWhenNoSignal(t *testing.T) {
	event := map[string]any{"hook_event_name": "PreToolUse"}
	cfg := Config{} // Cursor entrypoint shape: no Provider set
	assert.Equal(t, "", resolveProvider(event, cfg))
}
