// SPDX-FileCopyrightText: Copyright 2026 Dash0 Inc.
// SPDX-License-Identifier: Apache-2.0

package otlpcapture

import (
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/dash0hq/dash0-agent-plugin/internal/otlp"
)

// The span shapes these helpers select on. They are the pipeline's output
// contract, verified by internal/source/codex/testdata/golden_spans.json (a
// replayed real session) and pinned by TestTurnSelectorsMatchRealPipelineOutput.
const (
	opChat        = "chat"
	opExecuteTool = "execute_tool"

	attrOperation = "gen_ai.operation.name"
	attrToolName  = "gen_ai.tool.name"
	attrSkillName = "dash0.gen_ai.tool.skill.name"
)

// StringAttr reads a string attribute off a span.
func StringAttr(s otlp.Span, key string) (string, bool) {
	for _, a := range s.Attributes {
		if a.Key == key && a.Value.StringValue != nil {
			return *a.Value.StringValue, true
		}
	}
	return "", false
}

// spansWithOperation returns the spans whose gen_ai.operation.name matches.
func spansWithOperation(spans []otlp.Span, op string) []otlp.Span {
	var out []otlp.Span
	for _, s := range spans {
		if v, ok := StringAttr(s, attrOperation); ok && v == op {
			out = append(out, s)
		}
	}
	return out
}

// ChatSpans returns one span per completed turn. The plugin emits exactly one
// chat span per stop event, so the count is the turn count.
func ChatSpans(spans []otlp.Span) []otlp.Span {
	return spansWithOperation(spans, opChat)
}

// ToolSpans returns every tool-call span.
func ToolSpans(spans []otlp.Span) []otlp.Span {
	return spansWithOperation(spans, opExecuteTool)
}

// ToolNames returns the tool name of every tool span, so a failure message can
// say what the agent actually ran.
func ToolNames(spans []otlp.Span) []string {
	var out []string
	for _, s := range ToolSpans(spans) {
		if v, ok := StringAttr(s, attrToolName); ok {
			out = append(out, v)
		}
	}
	return out
}

// SkillNames returns the skill named by every skill invocation in spans.
//
// A skill arrives as an ordinary tool call named "Skill" (Claude Code) or
// "skill" (Copilot). pipeline.ExtractSkillName pulls the skill's own name out of
// the arguments into dash0.gen_ai.tool.skill.name. Selecting on that attribute
// covers both spellings and proves the extraction ran.
func SkillNames(spans []otlp.Span) []string {
	var out []string
	for _, s := range ToolSpans(spans) {
		if v, ok := StringAttr(s, attrSkillName); ok && v != "" {
			out = append(out, v)
		}
	}
	return out
}

// DescribeTurns summarizes a captured session for a failure message: how many
// turns closed, which tools ran, which skills were invoked.
func DescribeTurns(spans []otlp.Span) string {
	return fmt.Sprintf("%d chat span(s), tools=%v, skills=%v",
		len(ChatSpans(spans)), ToolNames(spans), SkillNames(spans))
}

// ToolNamesContain reports whether any tool span's name matches want, compared
// case-insensitively because the runtimes disagree on capitalization (Claude
// emits "Bash", Copilot "bash").
func ToolNamesContain(spans []otlp.Span, want string) bool {
	for _, got := range ToolNames(spans) {
		if strings.EqualFold(got, want) {
			return true
		}
	}
	return false
}

// AssertRepoIdentity checks that every span attributes the turn to the throwaway
// repo GitInit created, and not to whoever runs the test.
//
// The plugin resolves identity from the repository first and falls back to the
// OS account, so a work directory that is not a git repo switches code paths and
// stamps a real name on every span. The assertion keeps a real person out of the
// capture and keeps the canary on the path a user in a repository takes.
func AssertRepoIdentity(t *testing.T, spans []otlp.Span) {
	t.Helper()
	require.NotEmpty(t, spans, "no spans to check identity on")

	for _, s := range spans {
		source, ok := StringAttr(s, "dash0.gen_ai.user.identity.source")
		if !assert.True(t, ok, "%s carries no dash0.gen_ai.user.identity.source", s.Name) {
			continue
		}
		assert.Equal(t, "git", source,
			"%s resolved its identity from %q, not the repo; the work directory is not a git repo, "+
				"so this span carries the identity of whoever ran the test", s.Name, source)

		if name, ok := StringAttr(s, "user.name"); ok {
			assert.Equal(t, "Dash0 E2E", name,
				"%s carries user.name=%q; the throwaway repo sets \"Dash0 E2E\", so a different "+
					"value is a real account leaking into the capture", s.Name, name)
		}
	}
}
