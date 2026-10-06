// SPDX-FileCopyrightText: Copyright 2026 Dash0 Inc.
// SPDX-License-Identifier: Apache-2.0

package harness

import (
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"github.com/dash0hq/dash0-agent-plugin/internal/otlp"
)

// clearPrivacyEnv unsets every variable the resolution reads, so a subtest
// starts from "nothing configured" whatever ran before it. The memoized
// configuration goes too: a subtest that chdir'd into a directory with a file in
// it leaves that file's values cached past the chdir the framework undoes.
func clearPrivacyEnv(t *testing.T) {
	t.Helper()
	ResetConfig()
	for _, key := range []string{"OMIT_IO", "PROMPTS", "TOOLS", "SKILLS", "AGENTS"} {
		t.Setenv("DASH0_"+key, "")
		t.Setenv("OPENCODE_PLUGIN_OPTION_"+key, "")
	}
}

func TestPrivacyLevelResolution(t *testing.T) {
	t.Run("an untouched configuration is the omit_io: true posture", func(t *testing.T) {
		clearPrivacyEnv(t)

		cfg := OpenCode.Config()
		assert.True(t, cfg.OmitIO)
		assert.Equal(t, otlp.LevelLimited, cfg.Prompts)
		assert.Equal(t, otlp.LevelLimited, cfg.Tools)
		assert.Equal(t, otlp.LevelLimited, cfg.Skills)
		assert.Equal(t, otlp.LevelLimited, cfg.Agents)
	})

	t.Run("an explicit dimension overrides omit_io", func(t *testing.T) {
		clearPrivacyEnv(t)
		t.Setenv("DASH0_OMIT_IO", "true")
		t.Setenv("DASH0_TOOLS", "full")

		cfg := OpenCode.Config()
		assert.Equal(t, otlp.LevelFull, cfg.Tools)
		assert.Equal(t, otlp.LevelLimited, cfg.Prompts, "prompts was not set, so omit_io still governs it")
	})

	t.Run("omit_io: false resolves prompts and tools to full", func(t *testing.T) {
		clearPrivacyEnv(t)
		t.Setenv("DASH0_OMIT_IO", "false")

		cfg := OpenCode.Config()
		assert.Equal(t, otlp.LevelFull, cfg.Prompts)
		assert.Equal(t, otlp.LevelFull, cfg.Tools)
	})

	// omit_io never spoke for skills or sub-agent content, so mapping it onto
	// them would invent intent. They stay at the safe default until set.
	t.Run("omit_io does not reach skills or agents", func(t *testing.T) {
		clearPrivacyEnv(t)
		t.Setenv("DASH0_OMIT_IO", "false")

		cfg := OpenCode.Config()
		assert.Equal(t, otlp.LevelLimited, cfg.Skills)
		assert.Equal(t, otlp.LevelLimited, cfg.Agents)
	})

	t.Run("the dimensions are set independently", func(t *testing.T) {
		clearPrivacyEnv(t)
		t.Setenv("DASH0_PROMPTS", "disabled")
		t.Setenv("DASH0_TOOLS", "full")
		t.Setenv("DASH0_SKILLS", "disabled")
		t.Setenv("DASH0_AGENTS", "full")

		cfg := OpenCode.Config()
		assert.Equal(t, otlp.LevelDisabled, cfg.Prompts)
		assert.Equal(t, otlp.LevelFull, cfg.Tools)
		assert.Equal(t, otlp.LevelDisabled, cfg.Skills)
		assert.Equal(t, otlp.LevelFull, cfg.Agents)
	})

	// Every harness defaults to prompts: limited, so an ungated withheld-character
	// count would widen the spans the other four runtimes already export.
	t.Run("only opencode reports the withheld-character counts", func(t *testing.T) {
		clearPrivacyEnv(t)

		assert.True(t, OpenCode.Config().Dimensions)
		for _, h := range []Harness{Claude, Cursor, Codex, Copilot} {
			assert.False(t, h.Config().Dimensions, h.Name)
		}
	})

	// The wrapper exports the file's keys as DASH0_* for the user-scoped copy it
	// alone can find; a project-scoped file reaches the same values through
	// PluginOption. This pins the second path, which has no per-key wiring.
	t.Run("a configuration file sets all four dimensions", func(t *testing.T) {
		clearPrivacyEnv(t)
		chdirTo(t, writeConfig(t, t.TempDir(), OpenCode,
			"---\nprompts: disabled\ntools: full\nskills: limited\nagents: full\n---\n"))

		cfg := OpenCode.Config()
		assert.Equal(t, otlp.LevelDisabled, cfg.Prompts)
		assert.Equal(t, otlp.LevelFull, cfg.Tools)
		assert.Equal(t, otlp.LevelLimited, cfg.Skills)
		assert.Equal(t, otlp.LevelFull, cfg.Agents)
	})

	t.Run("a configuration file outranks the DASH0_ fallback", func(t *testing.T) {
		clearPrivacyEnv(t)
		t.Setenv("DASH0_TOOLS", "disabled")
		chdirTo(t, writeConfig(t, t.TempDir(), OpenCode, "---\ntools: full\n---\n"))

		assert.Equal(t, otlp.LevelFull, OpenCode.Config().Tools)
	})

	t.Run("a typo falls back to limited rather than to omit_io: false", func(t *testing.T) {
		clearPrivacyEnv(t)
		t.Setenv("DASH0_OMIT_IO", "false")
		t.Setenv("DASH0_TOOLS", "ful")

		cfg := OpenCode.Config()
		assert.Equal(t, otlp.LevelLimited, cfg.Tools)
	})
}

