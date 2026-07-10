// Package codex normalizes OpenAI Codex hook payloads into the pipeline's
// canonical event vocabulary. Unlike Cursor, Codex reuses Claude Code's hook
// event names (PascalCase: SessionStart, PreToolUse, PostToolUse, Stop, …) and
// field names (session_id, tool_name, tool_input, tool_response, tool_use_id,
// prompt, last_assistant_message, agent_id, agent_type, …), so this normalizer
// is nearly a passthrough.
//
// Its one substantive job: Codex omits a per-tool-call duration. We reconstruct
// duration_ms on PostToolUse by looking up the matching PreToolUse (same
// tool_use_id) that the pipeline logged to the session's events.jsonl, and
// diffing timestamps. The pipeline uses duration_ms to back-date the tool span's
// start time.
//
// Token usage is intentionally NOT handled here — it lives in the Codex rollout
// file (transcript_path / agent_transcript_path) and is read by the pipeline's
// transcript layer.
package codex

import (
	"encoding/json"
	"time"

	"github.com/dash0hq/dash0-agent-plugin/internal/filelog"
)

// Normalize adjusts a single Codex hook event in place to the pipeline's
// canonical shape and returns it. sessionDir is the per-session scratch
// directory (dataDir/<session_id>); now is the event's processing time. It
// returns nil for events the pipeline should not process (none today).
func Normalize(event map[string]any, sessionDir string, now time.Time) map[string]any {
	hookName, _ := event["hook_event_name"].(string)

	switch hookName {
	case "PostToolUse", "PostToolUseFailure":
		ensureDurationMs(event, sessionDir, now)
		anchorSpawnAgent(event)
	}

	return event
}

// anchorSpawnAgent makes Codex's sub-agent delegation parent correctly.
//
// Codex spawns a sub-agent via the `spawn_agent` tool, whose response is
// {"agent_id":"<id>","nickname":"..."}. The sub-agent's own turn and tool events
// then carry that agent_id, and the pipeline parents them under
// SpanIDFromAgentID(agent_id). But nothing creates a span WITH that id unless the
// pipeline recognizes the spawning call as the canonical "Agent" tool (Claude's
// name) and finds the spawned id under the "agentId" key.
//
// So on a spawn_agent PostToolUse we: (1) rename the tool to "Agent" so the
// pipeline anchors its span id to SpanIDFromAgentID(spawned id), matching what
// the workers point to; and (2) add an "agentId" key to the response so the
// pipeline's Claude-shaped extractor finds the id. Without this the sub-agent
// spans dangle under a non-existent parent.
func anchorSpawnAgent(event map[string]any) {
	if name, _ := event["tool_name"].(string); name != "spawn_agent" {
		return
	}
	resp, _ := event["tool_response"].(string)
	if resp == "" {
		return
	}
	var parsed map[string]any
	if err := json.Unmarshal([]byte(resp), &parsed); err != nil {
		return
	}
	id, _ := parsed["agent_id"].(string)
	if id == "" {
		return
	}

	event["tool_name"] = "Agent"
	// Preserve the original response fields; add the camelCase key the pipeline's
	// agent-id extractor expects.
	parsed["agentId"] = id
	if rewritten, err := json.Marshal(parsed); err == nil {
		event["tool_response"] = string(rewritten)
	}
}

// ensureDurationMs injects duration_ms (float64 milliseconds) when it is absent,
// derived from the timestamp of the matching PreToolUse event. Best-effort: if
// the tool_use_id is missing, no PreToolUse is found, or its timestamp cannot be
// parsed, the field is left unset and the pipeline falls back to a zero-duration
// span starting at `now`.
func ensureDurationMs(event map[string]any, sessionDir string, now time.Time) {
	if _, ok := event["duration_ms"].(float64); ok {
		return
	}
	toolUseID, _ := event["tool_use_id"].(string)
	if toolUseID == "" {
		return
	}

	pre, err := filelog.FindEvent(sessionDir, func(e map[string]any) bool {
		name, _ := e["hook_event_name"].(string)
		id, _ := e["tool_use_id"].(string)
		return name == "PreToolUse" && id == toolUseID
	})
	if err != nil || pre == nil {
		return
	}

	raw, ok := pre["timestamp"].(string)
	if !ok || raw == "" {
		return
	}
	preTS, err := time.Parse(time.RFC3339Nano, raw)
	if err != nil {
		return
	}

	if d := now.Sub(preTS); d > 0 {
		event["duration_ms"] = float64(d.Milliseconds())
	}
}
