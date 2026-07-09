// SPDX-FileCopyrightText: Copyright 2026 Dash0 Inc.
// SPDX-License-Identifier: Apache-2.0

package cursor

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Most of these tests mirror real Cursor hook payloads captured from a live
// session. The pre/post-normalization assertions document the field
// transformations the pipeline downstream relies on.

func parse(t *testing.T, raw string) map[string]any {
	t.Helper()
	var m map[string]any
	require.NoError(t, json.Unmarshal([]byte(raw), &m))
	return m
}

func TestNormalize_DropsRedundantHelpers(t *testing.T) {
	dropped := []string{
		"beforeMCPExecution",
		"afterMCPExecution",
		"beforeReadFile",
		"afterFileEdit",
		"beforeShellExecution",
		"afterShellExecution",
		"afterAgentThought",
		"preCompact",
		"subagentStart",
	}
	for _, name := range dropped {
		t.Run(name, func(t *testing.T) {
			ev := map[string]any{"hook_event_name": name}
			assert.Nil(t, Normalize(ev), "%s should be dropped", name)
		})
	}
}

func TestNormalize_HookNameRenames(t *testing.T) {
	cases := map[string]string{
		"sessionStart":       "SessionStart",
		"sessionEnd":         "SessionEnd",
		"beforeSubmitPrompt": "UserPromptSubmit",
		"afterAgentResponse": "Stop",
		"preToolUse":         "PreToolUse",
		"postToolUse":        "PostToolUse",
		"postToolUseFailure": "PostToolUseFailure",
		"subagentStop":       "SubagentStop",
	}
	for cursorName, expected := range cases {
		t.Run(cursorName, func(t *testing.T) {
			ev := map[string]any{"hook_event_name": cursorName}
			out := Normalize(ev)
			require.NotNil(t, out)
			assert.Equal(t, expected, out["hook_event_name"])
		})
	}
}

func TestNormalize_SessionStart(t *testing.T) {
	raw := `{"conversation_id":"2a409a3e-2603-44b3-8856-c6fb4f622874","generation_id":"","model":"default","is_background_agent":false,"composer_mode":"agent","session_id":"2a409a3e-2603-44b3-8856-c6fb4f622874","hook_event_name":"sessionStart","cursor_version":"3.7.27","workspace_roots":["/Users/kristof/source/dash0-agent-plugin"],"user_email":"kristof.berger@dash0.com","transcript_path":null}`
	out := Normalize(parse(t, raw))

	require.NotNil(t, out)
	assert.Equal(t, "SessionStart", out["hook_event_name"])
	assert.Equal(t, "2a409a3e-2603-44b3-8856-c6fb4f622874", out["session_id"])
	assert.Equal(t, "cursor-auto", out["model"], "Auto-routing 'default' is rewritten to 'cursor-auto'")
}

func TestNormalize_BeforeSubmitPromptCarriesPrompt(t *testing.T) {
	raw := `{"conversation_id":"aed69ea7","generation_id":"267cac69","model":"default","composer_mode":"agent","prompt":"hey","attachments":[],"session_id":"aed69ea7","hook_event_name":"beforeSubmitPrompt","cursor_version":"3.7.27","workspace_roots":["/Users/kristof/source/dash0-agent-plugin"],"user_email":"kristof.berger@dash0.com","transcript_path":null}`
	out := Normalize(parse(t, raw))

	require.NotNil(t, out)
	assert.Equal(t, "UserPromptSubmit", out["hook_event_name"])
	assert.Equal(t, "hey", out["prompt"])
}

func TestNormalize_AfterAgentResponse(t *testing.T) {
	raw := `{"conversation_id":"409ef9fc","generation_id":"ba665312","model":"default","text":"The subagent created YOLO.md","input_tokens":247506,"output_tokens":351,"cache_read_tokens":246176,"cache_write_tokens":0,"session_id":"409ef9fc","hook_event_name":"afterAgentResponse","cursor_version":"3.7.27"}`
	out := Normalize(parse(t, raw))

	require.NotNil(t, out)
	assert.Equal(t, "Stop", out["hook_event_name"])
	assert.Equal(t, "The subagent created YOLO.md", out["last_assistant_message"])
	assert.Equal(t, int64(247506), out["gen_ai.usage.input_tokens"])
	assert.Equal(t, int64(351), out["gen_ai.usage.output_tokens"])
	assert.Equal(t, int64(246176), out["gen_ai.usage.cache_read.input_tokens"])
	assert.Equal(t, int64(0), out["gen_ai.usage.cache_creation.input_tokens"])
	// text / token fields are removed from the canonical event so downstream
	// attribute emission doesn't double-up.
	_, hasText := out["text"]
	assert.False(t, hasText)
	_, hasInputTokens := out["input_tokens"]
	assert.False(t, hasInputTokens)
}

func TestNormalize_PreToolUse(t *testing.T) {
	raw := `{"conversation_id":"409ef9fc","generation_id":"c7eb0c0b","model":"default","tool_name":"Read","tool_input":{"file_path":"/x"},"tool_use_id":"tool_abb07c22","session_id":"409ef9fc","hook_event_name":"preToolUse","cursor_version":"3.7.27"}`
	out := Normalize(parse(t, raw))

	require.NotNil(t, out)
	assert.Equal(t, "PreToolUse", out["hook_event_name"])
	assert.Equal(t, "Read", out["tool_name"])
	assert.Equal(t, "tool_abb07c22", out["tool_use_id"])
}

