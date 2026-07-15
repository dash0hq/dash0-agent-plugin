// SPDX-FileCopyrightText: Copyright 2026 Dash0 Inc.
// SPDX-License-Identifier: Apache-2.0

// Package copilot normalizes GitHub Copilot CLI hook payloads into the
// pipeline's canonical event vocabulary (the Cursor pattern), and recovers
// per-turn token/cost/model usage from Copilot's native-OpenTelemetry file.
//
// Copilot's per-turn hook (`agentStop`) fires only under camelCase
// registration, and camelCase payloads carry no `hook_event_name` field — so
// the entrypoint passes the event name as an argv and hands it to Normalize.
// Tokens are NOT in the hook payloads (nor in `events.jsonl`, which has output
// tokens only); they come from the native-OTel file via the reader in
// otelfile.go, attached to the turn's Stop event before pipeline.Process.
package copilot

import (
	"encoding/json"
	"strings"
)

// Normalize transforms a Copilot hook payload (identified by eventName, which
// the caller reads from argv since camelCase payloads omit the event name) into
// the pipeline's canonical event shape. It returns nil for events the pipeline
// does not consume, so the caller can exit cleanly.
//
// Deliberately dropped in v1:
//   - preToolUse: Copilot's only fail-closed event and its payload carries no
//     duration; we emit zero-duration tool spans from postToolUse instead, and
//     avoid the fail-closed foot-gun by not registering it.
//   - subagentStart/subagentStop AND every sub-agent lifecycle event: a sub-agent
//     runs under a synthetic "call_<toolCallId>" session id, not the parent
//     conversation's UUID, and carries nothing that links back to it (verified
//     against captured payloads) — so it cannot be nested under the parent. We
//     drop these sessions wholesale (the call_ guard in Normalize) rather than
//     mint a spurious, token-less trace per sub-agent. Their tokens still roll
//     into the parent turn via the native-OTel reader (sub-agent chat spans share
//     the parent's conversation.id), and the parent conversation still shows each
//     spawn as a `task` tool span.
//   - notification/preCompact/permissionRequest/errorOccurred: no span consumer.
func Normalize(eventName string, event map[string]any) map[string]any {
	if event == nil {
		// A JSON `null` payload decodes to a nil map; writing to it would panic
		// and break the mandatory fail-open (exit 0) contract.
		return nil
	}
	canonical, ok := eventNameMap[eventName]
	if !ok {
		return nil
	}
	event["hook_event_name"] = canonical

	renameField(event, "sessionId", "session_id")

	// A sub-agent turn runs under a synthetic "call_<toolCallId>" session id, not
	// the parent conversation's UUID, and carries nothing linking back to it — so
	// processing it would mint a standalone, token-less trace (a spurious extra
	// "conversation"). Drop the whole sub-agent session; see the package doc.
	if sid, _ := event["session_id"].(string); strings.HasPrefix(sid, "call_") {
		return nil
	}

	renameField(event, "toolName", "tool_name")
	renameField(event, "toolArgs", "tool_input")
	renameField(event, "toolResult", "tool_response")
	liftTaskName(event)

	// Never carry Copilot's transcriptPath through: it points at events.jsonl
	// (not Claude-JSONL), so the pipeline's transcript reader must not run on it.
	// Per-turn tokens come from the native-OTel file instead.
	delete(event, "transcriptPath")
	delete(event, "transcript_path")

	// initialPrompt (sessionStart) is user content the identity/session span
	// doesn't need; the prompt for the chat span comes from userPromptSubmitted.
	delete(event, "initialPrompt")

	return event
}

// eventNameMap maps Copilot's camelCase hook names to the canonical PascalCase
// vocabulary the pipeline consumes. Events absent from this map are dropped.
var eventNameMap = map[string]string{
	"sessionStart":        "SessionStart",
	"userPromptSubmitted": "UserPromptSubmit",
	"postToolUse":         "PostToolUse",
	"postToolUseFailure":  "PostToolUseFailure",
	"agentStop":           "Stop",
	"sessionEnd":          "SessionEnd",
}

func renameField(event map[string]any, from, to string) {
	if v, ok := event[from]; ok {
		event[to] = v
		delete(event, from)
	}
}

// taskNameAttr is the span attribute carrying a sub-agent spawn's instance name;
// it mirrors the dash0.gen_ai.tool.<tool>.<detail> shape of skill_name/command_family.
const taskNameAttr = "dash0.gen_ai.tool.task.name"

// liftTaskName surfaces the instance name of a sub-agent spawn (the `task` tool)
// that Copilot buries inside the tool arguments, so the parent's task tool span
// is identifiable — otherwise every task span reads as a generic "task". The name
// (e.g. "echo-runner") is a non-content label; it is set directly under its wire
// attribute key (the pipeline passes through unmapped keys verbatim), keeping this
// Copilot-specific detail out of the shared attribute map. Copilot passes tool
// arguments as a JSON string, so it is parsed (an object is accepted defensively).
func liftTaskName(event map[string]any) {
	if tn, _ := event["tool_name"].(string); tn != "task" {
		return
	}
	var args map[string]any
	switch t := event["tool_input"].(type) {
	case map[string]any:
		args = t
	case string:
		_ = json.Unmarshal([]byte(t), &args)
	}
	if name, _ := args["name"].(string); name != "" {
		event[taskNameAttr] = name
	}
}
