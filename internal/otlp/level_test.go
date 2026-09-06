// SPDX-FileCopyrightText: Copyright 2026 Dash0 Inc.
// SPDX-License-Identifier: Apache-2.0

package otlp

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestParseLevel(t *testing.T) {
	cases := []struct {
		in    string
		want  Level
		valid bool
	}{
		{"disabled", LevelDisabled, true},
		{"limited", LevelLimited, true},
		{"full", LevelFull, true},
		{"FULL", LevelFull, true},
		{"  limited  ", LevelLimited, true},
		{"", LevelLimited, false},
		{"ful", LevelLimited, false},
		{"disable", LevelLimited, false},
		{"true", LevelLimited, false},
	}
	for _, c := range cases {
		t.Run(c.in, func(t *testing.T) {
			got, ok := ParseLevel(c.in)
			assert.Equal(t, c.want, got)
			assert.Equal(t, c.valid, ok)
			if !c.valid {
				assert.NotEqual(t, LevelFull, got, "an unrecognized value must never widen to full")
			}
		})
	}
}

// A Config a test builds without naming a dimension must behave like the
// shipped default rather than exporting nothing or everything.
func TestLevelZeroValueIsLimited(t *testing.T) {
	var cfg Config
	assert.Equal(t, LevelLimited, cfg.Prompts)
	assert.Equal(t, LevelLimited, cfg.Tools)
	assert.Equal(t, LevelLimited, cfg.Skills)
	assert.Equal(t, LevelLimited, cfg.Agents)
}

// A dimension levelFor does not know about — one added to contentKeys before it
// is wired here — must export nothing rather than inherit another dimension's
// level.
func TestLevelForUnknownDimensionFailsClosed(t *testing.T) {
	cfg := Config{Prompts: LevelFull, Tools: LevelFull}
	assert.Equal(t, LevelDisabled, cfg.levelFor(dimUnset))

	for key, dim := range contentKeys {
		assert.NotEqual(t, dimUnset, dim, "content key %q is governed by no dimension", key)
	}
}

func TestLevelString(t *testing.T) {
	assert.Equal(t, "disabled", LevelDisabled.String())
	assert.Equal(t, "limited", LevelLimited.String())
	assert.Equal(t, "full", LevelFull.String())
}
