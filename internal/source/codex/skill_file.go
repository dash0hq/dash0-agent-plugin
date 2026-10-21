// SPDX-FileCopyrightText: Copyright 2026 Dash0 Inc.
// SPDX-License-Identifier: Apache-2.0

package codex

import (
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"
)

// Codex can load a skill through an ordinary file read instead of injecting a
// <skill> message. Attribute the existing successful tool span, not another
// chat span: its duration is measured and its tokens still belong to the turn.
// Never read the path ourselves, execute shell text, or infer use from a name
// in the available-skills catalogue. Both a direct read and the returned skill
// frontmatter are required. More complex commands deliberately stay unknown.
func injectSkillFile(event map[string]any) {
	if event["hook_event_name"] != "PostToolUse" {
		return
	}
	if _, exists := event["skill_name"]; exists {
		return
	}
	tool, _ := event["tool_name"].(string)
	input, _ := event["tool_input"].(map[string]any)
	var path string
	switch strings.ToLower(tool) {
	case "bash", "exec_command":
		command, _ := input["command"].(string)
		if command == "" {
			command, _ = input["cmd"].(string)
		}
		path = skillReadPath(command)
	case "read", "read_file":
		for _, key := range []string{"offset", "start_line"} {
			if start, exists := input[key]; exists && start != float64(0) && start != float64(1) && start != int(0) && start != int(1) {
				return
			}
		}
		path, _ = input["file_path"].(string)
		if path == "" {
			path, _ = input["path"].(string)
		}
	default:
		return
	}
	// Accept both path separators regardless of the platform running the hook.
	if path != "SKILL.md" && !strings.HasSuffix(strings.ReplaceAll(path, `\`, "/"), "/SKILL.md") {
		return
	}
	if name := skillFrontmatterName(skillReadText(event["tool_response"])); name != "" {
		event["skill_name"] = name
		event["skill_source"] = skillSourceModel
	}
}

// These are deliberately not a shell parser. Only a single, literal, one-file
// cat/head/sed read is recognized. Quoted paths may contain spaces. Expansion,
// redirection, pipelines, command lists and interpreter scripts are excluded.
var skillReadCommand = regexp.MustCompile(`^\s*(?:(?:/(?:usr/)?bin/)?cat\s+(?:--\s+)?|(?:/(?:usr/)?bin/)?head\s+(?:-n\s+[1-9][0-9]*\s+)?(?:--\s+)?|(?:/(?:usr/)?bin/)?sed\s+-n\s+(?:'1,[1-9][0-9]*p'|"1,[1-9][0-9]*p"|1,[1-9][0-9]*p)\s+(?:--\s+)?)("[^"\r\n]+"|'[^'\r\n]+'|[^\s'";|&<>]+)\s*$`)

func skillReadPath(command string) string {
	// Even inside quotes, reject interpolation rather than trying to reproduce
	// the user's shell. A false negative is preferable to attributing a write,
	// a printed example, or a failed read as skill use.
	if strings.ContainsAny(command, "$`*?[{}\r\n") {
		return ""
	}
	match := skillReadCommand.FindStringSubmatch(command)
	if match == nil {
		return ""
	}
	path := match[1]
	if path[0] == '\'' || path[0] == '"' {
		path = path[1 : len(path)-1]
	}
	// Commas can form PowerShell argument lists; parentheses, # and ^ can
	// expand paths in shells with extended globbing enabled. Check the path,
	// not the whole command, since sed's supported prefix contains a comma.
	if strings.ContainsAny(path, ",()#^") {
		return ""
	}
	return path
}

// Hook responses vary between Codex hosts: Bash supplies plain stdout, while
// exec_command and file readers can wrap it. Only known text fields are read;
// explicit failure or an unfinished command never supplies skill metadata.
func skillReadText(response any) string {
	if text, ok := response.(string); ok {
		return text
	}
	value, ok := response.(map[string]any)
	if !ok {
		return ""
	}
	for _, key := range []string{"isError", "is_error"} {
		if flag, exists := value[key]; exists && flag != false {
			return ""
		}
	}
	if success, exists := value["success"]; exists && success != true {
		return ""
	}
	if _, running := value["session_id"]; running {
		return ""
	}
	if code, exists := value["exit_code"]; exists && code != float64(0) && code != int(0) {
		return ""
	}
	for _, key := range []string{"output", "content", "text"} {
		if text, ok := value[key].(string); ok {
			return text
		}
	}
	if file, ok := value["file"].(map[string]any); ok {
		text, _ := file["content"].(string)
		return text
	}
	return ""
}

var skillFileName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$`)

func skillFrontmatterName(text string) string {
	text = strings.ReplaceAll(text, "\r\n", "\n")
	if !strings.HasPrefix(text, "---\n") {
		return ""
	}
	// Only the first YAML document is metadata. Do not search prose, code
	// fences, directory listings or concatenated files for plausible names.
	end := strings.Index(text[4:], "\n---")
	if end < 0 || end > 16*1024 {
		return ""
	}
	tail := text[4+end+4:]
	if tail != "" && !strings.HasPrefix(tail, "\n") {
		return ""
	}
	var meta struct {
		Name        yaml.Node `yaml:"name"`
		Description yaml.Node `yaml:"description"`
	}
	if err := yaml.Unmarshal([]byte(text[4:4+end]), &meta); err != nil {
		return ""
	}
	if meta.Name.Kind != yaml.ScalarNode || meta.Name.Tag != "!!str" || !skillFileName.MatchString(meta.Name.Value) {
		return ""
	}
	if meta.Description.Kind != yaml.ScalarNode || meta.Description.Tag != "!!str" || strings.TrimSpace(meta.Description.Value) == "" {
		return ""
	}
	return meta.Name.Value
}