func TestNormalize_PostToolUseRenamesOutputAndDuration(t *testing.T) {
	raw := `{"conversation_id":"409ef9fc","generation_id":"c7eb0c0b","model":"default","tool_name":"Read","tool_input":{"file_path":"/x"},"tool_output":"{\"file_path\":\"/x\",\"content_length\":14786}","duration":14.726,"tool_use_id":"tool_abb07c22","session_id":"409ef9fc","hook_event_name":"postToolUse","cursor_version":"3.7.27"}`
	out := Normalize(parse(t, raw))

	require.NotNil(t, out)
	assert.Equal(t, "PostToolUse", out["hook_event_name"])
	assert.Equal(t, `{"file_path":"/x","content_length":14786}`, out["tool_response"])
	assert.Equal(t, 14.726, out["duration_ms"])
	_, hasOldOutput := out["tool_output"]
	assert.False(t, hasOldOutput, "tool_output should be renamed to tool_response")
	_, hasOldDuration := out["duration"]
	assert.False(t, hasOldDuration, "duration should be renamed to duration_ms")
}

func TestNormalize_PostToolUseFailure(t *testing.T) {
	raw := `{"conversation_id":"409ef9fc","generation_id":"416fcdab","model":"default","tool_name":"Grep","tool_input":{"pattern":""},"error_message":"Path does not exist","failure_type":"error","duration":6.889,"tool_use_id":"tool_eaec39b3","is_interrupt":false,"session_id":"409ef9fc","hook_event_name":"postToolUseFailure","cursor_version":"3.7.27"}`
	out := Normalize(parse(t, raw))

	require.NotNil(t, out)
	assert.Equal(t, "PostToolUseFailure", out["hook_event_name"])
	assert.Equal(t, "Path does not exist", out["error"])
	assert.Equal(t, "error", out["failure_type"])
	assert.Equal(t, 6.889, out["duration_ms"])
	_, hasOldErr := out["error_message"]
	assert.False(t, hasOldErr)
}

func TestNormalize_MCPViaGenericToolUse(t *testing.T) {
	// preToolUse for an MCP call uses tool_name "MCP:<tool>".
	raw := `{"conversation_id":"409ef9fc","generation_id":"98eaf108","model":"default","tool_name":"MCP:list_issues","tool_input":{"limit":1},"tool_use_id":"55b22efc","session_id":"409ef9fc","hook_event_name":"preToolUse","cursor_version":"3.7.27"}`
	out := Normalize(parse(t, raw))

	require.NotNil(t, out)
	assert.Equal(t, "PreToolUse", out["hook_event_name"])
	assert.Equal(t, "list_issues", out["tool_name"], "MCP: prefix should be stripped")
	assert.Equal(t, "cursor", out["mcp_server"], "MCP call should be tagged with placeholder server name in v1")
}

func TestNormalize_SubagentStop(t *testing.T) {
	raw := `{"conversation_id":"409ef9fc","generation_id":"409ef9fc","model":"composer-2.5-fast","subagent_id":"tool_3f3a547c","subagent_type":"general-purpose","status":"completed","duration_ms":166652,"parent_conversation_id":"409ef9fc","message_count":0,"tool_call_count":0,"loop_count":0,"task":"Create YOLO.md","description":"Create YOLO.md about moon","session_id":"409ef9fc","hook_event_name":"subagentStop","cursor_version":"3.7.27"}`
	out := Normalize(parse(t, raw))

	require.NotNil(t, out)
	assert.Equal(t, "SubagentStop", out["hook_event_name"])
	assert.Equal(t, "tool_3f3a547c", out["agent_id"])
	assert.Equal(t, "general-purpose", out["agent_type"])
	// subagentStop already provides duration_ms — no rename needed.
	assert.Equal(t, float64(166652), out["duration_ms"])
	_, hasOldSubID := out["subagent_id"]
	assert.False(t, hasOldSubID)
}

func TestNormalize_SessionEnd(t *testing.T) {
	raw := `{"conversation_id":"594898c5","generation_id":"a532f446","model":"default","reason":"user_close","duration_ms":0,"is_background_agent":false,"final_status":"completed","session_id":"594898c5","hook_event_name":"sessionEnd","cursor_version":"3.7.27"}`
	out := Normalize(parse(t, raw))

	require.NotNil(t, out)
	assert.Equal(t, "SessionEnd", out["hook_event_name"])
	assert.Equal(t, "user_close", out["reason"])
}

func TestNormalize_RewritesDefaultModelToCursorAuto(t *testing.T) {
	cases := []struct {
		name     string
		input    string
		expected string
	}{
		{"default becomes cursor-auto", "default", "cursor-auto"},
		{"explicit Cursor model is preserved", "composer-2.5-fast", "composer-2.5-fast"},
		{"already normalized stays put", "cursor-auto", "cursor-auto"},
		{"empty string passes through", "", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			ev := map[string]any{
				"hook_event_name": "sessionStart",
				"model":           c.input,
			}
			out := Normalize(ev)
			require.NotNil(t, out)
			assert.Equal(t, c.expected, out["model"])
		})
	}
}

func TestNormalize_PreservesUnknownFields(t *testing.T) {
	// Cursor may add fields in future versions; the adapter should pass them
	// through untouched rather than swallowing them.
	ev := map[string]any{
		"hook_event_name":          "preToolUse",
		"tool_name":                "Read",
		"some_future_cursor_field": "preserve_me",
		"another":                  42.0,
	}
	out := Normalize(ev)
	require.NotNil(t, out)
	assert.Equal(t, "preserve_me", out["some_future_cursor_field"])
	assert.Equal(t, 42.0, out["another"])
}
