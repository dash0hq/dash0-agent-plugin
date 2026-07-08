// SPDX-FileCopyrightText: Copyright 2026 Dash0 Inc.
// SPDX-License-Identifier: Apache-2.0

package pipeline

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestToolResponseText(t *testing.T) {
	t.Run("string input", func(t *testing.T) {
		assert.Equal(t, "hello", ToolResponseText("hello"))
	})
	t.Run("nil input", func(t *testing.T) {
		assert.Equal(t, "", ToolResponseText(nil))
	})
	t.Run("Bash response dict", func(t *testing.T) {
		resp := map[string]any{
			"stdout":  "output line",
			"stderr":  "warning",
			"isImage": false,
		}
		text := ToolResponseText(resp)
		assert.Contains(t, text, "output line")
		assert.Contains(t, text, "warning")
	})
	t.Run("dict without stdout falls back to JSON", func(t *testing.T) {
		resp := map[string]any{"filePath": "/tmp/file.go"}
		text := ToolResponseText(resp)
		assert.Contains(t, text, "filePath")
	})
}

func TestExtractPRURL(t *testing.T) {
	tests := []struct {
		name     string
		input    any
		expected string
	}{
		{"GitHub PR URL", "https://github.com/dash0hq/dash0-agent-plugin/pull/94", "https://github.com/dash0hq/dash0-agent-plugin/pull/94"},
		{"GitHub PR in multiline output", "Creating pull request...\nhttps://github.com/org/repo/pull/123\nDone.", "https://github.com/org/repo/pull/123"},
		{"GitLab MR URL", "https://gitlab.com/org/repo/-/merge_requests/42", "https://gitlab.com/org/repo/-/merge_requests/42"},
		{"Bitbucket PR URL", "https://bitbucket.org/team/repo/pull-requests/7", "https://bitbucket.org/team/repo/pull-requests/7"},
		{"self-hosted GitHub", "https://github.company.com/team/repo/pull/99", "https://github.company.com/team/repo/pull/99"},
		{"ignores pull/new from git push", "https://github.com/org/repo/pull/new/feat-branch", ""},
		{"no PR URL", "file1.go\nfile2.go\nok", ""},
		{"nil input", nil, ""},
		{"Bash response dict with PR in stdout", map[string]any{
			"stdout": "Warning: 2 uncommitted changes\nhttps://github.com/dash0hq/dash0-agent-plugin/pull/94",
			"stderr": "",
		}, "https://github.com/dash0hq/dash0-agent-plugin/pull/94"},
		{"empty string", "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.expected, ExtractPRURL(tt.input))
		})
	}
}

func TestExtractIssueURL(t *testing.T) {
	tests := []struct {
		name     string
		input    any
		expected string
	}{
		{"GitHub issue", "https://github.com/dash0hq/dash0-agent-plugin/issues/91", "https://github.com/dash0hq/dash0-agent-plugin/issues/91"},
		{"GitLab issue", "https://gitlab.com/org/repo/issues/42", "https://gitlab.com/org/repo/issues/42"},
		{"issue in Bash stdout", map[string]any{
			"stdout": "Created issue https://github.com/org/repo/issues/5\n",
		}, "https://github.com/org/repo/issues/5"},
		{"no issue URL", "file1.go\nfile2.go", ""},
		{"nil", nil, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.expected, ExtractIssueURL(tt.input))
		})
	}
}

func TestExtractCommitSHA(t *testing.T) {
	tests := []struct {
		name     string
		input    any
		expected string
	}{
		{"git commit output", "[feat/my-branch 82717dc] feat: add new feature\n 4 files changed", "82717dc"},
		{"full SHA", "[main abcdef1234567890abcdef1234567890abcdef12] fix: bug", "abcdef1234567890abcdef1234567890abcdef12"},
		{"Bash response dict", map[string]any{
			"stdout": "[feat/extract-pr-urls a1b2c3d] feat: extract PR URLs\n 3 files changed",
		}, "a1b2c3d"},
		{"no commit", "file1.go\nfile2.go", ""},
		{"nil", nil, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.expected, ExtractCommitSHA(tt.input))
		})
	}
}

func TestExtractLinesCounts(t *testing.T) {
	tests := []struct {
		name        string
		input       any
		wantAdded   int
		wantRemoved int
	}{
		{
			"Edit with additions and removals",
			map[string]any{
				"structuredPatch": []any{
					map[string]any{
						"filePath": "main.go",
						"lines": []any{
							" unchanged line",
							"-old line 1",
							"-old line 2",
							"+new line 1",
							"+new line 2",
							"+new line 3",
							" another unchanged",
						},
					},
				},
			},
			3, 2,
		},
		{
			"Write (new file, no patches)",
			map[string]any{
				"structuredPatch": []any{},
			},
			0, 0,
		},
		{
			"nil input",
			nil,
			0, 0,
		},
		{
			"string input (not a map)",
			"some text response",
			0, 0,
		},
		{
			"map without structuredPatch",
			map[string]any{"filePath": "/tmp/file.go"},
			0, 0,
		},
		{
			"multiple patches across files",
			map[string]any{
				"structuredPatch": []any{
					map[string]any{
						"filePath": "a.go",
						"lines":    []any{"+added1", "+added2", "-removed1"},
					},
					map[string]any{
						"filePath": "b.go",
						"lines":    []any{"+added3", "-removed2", "-removed3"},
					},
				},
			},
			3, 3,
		},
		{
			"patch with missing lines field",
			map[string]any{
				"structuredPatch": []any{
					map[string]any{"filePath": "c.go"},
				},
			},
			0, 0,
		},
		{
			"empty line strings are skipped",
			map[string]any{
				"structuredPatch": []any{
					map[string]any{
						"lines": []any{"", "+added", ""},
					},
				},
			},
			1, 0,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			added, removed := ExtractLinesCounts(tt.input)
			assert.Equal(t, tt.wantAdded, added, "lines added")
			assert.Equal(t, tt.wantRemoved, removed, "lines removed")
		})
	}
}

