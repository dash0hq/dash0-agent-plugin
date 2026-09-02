// SPDX-FileCopyrightText: Copyright 2026 Dash0 Inc.
// SPDX-License-Identifier: Apache-2.0

package otlpcapture

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/dash0hq/dash0-agent-plugin/internal/otlp"
	"github.com/dash0hq/dash0-agent-plugin/test/helpers/pluginrepo"
)

// goldenSpan is the canonicalized projection written by
// internal/source/codex/golden_test.go. Only the fields these assertions read are
// declared.
type goldenSpan struct {
	Name       string            `json:"name"`
	Attributes map[string]string `json:"attributes"`
}

// TestTurnSelectorsMatchRealPipelineOutput pins the attribute names the e2e turn
// assertions select on against a golden snapshot of a REPLAYED REAL session, so a
// rename in the exporter fails here rather than as a puzzling e2e failure on
// someone else's machine.
//
// It reads the codex golden, a genuine multi-turn session with tool calls. The
// skill attribute has no codex counterpart, so TestSkillNameSurvivesOmitIO in
// cmd/claude-on-event pins that one.
func TestTurnSelectorsMatchRealPipelineOutput(t *testing.T) {
	path := filepath.Join(pluginrepo.Root(t), "internal", "source", "codex", "testdata", "golden_spans.json")
	raw, err := os.ReadFile(path)
	require.NoError(t, err, "the golden fixture these selectors are pinned against is missing")

	var golden []goldenSpan
	require.NoError(t, json.Unmarshal(raw, &golden))
	require.NotEmpty(t, golden)

	var chats, tools, named int
	for _, s := range golden {
		switch s.Attributes[attrOperation] {
		case opChat:
			chats++
		case opExecuteTool:
			tools++
			if s.Attributes[attrToolName] != "" {
				named++
			}
		}
	}

	// Two or more chat spans is what "multi-turn" means for these assertions, and
	// the fixture proves a real session produces one per turn.
	assert.GreaterOrEqual(t, chats, 2,
		"%q must select one span per turn; the golden session should hold several", opChat)
	assert.NotZero(t, tools, "%q must select the tool calls in the golden session", opExecuteTool)
	assert.Equal(t, tools, named, "every tool span must carry %s", attrToolName)
}

// TestTurnSelectors covers the filtering itself, including the two runtime
// spellings of a skill tool that SkillNames has to tolerate.
func TestTurnSelectors(t *testing.T) {
	spans := []otlp.Span{
		span("chat haiku", attr(attrOperation, opChat)),
		span("chat haiku", attr(attrOperation, opChat)),
		span("execute_tool Bash", attr(attrOperation, opExecuteTool), attr(attrToolName, "Bash")),
		span("execute_tool Skill",
			attr(attrOperation, opExecuteTool), attr(attrToolName, "Skill"),
			attr(attrSkillName, "dash0-e2e-probe")),
		span("execute_tool skill",
			attr(attrOperation, opExecuteTool), attr(attrToolName, "skill"),
			attr(attrSkillName, "lowercase-runtime")),
		// A session span must not be mistaken for a turn.
		span("session", attr(attrOperation, "invoke_agent")),
	}

	assert.Len(t, ChatSpans(spans), 2, "one chat span per turn, and nothing else")
	assert.Len(t, ToolSpans(spans), 3)
	assert.Equal(t, []string{"Bash", "Skill", "skill"}, ToolNames(spans))
	assert.Equal(t, []string{"dash0-e2e-probe", "lowercase-runtime"}, SkillNames(spans),
		"both runtime spellings of the skill tool must be picked up")

	assert.True(t, ToolNamesContain(spans, "bash"), "tool names compare case-insensitively")
	assert.False(t, ToolNamesContain(spans, "Write"))

	assert.Contains(t, DescribeTurns(spans), "2 chat span(s)")
}

func span(name string, attrs ...otlp.Attribute) otlp.Span {
	return otlp.Span{Name: name, Attributes: attrs}
}

func attr(key, value string) otlp.Attribute {
	return otlp.Attribute{Key: key, Value: otlp.StringVal(value)}
}
