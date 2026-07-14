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
// Token usage lives in the Codex rollout file (transcript_path, or
// agent_transcript_path for a sub-agent) rather than the hook payload, so on the
// stop-family events that produce a chat/invoke_agent span we read the rollout
// and inject gen_ai.usage.* onto the event (see injectTokenUsage + rollout.go).
// This mirrors how the Cursor normalizer injects usage; the pipeline's span
// builder emits any gen_ai.usage.* keys verbatim, so no pipeline change is
// needed. The Claude transcript reader the pipeline also runs no-ops on a Codex
// rollout (its records never carry a top-level "assistant" type), so it never
// clobbers what we set here.
package codex

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
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
	case "Stop", "StopFailure", "SubagentStop":
		injectTokenUsage(event)
	}

	return event
}

// injectTokenUsage reads the just-completed turn's token usage from the Codex
// rollout file and writes it onto the event as gen_ai.usage.* attributes, which
// the pipeline's LLM span builder emits verbatim. A sub-agent has its own rollout
// (agent_transcript_path); the main session uses transcript_path. Best-effort: on
// a missing path or any read/parse failure the event is left unchanged and the
// span is emitted without token attributes.
func injectTokenUsage(event map[string]any) {
	path, _ := event["transcript_path"].(string)
	if atp, _ := event["agent_transcript_path"].(string); atp != "" {
		path = atp
	}
	if path == "" {
		return
	}

	// A compressed rollout is unreadable without a zstd dependency this module
	// deliberately avoids. Mark the span (dash0.* vendor namespace — the gen_ai.*
	// semconv namespace stays clean) so the missing usage is visible in telemetry
	// as a known gap rather than a bug, and queryable to catch the day Codex
	// starts compressing rollouts in the field. The span attribute (plus the e2e
	// canary) is the reachable signal; Codex does not surface hook stderr, so we
	// don't bother logging here.
	if strings.HasSuffix(path, ".zst") {
		event["dash0.codex.rollout.compressed"] = true
		return
	}

	usage, err := ReadTurnUsage(path)
	if err != nil {
		fmt.Fprintf(os.Stderr, "codex: reading rollout usage: %v\n", err)
		return
	}
	if usage == nil {
		return
	}

	event["gen_ai.usage.input_tokens"] = usage.InputTokens
	event["gen_ai.usage.output_tokens"] = usage.OutputTokens
	event["gen_ai.usage.cache_read.input_tokens"] = usage.CacheReadInputTokens
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
