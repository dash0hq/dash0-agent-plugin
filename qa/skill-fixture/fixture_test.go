// SPDX-FileCopyrightText: Copyright 2026 Dash0 Inc.
// SPDX-License-Identifier: Apache-2.0

// Package skillfixture holds the Codex skill a QA run installs into its
// throwaway home. There is no code here: a skill is a SKILL.md that Codex reads.
// The test exists for the same reason qa/mcp-fixture's does — a spec asserts
// exact values that come out of this fixture, and nothing else stops the two
// drifting apart.
package skillfixture

import (
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The marker the skill instructs the model to print. A spec asserts this exact
// string appears in the session's output, which is what proves the skill's
// instructions reached the model rather than only its name being recorded.
const marker = "QA-SKILL-MARKER"

func TestFixtureIsWhatTheSpecExpects(t *testing.T) {
	body, err := os.ReadFile("qa-echo/SKILL.md")
	require.NoError(t, err, "the fixture must live at the path the driver installs from")
	text := string(body)

	// The directory name is what Codex calls the skill and what a $mention has
	// to match, so a rename that touches only one of the two breaks the spec in
	// a way no other test would catch.
	assert.Contains(t, text, "name: qa-echo",
		"the frontmatter name must match the directory, which is what $qa-echo addresses")

	// The description is the only thing the model sees before it decides to load
	// the skill, so an empty or vague one makes the model route unreachable.
	assert.Regexp(t, `(?m)^description: \S.{20,}`, text,
		"a skill with a thin description cannot be chosen by the model")

	assert.Contains(t, text, marker, "the fixture must instruct the model to print the marker")
	assert.Equal(t, 1, strings.Count(text, "```sh"),
		"exactly one command, so a run cannot pass by doing something else")
}
