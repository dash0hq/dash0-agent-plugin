// SPDX-FileCopyrightText: Copyright 2026 Dash0 Inc.
// SPDX-License-Identifier: Apache-2.0

package codex

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/dash0hq/dash0-agent-plugin/internal/otlp"
)

const skillFileContent = "---\nname: otel-collector\ndescription: Configure a collector.\n---\nPrivate skill instructions.\n"

func TestNormalizeSkillFileReads(t *testing.T) {
	for _, tc := range []struct {
		name, tool string
		input      map[string]any
		response   any
	}{
		{"desktop Bash", "Bash", map[string]any{"command": "cat /home/test/.agents/skills/otel-collector/SKILL.md"}, skillFileContent},
		{"quoted path", "Bash", map[string]any{"command": `cat '/home/test/My Skills/collector/SKILL.md'`}, skillFileContent},
		{"double quoted path", "Bash", map[string]any{"command": `cat "/home/test/My Skills/collector/SKILL.md"`}, skillFileContent},
		{"cat separator", "Bash", map[string]any{"command": "cat -- .agents/skills/collector/SKILL.md"}, skillFileContent},
		{"absolute utility", "Bash", map[string]any{"command": "/bin/cat ./SKILL.md"}, skillFileContent},
		{"home-relative path", "Bash", map[string]any{"command": "cat ~/.agents/skills/collector/SKILL.md"}, skillFileContent},
		{"sed prefix", "Bash", map[string]any{"command": `sed -n '1,160p' /tmp/collector/SKILL.md`}, skillFileContent},
		{"head prefix", "Bash", map[string]any{"command": `head -n 80 /tmp/collector/SKILL.md`}, skillFileContent},
		{"exec command wrapper", "exec_command", map[string]any{"cmd": "cat /tmp/collector/SKILL.md"}, map[string]any{"exit_code": float64(0), "output": skillFileContent}},
		{"integer exit and success", "exec_command", map[string]any{"cmd": "cat /tmp/collector/SKILL.md"}, map[string]any{"exit_code": 0, "success": true, "output": skillFileContent}},
		{"file reader", "read_file", map[string]any{"path": "/tmp/collector/SKILL.md"}, map[string]any{"content": skillFileContent}},
		{"Read wrapper", "Read", map[string]any{"file_path": "/tmp/collector/SKILL.md"}, map[string]any{"file": map[string]any{"content": skillFileContent}}},
		{"Read from first line", "Read", map[string]any{"file_path": "/tmp/collector/SKILL.md", "offset": float64(1)}, skillFileContent},
		{"Windows path and CRLF", "Read", map[string]any{"file_path": `C:\Users\test\skills\collector\SKILL.md`}, strings.ReplaceAll(skillFileContent, "\n", "\r\n")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			event := Normalize(map[string]any{
				"hook_event_name": "PostToolUse", "tool_name": tc.tool,
				"tool_input": tc.input, "tool_response": tc.response,
				"tool_use_id": "read-1", "duration_ms": float64(42), "agent_id": "child-1",
			}, t.TempDir(), time.Unix(123, 0))
			require.Equal(t, "otel-collector", event["skill_name"])
			assert.Equal(t, "model", event["skill_source"])
			assert.Equal(t, tc.tool, event["tool_name"], "retain the actual tool and its measured timing")
			assert.Equal(t, float64(42), event["duration_ms"])
			assert.Equal(t, "child-1", event["agent_id"])
			span := otlp.NewToolSpan("aaaabbbbccccddddaaaabbbbccccdddd", "1111222233334444", "5555666677778888",
				time.Unix(122, 0), time.Unix(123, 0), event, false, otlp.Config{OmitIO: true, OmitUserInfo: true})
			assert.Contains(t, span.Attributes, otlp.Attribute{Key: "dash0.gen_ai.tool.skill.name", Value: otlp.StringVal("otel-collector")})
			assert.Contains(t, span.Attributes, otlp.Attribute{Key: "dash0.gen_ai.tool.skill.source", Value: otlp.StringVal("model")})
			wire, err := json.Marshal(span)
			require.NoError(t, err)
			assert.NotContains(t, string(wire), "Private skill instructions")
			assert.NotContains(t, string(wire), "SKILL.md", "file paths are not new telemetry attributes")
			assert.NotContains(t, string(wire), "gen_ai.usage.", "file reads must not copy the turn's usage")
		})
	}
}

func TestNormalizeSkillFileRejectsAmbiguousCommands(t *testing.T) {
	for _, command := range []string{
		"echo /tmp/collector/SKILL.md", "ls /tmp/collector/SKILL.md", "rg --files -g SKILL.md",
		"cat /tmp/collector/README.md", "cat /tmp/SKILL.md.bak", "cat /tmp/NOTSKILL.md",
		"cat /tmp/collector/SKILL.md /tmp/other/SKILL.md",
		"cat /tmp/other.md /tmp/collector/SKILL.md",
		"cat /tmp/collector/SKILL.md; echo done", "cat /tmp/collector/SKILL.md && echo done",
		"cat /tmp/collector/SKILL.md\ncat /tmp/other.md", "cat /tmp/collector/SKILL.md | head",
		"cat /tmp/collector/SKILL.md > /tmp/copied", "cat < /tmp/collector/SKILL.md",
		"cat \"$(echo /tmp/collector)/SKILL.md\"", "cat $SKILLS/collector/SKILL.md",
		"cat `pwd`/SKILL.md", "python3 -c 'print(open(\"/tmp/collector/SKILL.md\").read())'",
		"cat ~/.agents/skills/*/SKILL.md", "cat /tmp/{alpha,beta}/SKILL.md", "cat /tmp/[ab]*/SKILL.md",
		"sed -n '1,200p' /tmp/*/SKILL.md", "head -n 50 /tmp/?/SKILL.md",
		`cat C:\a\SKILL.md,C:\b\SKILL.md`, `cat "C:\a\SKILL.md,C:\b\SKILL.md"`,
		"cat /tmp/+(a)/SKILL.md", "cat /tmp/a#/SKILL.md", "cat /tmp/^a/SKILL.md",
		"sed -n '20,80p' /tmp/collector/SKILL.md", "sed -n '1,80p;w /tmp/copy' /tmp/collector/SKILL.md",
	} {
		t.Run(command, func(t *testing.T) {
			event := Normalize(map[string]any{
				"hook_event_name": "PostToolUse", "tool_name": "Bash",
				"tool_input": map[string]any{"command": command}, "tool_response": skillFileContent,
			}, t.TempDir(), time.Unix(123, 0))
			assert.NotContains(t, event, "skill_name")
			assert.NotContains(t, event, "skill_source")
		})
	}
}