func TestExtractBashCommandFamily(t *testing.T) {
	tests := []struct {
		name  string
		input any
		want  string
	}{
		{"simple command", "git status", "git"},
		{"command with args", "npm install express", "npm"},
		{"env var prefix", "FOO=bar git push", "git"},
		{"multiple env vars", "A=1 B=2 docker build .", "docker"},
		{"chained commands", "cd /tmp && make build", "cd"},
		{"absolute path", "/usr/bin/git log", "git"},
		{"empty input", "", ""},
		{"only env vars", "FOO=bar", ""},
		{"command with flags", "ls -la /tmp", "ls"},
		{"map with command field", map[string]any{"command": "git log --oneline -3", "description": "Show log"}, "git"},
		{"map with env var prefix", map[string]any{"command": "DASH0_DEBUG=true claude --debug"}, "claude"},
		{"map without command field", map[string]any{"description": "no command"}, ""},
		{"nil input", nil, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, ExtractBashCommandFamily(tt.input))
		})
	}
}

func TestExtractSkillName(t *testing.T) {
	tests := []struct {
		name  string
		input any
		want  string
	}{
		{"valid skill JSON string", `{"skill":"translation-updater","args":"Add translations"}`, "translation-updater"},
		{"skill only JSON string", `{"skill":"reviewer"}`, "reviewer"},
		{"empty input", "", ""},
		{"invalid JSON", "not json", ""},
		{"missing skill field string", `{"args":"something"}`, ""},
		{"null skill string", `{"skill":null}`, ""},
		{"map with skill field", map[string]any{"skill": "keybindings-help"}, "keybindings-help"},
		{"map without skill field", map[string]any{"args": "something"}, ""},
		{"nil input", nil, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, ExtractSkillName(tt.input))
		})
	}
}

func TestExtractMCPServer(t *testing.T) {
	tests := []struct {
		name     string
		toolName string
		want     string
	}{
		{"linear server", "mcp__linear-server__list_issues", "linear-server"},
		{"github", "mcp__github__create_pull_request", "github"},
		{"slack", "mcp__slack__send_message", "slack"},
		{"claude_ai namespace", "mcp__claude_ai_Linear__list_projects", "claude_ai_Linear"},
		{"dash0", "mcp__dash0__query_metrics", "dash0"},
		{"not MCP", "Bash", ""},
		{"partial MCP prefix", "mcp__", ""},
		{"no tool part", "mcp__server", "server"},
		{"empty", "", ""},
		{"UUID server", "mcp__1a66ca22-a5b4-4d91-b577-b64d7f7bc86c__read_thread", ""},
		{"uppercase UUID", "mcp__1A66CA22-A5B4-4D91-B577-B64D7F7BC86C__read_thread", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, ExtractMCPServer(tt.toolName))
		})
	}
}

func TestNormalizeMCPToolName(t *testing.T) {
	tests := []struct {
		name     string
		toolName string
		want     string
	}{
		{"strips server", "mcp__slack__send_message", "send_message"},
		{"strips UUID server", "mcp__1a66ca22-a5b4-4d91-b577-b64d7f7bc86c__read_thread", "read_thread"},
		{"strips namespace", "mcp__claude_ai_Linear__list_projects", "list_projects"},
		{"non-MCP unchanged", "Bash", "Bash"},
		{"partial prefix unchanged", "mcp__server", "mcp__server"},
		{"empty tool part unchanged", "mcp__server__", "mcp__server__"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, NormalizeMCPToolName(tt.toolName))
		})
	}
}

func TestExtractAgentIDFromResponse(t *testing.T) {
	tests := []struct {
		name  string
		input any
		want  string
	}{
		{"JSON string with agentId", `{"agentId":"a13ff1c4e70c41cd1","agentType":"general-purpose","content":[]}`, "a13ff1c4e70c41cd1"},
		{"map with agentId", map[string]any{"agentId": "abc123", "agentType": "general-purpose"}, "abc123"},
		{"missing agentId in JSON", `{"no_agent":"here"}`, ""},
		{"invalid JSON", "not json", ""},
		{"nil input", nil, ""},
		{"non-string non-map", 42, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, ExtractAgentIDFromResponse(tt.input))
		})
	}
}
