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
		injectRollout(event)
	}

	return event
}

// injectRollout reads the Codex rollout file once and writes what it yields onto
// the event: the just-completed turn's token usage as gen_ai.usage.* attributes,
// and the account's billing/allowance state as dash0.gen_ai.* ones. The pipeline's
// LLM span builder emits both verbatim. A sub-agent has its own rollout
// (agent_transcript_path); the main session uses transcript_path. Best-effort: on
// a missing path or any read/parse failure the event is left unchanged and the
// span is emitted without these attributes.
func injectRollout(event map[string]any) {
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

	rollout, err := ReadRollout(path)
	if err != nil {
		fmt.Fprintf(os.Stderr, "codex: reading rollout: %v\n", err)
		return
	}
	if rollout == nil {
		return
	}

	if usage := rollout.Usage; usage != nil {
		event["gen_ai.usage.input_tokens"] = usage.InputTokens
		event["gen_ai.usage.output_tokens"] = usage.OutputTokens
		// Both cache halves, unconditionally. A zero is a measurement — the turn
		// read or wrote nothing from cache — and dropping the key at zero makes
		// "no cache activity" indistinguishable from "this runtime does not
		// report it", which is exactly the confusion that hid the missing
		// cache-creation key.
		event["gen_ai.usage.cache_read.input_tokens"] = usage.CacheReadInputTokens
		event["gen_ai.usage.cache_creation.input_tokens"] = usage.CacheCreationInputTokens
		// Reasoning only when there is some, matching what Claude and Copilot
		// do: absence means the turn did no thinking, and a zero on every
		// non-thinking turn is noise. It is a subset of output_tokens rather
		// than an addition, so cost is unaffected either way.
		if usage.ReasoningOutputTokens > 0 {
			event["gen_ai.usage.reasoning.output_tokens"] = usage.ReasoningOutputTokens
		}
	}
	injectBilling(event, rollout.Limits)
}

// injectBilling writes the account's billing and allowance state onto the event.
// Attribute names, the harness-neutral namespace, and the omit-don't-zero rule
// are specified in DEVELOPMENT.md.
func injectBilling(event map[string]any, l *Limits) {
	// Always stated, including "unknown" — recording that we looked and could
	// not tell is different from never having looked. Everything below is
	// omitted rather than zeroed, because a zero here reads as a measurement.
	event["dash0.gen_ai.billing_mode"] = l.BillingMode()
	if l == nil {
		return
	}

	if l.PlanType != "" {
		event["dash0.gen_ai.plan_type"] = l.PlanType
	}
	// Both slots emit under matching keys so a consumer picks the window it wants
	// by window_minutes, rather than relying on an ordering Codex does not fix.
	injectWindow(event, "primary", l.Primary)
	injectWindow(event, "secondary", l.Secondary)
	// Null until a limit is actually hit, and a limit not hit is not an event.
	if l.ReachedType != "" {
		event["dash0.gen_ai.rate_limit.reached_type"] = l.ReachedType
	}
	if c := l.Credits; c != nil {
		event["dash0.gen_ai.credits.available"] = c.HasCredits
		event["dash0.gen_ai.credits.unlimited"] = c.Unlimited
		if c.Balance != nil {
			event["dash0.gen_ai.credits.balance"] = *c.Balance
		}
	}
}

// injectWindow writes one allowance window under dash0.gen_ai.rate_limit.<slot>.*,
// or nothing at all when the plan does not report that slot.
func injectWindow(event map[string]any, slot string, w *Window) {
	if w == nil {
		return
	}
	prefix := "dash0.gen_ai.rate_limit." + slot + "."
	event[prefix+"used_percent"] = w.UsedPercent
	event[prefix+"window_minutes"] = w.WindowMinutes
	event[prefix+"resets_at"] = w.ResetsAt
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
// spawnAnchorWaitBudget caps how long anchorSpawnAgent waits for Codex to flush
// the SubAgentActivity record. Codex writes it within milliseconds of the spawn
// call returning — measured 13ms ahead of the hook on one run and already
// present on both spawns of another — but the rollout is flushed asynchronously,
// so a hook that wins the race would otherwise silently lose the anchor and
// orphan every span the sub-agent produces. Only spawn calls pay this, and only
// when the record is not already there.
const (
	spawnAnchorWaitBudget   = 250 * time.Millisecond
	spawnAnchorPollInterval = 25 * time.Millisecond
)

// waitForSpawnedAgentID polls the calling thread's rollout for the mapping this
// spawn call produced. Returns "" when the budget elapses, which leaves the span
// unanchored rather than wrong.
func waitForSpawnedAgentID(rolloutPath, spawnCallID string) string {
	if rolloutPath == "" || spawnCallID == "" {
		return ""
	}
	deadline := time.Now().Add(spawnAnchorWaitBudget)
	for {
		id, err := ReadSpawnedAgentID(rolloutPath, spawnCallID)
		if err != nil {
			fmt.Fprintf(os.Stderr, "codex: reading spawn anchor: %v\n", err)
			return ""
		}
		if id != "" {
			return id
		}
		if time.Now().After(deadline) {
			return ""
		}
		time.Sleep(spawnAnchorPollInterval)
	}
}

func anchorSpawnAgent(event map[string]any) {
	// Codex namespaces this tool and has changed the prefix at least once, with
	// no separator: 0.142.5 sent bare "spawn_agent" (alongside
	// "multi_agent_v1wait_agent"), 0.149.1 sends "collaborationspawn_agent". An
	// exact match went stale on the rename, and the failure is silent and ugly —
	// the anchor is never created, so the sub-agent's invoke_agent span and every
	// tool call inside it parent onto a span id nothing emitted, and they hang
	// outside the trace. Match the suffix so the next prefix cannot break it.
	if name, _ := event["tool_name"].(string); !strings.HasSuffix(name, "spawn_agent") {
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

	// Where the spawned agent's id comes from, newest source first.
	//
	// 0.142.5 put agent_id in the spawn call's own response and this was a field
	// read. 0.149.1 returns only {"task_name":"/root/<name>"}, so there is
	// nothing in the response to anchor with, and every sub-agent span in the
	// session ended up parented onto an id no span carried.
	//
	// Codex does record the mapping, in the rollout of the thread that made the
	// call: a SubAgentActivity item keyed by this very call id. transcript_path
	// on this payload IS that thread's rollout — the main session's at depth 0,
	// the parent agent's for a nested spawn — so the lookup is correct at any
	// depth without knowing the depth. Verified against both on
	// qa/runs/probe-codex-two-subagents.
	id, _ := parsed["agent_id"].(string)
	if id == "" {
		callID, _ := event["tool_use_id"].(string)
		path, _ := event["transcript_path"].(string)
		id = waitForSpawnedAgentID(path, callID)
	}
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