func TestNormalizeSkillFileRejectsPartialFileReads(t *testing.T) {
	for _, key := range []string{"offset", "start_line"} {
		for _, start := range []any{float64(40), float64(-1), "1", nil, []any{1}} {
			event := Normalize(map[string]any{
				"hook_event_name": "PostToolUse", "tool_name": "Read",
				"tool_input":    map[string]any{"file_path": "/tmp/collector/SKILL.md", key: start},
				"tool_response": skillFileContent,
			}, t.TempDir(), time.Unix(123, 0))
			assert.NotContains(t, event, "skill_name", "frontmatter-looking examples later in a skill are not metadata")
		}
	}
}

func TestNormalizeSkillFileRequiresSuccessfulRead(t *testing.T) {
	for _, tc := range []struct {
		name, hook, tool string
		response         any
	}{
		{"before execution", "PreToolUse", "Bash", skillFileContent},
		{"failed hook", "PostToolUseFailure", "Bash", skillFileContent},
		{"turn end", "Stop", "Bash", skillFileContent},
		{"other tool", "PostToolUse", "Grep", skillFileContent},
		{"nonzero exit", "PostToolUse", "Bash", map[string]any{"exit_code": float64(1), "output": skillFileContent}},
		{"invalid exit", "PostToolUse", "Bash", map[string]any{"exit_code": "0", "output": skillFileContent}},
		{"unfinished process", "PostToolUse", "Bash", map[string]any{"session_id": 123, "output": skillFileContent}},
		{"error flag", "PostToolUse", "read_file", map[string]any{"isError": true, "content": skillFileContent}},
		{"failure flag", "PostToolUse", "read_file", map[string]any{"success": false, "content": skillFileContent}},
		{"missing output", "PostToolUse", "Bash", nil},
		{"file missing", "PostToolUse", "Bash", "cat: /tmp/collector/SKILL.md: No such file or directory"},
		{"directory listing", "PostToolUse", "Bash", "/tmp/collector/SKILL.md\n"},
		{"catalogue", "PostToolUse", "Bash", "<skills_instructions>\n- collector: Read this skill\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			event := Normalize(map[string]any{
				"hook_event_name": tc.hook, "tool_name": tc.tool,
				"tool_input":    map[string]any{"command": "cat /tmp/collector/SKILL.md", "path": "/tmp/collector/SKILL.md"},
				"tool_response": tc.response,
			}, t.TempDir(), time.Unix(123, 0))
			assert.NotContains(t, event, "skill_name")
		})
	}
}

func TestSkillFileFrontmatter(t *testing.T) {
	for _, tc := range []struct{ name, text, want string }{
		{"quoted name", "---\nname: 'otel-collector'\ndescription: >\n  Configure a collector.\n---\nBody", "otel-collector"},
		{"plugin qualified", "---\nname: 'dash0:otel-collector'\ndescription: help\n---\n", "dash0:otel-collector"},
		{"no closing delimiter", "---\nname: otel-collector\ndescription: help\n", ""},
		{"no description", "---\nname: otel-collector\n---\n", ""},
		{"empty description", "---\nname: otel-collector\ndescription: ' '\n---\n", ""},
		{"no name", "---\ndescription: help\n---\n", ""},
		{"numeric name", "---\nname: 123\ndescription: help\n---\n", ""},
		{"structured name", "---\nname: [otel-collector]\ndescription: help\n---\n", ""},
		{"duplicate name", "---\nname: a\nname: b\ndescription: help\n---\n", ""},
		{"invalid YAML", "---\nname: [\ndescription: help\n---\n", ""},
		{"code example", "Example:\n```yaml\n" + skillFileContent + "```", ""},
		{"unbounded metadata", "---\nname: otel-collector\ndescription: " + strings.Repeat("x", 17*1024) + "\n---\n", ""},
	} {
		t.Run(tc.name, func(t *testing.T) { assert.Equal(t, tc.want, skillFrontmatterName(tc.text)) })
	}
}

func TestNormalizeSkillFilePreservesExistingAttribution(t *testing.T) {
	event := Normalize(map[string]any{
		"hook_event_name": "PostToolUse", "tool_name": "Bash",
		"tool_input": map[string]any{"command": "cat /tmp/collector/SKILL.md"}, "tool_response": skillFileContent,
		"skill_name": "native-skill", "skill_source": "command",
	}, t.TempDir(), time.Unix(123, 0))
	assert.Equal(t, "native-skill", event["skill_name"])
	assert.Equal(t, "command", event["skill_source"])
}
