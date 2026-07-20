// SPDX-FileCopyrightText: Copyright 2026 Dash0 Inc.
// SPDX-License-Identifier: Apache-2.0

// Package copilot normalizes GitHub Copilot CLI hook payloads into the
// pipeline's canonical event vocabulary, and recovers each turn — token/cost/
// model/response AND the turn's tool executions — from Copilot's
// native-OpenTelemetry file.
//
// Copilot's per-turn hook (`agentStop`) fires only under camelCase
// registration, and camelCase payloads carry no `hook_event_name` field — so
// the entrypoint passes the event name as an argv and hands it to Normalize.
// Hooks drive only the session/turn lifecycle (SessionStart, UserPromptSubmit,
// Stop, SessionEnd); everything quantitative comes from the native-OTel file
// via the reader in otelfile.go: tokens are NOT in the hook payloads (nor in
// `events.jsonl`), and tool spans are sourced from the file's execute_tool
// spans — real durations and sub-agent nesting that hooks cannot provide.
package copilot

import "strings"

// Normalize transforms a Copilot hook payload (identified by eventName, which
// the caller reads from argv since camelCase payloads omit the event name) into
// the pipeline's canonical event shape. It returns nil for events the pipeline
// does not consume, so the caller can exit cleanly.
//
// Deliberately dropped:
//   - postToolUse/postToolUseFailure: hook tool events carry no duration (the
//     spans they'd produce are zero-length instants) and never fire inside
//     sub-agents. Tool spans come from the native-OTel file's execute_tool
//     spans instead — real timings, failure status, and sub-agent tool calls
//     nested under their spawning `task` span (see otelfile.go / the
//     entrypoint's emitToolSpans).
//   - preToolUse: Copilot's only fail-closed event; not registering it avoids
//     the foot-gun of a broken hook blocking the user's tools.
//   - subagentStart/subagentStop AND every sub-agent lifecycle event: a sub-agent
//     runs under a synthetic "call_<toolCallId>" session id, not the parent
//     conversation's UUID, and carries nothing that links back to it (verified
//     against captured payloads) — so it cannot be nested under the parent from
//     hook data. We drop these sessions wholesale (the call_ guard in Normalize)
//     rather than mint a spurious, token-less trace per sub-agent. Their tokens
//     still roll into the parent turn via the native-OTel reader (sub-agent chat
//     spans share the parent's conversation.id), and their tool calls surface as
//     OTel-sourced spans nested under the parent's `task` span.
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
// vocabulary the pipeline consumes. Events absent from this map are dropped —
// notably postToolUse/postToolUseFailure: tool spans come from the native-OTel
// file, not hooks (see the package doc).
var eventNameMap = map[string]string{
	"sessionStart":        "SessionStart",
	"userPromptSubmitted": "UserPromptSubmit",
	"agentStop":           "Stop",
	"sessionEnd":          "SessionEnd",
}

func renameField(event map[string]any, from, to string) {
	if v, ok := event[from]; ok {
		event[to] = v
		delete(event, from)
	}
}