// dimensionEvents are one of each span-bearing event shape, so the comparison
// below covers a session start, a chat turn, a tool call, a skill invocation and
// a delegated sub-agent rather than only the attributes one of them happens to
// carry. Every event carries the two failure fields, since the failed spans the
// comparison builds from them are where the dimensions touch a span's status.
func dimensionEvents() map[string]map[string]any {
	return map[string]map[string]any{
		"session start": {
			"hook_event_name": "SessionStart",
			"session_id":      "sess-unaffected",
			"cwd":             "/tmp/project",
		},
		"chat turn": {
			"hook_event_name":            "Stop",
			"session_id":                 "sess-unaffected",
			"model":                      "claude-sonnet-4-20250514",
			"prompt":                     "summarize the outage for customer 4711",
			"last_assistant_message":     "The outage lasted 12 minutes.",
			"gen_ai.conversation.name":   "Customer 4711 outage",
			"gen_ai.usage.input_tokens":  int64(1200),
			"gen_ai.usage.output_tokens": int64(340),
			"error":                      "the model stopped early",
			"error_type":                 "max_tokens",
		},
		"tool call": {
			"hook_event_name": "PostToolUse",
			"session_id":      "sess-unaffected",
			"tool_name":       "Bash",
			"tool_use_id":     "tu-1",
			"tool_input":      `{"command":"git commit -m \"fix 4711\""}`,
			"tool_response":   "[main 82717dc] fix 4711",
			"error":           `git commit -m "fix 4711" exited with 1`,
			"error_type":      "non_zero_exit",
		},
		"skill invocation": {
			"hook_event_name": "PostToolUse",
			"session_id":      "sess-unaffected",
			"tool_name":       "Skill",
			"tool_use_id":     "tu-2",
			"skill_name":      "gather-evidence",
			"tool_input":      `{"skill":"gather-evidence","args":"customer 4711"}`,
			"tool_response":   "evidence gathered",
			"error":           "gather-evidence found no evidence for customer 4711",
			"error_type":      "skill_failed",
		},
		"sub-agent turn": {
			"hook_event_name":        "SubagentStop",
			"session_id":             "sess-unaffected",
			"agent_id":               "agent-1",
			"agent_type":             "Explore",
			"model":                  "claude-sonnet-4-20250514",
			"prompt":                 "find every caller of resolveLevel",
			"last_assistant_message": "three callers, all in internal/harness",
			"error":                  "the sub-agent hit its turn limit",
			"error_type":             "turn_limit",
		},
	}
}

// dimensionSpans builds a span per event shape in both its succeeded and its
// failed form, with attributes sorted, since they are assembled from a map and
// OTLP does not order them. The failed form matters on its own: error.type and
// the withheld failure message are the two attributes a span gains or loses from
// Config.Dimensions alone, and neither appears on a span that succeeded.
func dimensionSpans(cfg otlp.Config) map[string]otlp.Span {
	const traceID, spanID, parentSpanID = "aabbccddeeff00112233445566778899", "span1234span1234", "parentidparentid"
	start := time.Date(2025, 6, 15, 12, 0, 0, 0, time.UTC)
	end := time.Date(2025, 6, 15, 12, 0, 45, 0, time.UTC)

	spans := map[string]otlp.Span{}
	add := func(label string, span otlp.Span) {
		slices.SortFunc(span.Attributes, func(a, b otlp.Attribute) int {
			return strings.Compare(a.Key, b.Key)
		})
		spans[label] = span
	}

	for label, event := range dimensionEvents() {
		if event["hook_event_name"] == "SessionStart" {
			add(label, otlp.NewSessionSpan(traceID, spanID, start, event, cfg))
			continue
		}
		build := otlp.NewLLMSpan
		if _, isTool := event["tool_name"]; isTool {
			build = otlp.NewToolSpan
		}
		add(label, build(traceID, spanID, parentSpanID, start, end, event, false, cfg))
		add(label+" (failed)", build(traceID, spanID, parentSpanID, start, end, event, true, cfg))
	}
	return spans
}

// TestTheDimensionsDoNotReachTheOtherRuntimes covers the spec scenario "Another
// runtime is unaffected". The four keys are OpenCode's config surface, but they
// resolve on every Harness because otlp.Config is shared — so what keeps Claude,
// Cursor, Codex and Copilot byte-identical is Config.Dimensions, not the absence
// of a value. Setting all four to their most extreme levels must move nothing.
func TestTheDimensionsDoNotReachTheOtherRuntimes(t *testing.T) {
	for _, h := range []Harness{Claude, Cursor, Codex, Copilot} {
		t.Run(h.Name, func(t *testing.T) {
			clearPrivacyEnv(t)
			baseline := dimensionSpans(h.Config())

			t.Setenv("DASH0_PROMPTS", "disabled")
			t.Setenv("DASH0_TOOLS", "full")
			t.Setenv("DASH0_SKILLS", "disabled")
			t.Setenv("DASH0_AGENTS", "full")
			cfg := h.Config()

			for label, got := range dimensionSpans(cfg) {
				assert.Equal(t, baseline[label], got, "%s span moved", label)
			}

			// Suppression is decided in the pipeline rather than on the span, so
			// an unchanged span would not catch a dropped one.
			assert.False(t, cfg.AgentSpanSuppressed())
			assert.False(t, cfg.AgentToolSpanSuppressed())
			for label, event := range dimensionEvents() {
				assert.False(t, cfg.ToolSpanSuppressed(event), "%s suppressed", label)
			}
		})
	}
}
