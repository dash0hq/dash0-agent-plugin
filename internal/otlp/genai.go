// SPDX-FileCopyrightText: Copyright 2026 Dash0 Inc.
// SPDX-License-Identifier: Apache-2.0

package otlp

import "strings"

// ProviderForModel maps a model identifier to the OTel GenAI
// gen_ai.provider.name enum value. Returns "" when the model name doesn't
// match a known vendor — callers can then fall back to a runtime-level
// default (see Config.Provider).
//
// Mappings (model prefix → provider):
//
//	claude-*                                 → anthropic
//	gpt-*, o1[-*], o3[-*], o4[-*], codex-*   → openai
//	gemini-*                                 → gcp.gemini
//	grok-*                                   → x_ai
//	deepseek-*                               → deepseek
//	mistral-*                                → mistral_ai
//	cursor-auto                              → cursor (non-standard;
//	                                            Cursor's Auto router does
//	                                            not surface the chosen
//	                                            provider, so we tag the
//	                                            host instead)
//
// OpenAI's reasoning models ship under bare IDs (o1, o3) as well as
// hyphenated variants (o3-mini, o3-pro), so both forms are matched —
// see https://developers.openai.com/api/docs/models/all.
func ProviderForModel(model string) string {
	switch {
	case model == "":
		return ""
	case model == "cursor-auto":
		return "cursor"
	case strings.HasPrefix(model, "claude-"):
		return "anthropic"
	case strings.HasPrefix(model, "gpt-"),
		model == "o1", strings.HasPrefix(model, "o1-"),
		model == "o3", strings.HasPrefix(model, "o3-"),
		model == "o4", strings.HasPrefix(model, "o4-"),
		strings.HasPrefix(model, "codex-"):
		return "openai"
	case strings.HasPrefix(model, "gemini-"):
		return "gcp.gemini"
	case strings.HasPrefix(model, "grok-"):
		return "x_ai"
	case strings.HasPrefix(model, "deepseek-"):
		return "deepseek"
	case strings.HasPrefix(model, "mistral-"):
		return "mistral_ai"
	}
	return ""
}

// resolveProvider picks the gen_ai.provider.name value for an OTLP export.
// Per-event model wins (Cursor sessions can mix providers); cfg.Provider is
// the runtime-level fallback (Claude Code sets "anthropic" so non-LLM events
// like SessionStart still carry a provider tag).
func resolveProvider(event map[string]any, cfg Config) string {
	model, _ := event["model"].(string)
	if p := ProviderForModel(model); p != "" {
		return p
	}
	return cfg.Provider
}
