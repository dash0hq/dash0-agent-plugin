// SPDX-FileCopyrightText: Copyright 2026 Dash0 Inc.
// SPDX-License-Identifier: Apache-2.0

// Package cursor normalizes Cursor hook payloads into the pipeline's canonical
// event vocabulary. The pipeline then handles trace context, span emission,
// and OTLP export uniformly across sources.
package cursor

import "strings"

// Normalize transforms a single Cursor hook event in place to the pipeline's
// canonical shape. It returns the same map (or a renamed/mutated version) when
// the event should be processed, or nil when the event is a redundant helper
// the pipeline doesn't act on.
//
// Cursor fires both a generic preToolUse/postToolUse pair AND specialized
// before*/after* hooks for the same logical tool call (shell, file edit, MCP).
// We drop the specialized helpers — the generic pair already carries
// tool_use_id, tool_input, tool_output, and duration. Helpers that would
// otherwise enrich (e.g. afterFileEdit's edits[]) are deferred to v2.
func Normalize(event map[string]any) map[string]any {
	hookName, _ := event["hook_event_name"].(string)
	if drop(hookName) {
		return nil
	}

	if mapped, ok := hookNameMap[hookName]; ok {
		event["hook_event_name"] = mapped
	}

	renameField(event, "tool_output", "tool_response")
	renameField(event, "error_message", "error")
	renameField(event, "subagent_id", "agent_id")
	renameField(event, "subagent_type", "agent_type")

	// Cursor uses `duration` (float, ms) on tool/shell/MCP events.
	// The pipeline expects `duration_ms` (float64 ms — it casts to ms internally).
	if d, ok := event["duration"].(float64); ok && d > 0 {
		event["duration_ms"] = d
		delete(event, "duration")
	}

	// Cursor reports model="default" when the user has the model picker set
	// to Auto — the actual model is chosen per-request by Cursor's backend
	// and never surfaced (not in the hook payload, not in the transcript,
	// not in any user-readable on-disk state). Rewrite to "cursor-auto" so
	// downstream dashboards can distinguish auto-routing from a literal
	// model whose vendor name happens to be "default".
	if model, ok := event["model"].(string); ok && model == "default" {
		event["model"] = "cursor-auto"
	}

	// afterAgentResponse carries the assistant text + the per-turn token usage.
	// These are the only place Cursor exposes per-call tokens, so the rename
	// from afterAgentResponse to Stop has to bring the token fields along too.
	if hookName == "afterAgentResponse" {
		renameField(event, "text", "last_assistant_message")
		moveInt64(event, "input_tokens", "gen_ai.usage.input_tokens")
		moveInt64(event, "output_tokens", "gen_ai.usage.output_tokens")
		moveInt64(event, "cache_read_tokens", "gen_ai.usage.cache_read.input_tokens")
		moveInt64(event, "cache_write_tokens", "gen_ai.usage.cache_creation.input_tokens")
	}

	// Cursor exposes MCP calls through generic preToolUse/postToolUse with
	// tool_name "MCP:<tool>". The specialized MCP hooks (which carry the
	// server name) are dropped, so v1 tags mcp_server with a placeholder
	// rather than the real server identity. v2 can cross-reference by joining
	// on tool name + generation_id within the session scratch dir.
	if toolName, ok := event["tool_name"].(string); ok && strings.HasPrefix(toolName, "MCP:") {
		event["tool_name"] = strings.TrimPrefix(toolName, "MCP:")
		event["mcp_server"] = "cursor"
	}

	return event
}

// drop is true for Cursor hook events the v1 pipeline does not consume.
// These either duplicate data already carried by other events (shell/file/MCP
// helpers) or have no current consumer (afterAgentThought, preCompact).
// subagentStart is dropped because v1 does not synthesize the parent Agent
// tool span — subagentStop's LLM span dangles under the chat span, which is
// acceptable for v1 and tightened in v2.
func drop(hookName string) bool {
	switch hookName {
	case "beforeMCPExecution", "afterMCPExecution",
		"beforeReadFile", "afterFileEdit",
		"beforeShellExecution", "afterShellExecution",
		"afterAgentThought",
		"preCompact",
		"subagentStart":
		return true
	}
	return false
}

// hookNameMap renames Cursor's lowerCamel hook event names to the PascalCase
// vocabulary the pipeline uses (inherited from Claude Code's payload shapes).
var hookNameMap = map[string]string{
	"sessionStart":       "SessionStart",
	"sessionEnd":         "SessionEnd",
	"beforeSubmitPrompt": "UserPromptSubmit",
	"afterAgentResponse": "Stop",
	"preToolUse":         "PreToolUse",
	"postToolUse":        "PostToolUse",
	"postToolUseFailure": "PostToolUseFailure",
	"subagentStop":       "SubagentStop",
}

func renameField(event map[string]any, from, to string) {
	if v, ok := event[from]; ok {
		event[to] = v
		delete(event, from)
	}
}

// moveInt64 copies a numeric field under a new key as an int64 (the OTLP
// attribute layer uses IntVal for int64-typed attributes). Cursor's token
// fields come over JSON as float64.
func moveInt64(event map[string]any, from, to string) {
	switch v := event[from].(type) {
	case float64:
		event[to] = int64(v)
		delete(event, from)
	case int64:
		event[to] = v
		delete(event, from)
	}
}
