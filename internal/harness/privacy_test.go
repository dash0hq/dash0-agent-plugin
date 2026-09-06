// SPDX-FileCopyrightText: Copyright 2026 Dash0 Inc.
// SPDX-License-Identifier: Apache-2.0

package harness

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/dash0hq/dash0-agent-plugin/internal/otlp"
)

// clearPrivacyEnv unsets every variable the resolution reads, so a subtest
// starts from "nothing configured" whatever ran before it.
func clearPrivacyEnv(t *testing.T) {
	t.Helper()
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

	t.Run("a typo falls back to limited rather than to omit_io: false", func(t *testing.T) {
		clearPrivacyEnv(t)
		t.Setenv("DASH0_OMIT_IO", "false")
		t.Setenv("DASH0_TOOLS", "ful")

		cfg := OpenCode.Config()
		assert.Equal(t, otlp.LevelLimited, cfg.Tools)
	})
}
