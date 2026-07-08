// SPDX-FileCopyrightText: Copyright 2026 Dash0 Inc.
// SPDX-License-Identifier: Apache-2.0

// Package pipeline is the source-agnostic engine that turns normalized hook
// events into OTLP spans. Both the Claude Code and Cursor entrypoints feed
// already-normalized events into Process; this package owns trace context
// lifecycle, span emission, and per-session scratch state.
package pipeline

import (
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/dash0hq/dash0-agent-plugin/internal/filelog"
	"github.com/dash0hq/dash0-agent-plugin/internal/otlp"
	"github.com/dash0hq/dash0-agent-plugin/internal/transcript"
)

// Result is the structured output of Process. Source-specific entrypoints render
// Messages via their own hook output contract (Claude Code emits JSON on stdout
// with systemMessage/additionalContext; Cursor uses a different contract).
type Result struct {
	Messages []Message
}

// Message carries text destined for both user-facing display and (optionally)
// model context injection.
type Message struct {
	UserText     string
	ModelContext string
}

// Process consumes a single normalized hook event and produces side effects
// (filelog write, trace context update, OTLP export) plus a Result with any
// messages the source should render. dataDir is the per-source scratch root
// (e.g. CLAUDE_PLUGIN_DATA); session-scoped state lives at dataDir/<session_id>.
//
// Process never returns an error caused by telemetry export — those are logged
// to stderr and swallowed so the agent loop never breaks. It only returns an
// error for fatal local issues (missing dataDir, filesystem failures).
func Process(event map[string]any, cfg otlp.Config, dataDir string, now time.Time) (Result, error) {
	var res Result

	event["timestamp"] = now.Format(time.RFC3339Nano)

	hookEvent, _ := event["hook_event_name"].(string)
	agentID, _ := event["agent_id"].(string)

	sessionID, _ := event["session_id"].(string)
	if sessionID == "" {
		fmt.Fprintf(os.Stderr, "on-event: session_id missing in %s event, using random ID\n", hookEvent)
		randID, err := otlp.GenerateTraceID()
		if err != nil {
			return res, err
		}
		event["session_id"] = randID[:16]
		sessionID = event["session_id"].(string)
		event["dash0.warning"] = "session_id was missing from hook payload"
	}

	sessionDir := filepath.Join(dataDir, sessionID)
	if err := os.MkdirAll(sessionDir, 0o755); err != nil {
		return res, fmt.Errorf("creating session directory: %w", err)
	}

	startedFile := filepath.Join(sessionDir, "started")
	sessionAlreadyStarted := false
	if hookEvent == "SessionStart" {
		if _, err := os.Stat(startedFile); err == nil {
			sessionAlreadyStarted = true
		} else {
			model, _ := event["model"].(string)
			if err := otlp.SaveTraceContext(otlp.TraceContext{
				SessionID: sessionID,
				Model:     model,
			}, sessionDir); err != nil {
				return res, err
			}
			_ = os.WriteFile(startedFile, nil, 0o644)
		}
	}

	if hookEvent == "UserPromptSubmit" && agentID == "" {
		traceID, err := otlp.GenerateTraceID()
		if err != nil {
			return res, err
		}
		chatSpanID, err := otlp.GenerateSpanID()
		if err != nil {
			return res, err
		}
		event["chat_span_id"] = chatSpanID

		model := ""
		if ctx, err := otlp.LoadTraceContext(sessionDir); err == nil && ctx != nil {
			model = ctx.Model
		}

		if err := otlp.SaveTraceContext(otlp.TraceContext{
			TraceID:   traceID,
			SpanID:    chatSpanID,
			SessionID: sessionID,
			Model:     model,
		}, sessionDir); err != nil {
			return res, err
		}
	}

	if err := filelog.WriteEvent(event, sessionDir); err != nil {
		return res, err
	}

	if hookEvent == "SessionStart" && !sessionAlreadyStarted {
		switch cfg.OTLPUrl {
		case "":
			res.Messages = append(res.Messages, Message{
				UserText: "dash0: telemetry is not active — configure the plugin to start sending data.",
			})
		default:
			if err := otlp.CheckConnectivity(cfg); err != nil {
				res.Messages = append(res.Messages, Message{
					UserText: fmt.Sprintf("dash0: connectivity check failed — %v", err),
				})
			} else {
				res.Messages = append(res.Messages, Message{
					UserText: "dash0: connected",
				})
			}
		}
	}

	switch hookEvent {
	case "PostToolUse", "PostToolUseFailure":
		if err := sendToolTrace(event, cfg, now, sessionDir, hookEvent == "PostToolUseFailure"); err != nil {
			fmt.Fprintf(os.Stderr, "on-event: trace export: %v\n", err)
		}
	case "Stop", "StopFailure":
		if err := sendLLMTrace(event, cfg, now, sessionDir, hookEvent == "StopFailure"); err != nil {
			fmt.Fprintf(os.Stderr, "on-event: trace export: %v\n", err)
		}
		otlp.ClearTraceContext(sessionDir)
	case "SubagentStop":
		if err := sendLLMTrace(event, cfg, now, sessionDir, false); err != nil {
			fmt.Fprintf(os.Stderr, "on-event: trace export (subagent): %v\n", err)
		}
	case "SessionEnd":
		if ctx, err := otlp.LoadTraceContext(sessionDir); err == nil && ctx != nil && ctx.TraceID != "" {
			event["error"] = "session ended before completion"
			if err := sendLLMTrace(event, cfg, now, sessionDir, true); err != nil {
				fmt.Fprintf(os.Stderr, "on-event: trace export (session end fallback): %v\n", err)
			}
		}
	}

	if hookEvent == "SessionEnd" {
		_ = os.RemoveAll(sessionDir)
	}

	return res, nil
}

// SessionDir returns the per-session scratch directory under dataDir for the
// given session ID. Source entrypoints can use this to look up state that
// outlives a single hook invocation (e.g. for cross-event correlation).
func SessionDir(dataDir, sessionID string) string {
	return filepath.Join(dataDir, sessionID)
}

func sendToolTrace(event map[string]any, cfg otlp.Config, ts time.Time, dataDir string, failed bool) error {
	ctx, err := otlp.LoadTraceContext(dataDir)
	if err != nil || ctx == nil {
		return fmt.Errorf("no trace context available for tool span")
	}

	traceID := ctx.TraceID
	parentSpanID := ctx.SpanID

	if _, hasModel := event["model"]; !hasModel && ctx.Model != "" {
		event["model"] = ctx.Model
	}

	if _, hasModel := event["model"]; !hasModel {
		if tp, _ := event["transcript_path"].(string); tp != "" {
			if m := transcript.ReadModel(tp); m != "" {
				event["model"] = m
			}
		}
	}

	startTime := ts
	if durationMs, ok := event["duration_ms"].(float64); ok && durationMs > 0 {
		startTime = ts.Add(-time.Duration(durationMs) * time.Millisecond)
	}

	toolName, _ := event["tool_name"].(string)
	agentID, _ := event["agent_id"].(string)

	var spanID string
	if toolName == "Agent" {
		resultAgentID := extractAgentIDFromResponse(event["tool_response"])
		if resultAgentID != "" {
			spanID = otlp.SpanIDFromAgentID(resultAgentID)
			event["agent_id"] = resultAgentID
		} else {
			spanID, err = otlp.GenerateSpanID()
			if err != nil {
				return err
			}
		}
	} else {
		spanID, err = otlp.GenerateSpanID()
		if err != nil {
			return err
		}
	}

	if toolName != "Agent" && agentID != "" {
		parentSpanID = otlp.SpanIDFromAgentID(agentID)
	}

	resp := event["tool_response"]
	if prURL := ExtractPRURL(resp); prURL != "" {
		event["pr_url"] = prURL
	}
	if issueURL := ExtractIssueURL(resp); issueURL != "" {
		event["issue_url"] = issueURL
	}
	if sha := ExtractCommitSHA(resp); sha != "" {
		event["commit_sha"] = sha
	}

	if added, removed := ExtractLinesCounts(resp); added > 0 || removed > 0 {
		event["lines_added"] = int64(added)
		event["lines_removed"] = int64(removed)
	}

	toolInput := event["tool_input"]
	if toolName == "Bash" {
		if family := ExtractBashCommandFamily(toolInput); family != "" {
			event["bash_command_family"] = family
		}
	}
	if toolName == "Skill" {
		if skill := ExtractSkillName(toolInput); skill != "" {
			event["skill_name"] = skill
		}
	}
	if server := ExtractMCPServer(toolName); server != "" {
		event["mcp_server"] = server
	}
	if normalized := NormalizeMCPToolName(toolName); normalized != toolName {
		event["tool_name"] = normalized
	}

	span := otlp.NewToolSpan(traceID, spanID, parentSpanID, startTime, ts, event, failed, cfg)
	return otlp.SendTrace(span, event, cfg)
}

func sendLLMTrace(event map[string]any, cfg otlp.Config, ts time.Time, dataDir string, failed bool) error {
	ctx, err := otlp.LoadTraceContext(dataDir)
	if err != nil || ctx == nil {
		return fmt.Errorf("no trace context available for LLM span")
	}

	traceID := ctx.TraceID
	spanID := ctx.SpanID

	if _, hasModel := event["model"]; !hasModel && ctx.Model != "" {
		event["model"] = ctx.Model
	}

	startTime := ts
	promptEvent, _ := filelog.FindEvent(dataDir, func(e map[string]any) bool {
		name, _ := e["hook_event_name"].(string)
		return name == "UserPromptSubmit"
	})
	if promptEvent != nil {
		if raw, ok := promptEvent["timestamp"].(string); ok {
			if parsed, parseErr := time.Parse(time.RFC3339Nano, raw); parseErr == nil {
				startTime = parsed
			}
		}
		if prompt, ok := promptEvent["prompt"]; ok {
			if _, hasPrompt := event["prompt"]; !hasPrompt {
				event["prompt"] = prompt
			}
		}
	}

	agentID, _ := event["agent_id"].(string)
	parentSpanID := ""
	if agentID != "" {
		parentSpanID = otlp.SpanIDFromAgentID(agentID)
		newSpanID, err := otlp.GenerateSpanID()
		if err != nil {
			return fmt.Errorf("generating sub-agent span ID: %w", err)
		}
		spanID = newSpanID
	}

	transcriptPath, _ := event["transcript_path"].(string)
	if agentID != "" {
		if atp, ok := event["agent_transcript_path"].(string); ok && atp != "" {
			transcriptPath = atp
		}
	}
	if transcriptPath != "" {
		usage, err := transcript.ReadTurnUsage(transcriptPath)
		if err != nil {
			fmt.Fprintf(os.Stderr, "on-event: reading transcript: %v\n", err)
		}
		if usage != nil {
			event["gen_ai.usage.input_tokens"] = usage.InputTokens
			event["gen_ai.usage.output_tokens"] = usage.OutputTokens
			event["gen_ai.usage.cache_creation.input_tokens"] = usage.CacheCreationInputTokens
			event["gen_ai.usage.cache_read.input_tokens"] = usage.CacheReadInputTokens
		}

		if title := transcript.ReadSessionTitle(transcriptPath); title != "" {
			event["gen_ai.conversation.name"] = title
		}

		if _, hasModel := event["model"]; !hasModel {
			if m := transcript.ReadModel(transcriptPath); m != "" {
				event["model"] = m
			}
		}
	}

	span := otlp.NewLLMSpan(traceID, spanID, parentSpanID, startTime, ts, event, failed, cfg)
	return otlp.SendTrace(span, event, cfg)
}

var prURLPattern = regexp.MustCompile(`https?://[^\s"'<>\x60\])]+/(?:pull/\d+|pull-requests/\d+|-/merge_requests/\d+)`)

var issueURLPattern = regexp.MustCompile(`https?://[^\s"'<>\x60\])]+/issues/\d+`)

var commitSHAPattern = regexp.MustCompile(`^\[[\w/.-]+ ([0-9a-f]{7,40})\]`)

// ToolResponseText extracts the scannable text from a tool response.
// Bash tool responses are dicts with stdout/stderr; other responses may be
// plain strings or arbitrary dicts.
func ToolResponseText(v any) string {
	if v == nil {
		return ""
	}
	switch val := v.(type) {
	case string:
		return val
	case map[string]any:
		var parts []string
		if stdout, ok := val["stdout"].(string); ok && stdout != "" {
			parts = append(parts, stdout)
		}
		if stderr, ok := val["stderr"].(string); ok && stderr != "" {
			parts = append(parts, stderr)
		}
		if len(parts) > 0 {
			return strings.Join(parts, "\n")
		}
		b, err := json.Marshal(val)
		if err != nil {
			return ""
		}
		return string(b)
	default:
		b, err := json.Marshal(val)
		if err != nil {
			return ""
		}
		return string(b)
	}
}

// ExtractPRURL scans a tool response for a pull/merge request URL.
func ExtractPRURL(v any) string {
	return prURLPattern.FindString(ToolResponseText(v))
}

// ExtractIssueURL scans a tool response for an issue URL.
func ExtractIssueURL(v any) string {
	return issueURLPattern.FindString(ToolResponseText(v))
}

// ExtractCommitSHA scans a tool response for a git commit SHA from the
// standard git commit output format: [branch SHA] message
func ExtractCommitSHA(v any) string {
	text := ToolResponseText(v)
	for _, line := range strings.Split(text, "\n") {
		if m := commitSHAPattern.FindStringSubmatch(line); len(m) > 1 {
			return m[1]
		}
	}
	return ""
}

// ExtractLinesCounts returns the number of lines added and removed from a tool
// response that contains a structuredPatch (Edit/Write/MultiEdit tools).
func ExtractLinesCounts(v any) (added, removed int) {
	m, ok := v.(map[string]any)
	if !ok {
		return 0, 0
	}

	patches, ok := m["structuredPatch"].([]any)
	if !ok || len(patches) == 0 {
		return 0, 0
	}

	for _, p := range patches {
		patch, ok := p.(map[string]any)
		if !ok {
			continue
		}
		lines, ok := patch["lines"].([]any)
		if !ok {
			continue
		}
		for _, l := range lines {
			line, ok := l.(string)
			if !ok || len(line) == 0 {
				continue
			}
			switch line[0] {
			case '+':
				added++
			case '-':
				removed++
			}
		}
	}
	return added, removed
}

// ExtractBashCommandFamily extracts the leading binary name from a Bash tool
// input, skipping environment variable assignments (KEY=val prefixes).
// Input may be a string ("git status") or a map with a "command" field.
func ExtractBashCommandFamily(v any) string {
	var cmd string
	switch val := v.(type) {
	case string:
		cmd = val
	case map[string]any:
		cmd, _ = val["command"].(string)
	default:
		return ""
	}
	if cmd == "" {
		return ""
	}
	for _, token := range strings.Fields(cmd) {
		if strings.Contains(token, "=") && !strings.HasPrefix(token, "-") {
			continue
		}
		binary := filepath.Base(token)
		if binary == "." || binary == "/" {
			return ""
		}
		return binary
	}
	return ""
}

// ExtractSkillName parses the skill name from a Skill tool's input.
// Input may be a JSON string or an already-decoded map with a "skill" field.
func ExtractSkillName(v any) string {
	switch val := v.(type) {
	case string:
		if val == "" {
			return ""
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(val), &m); err != nil {
			return ""
		}
		name, _ := m["skill"].(string)
		return name
	case map[string]any:
		name, _ := val["skill"].(string)
		return name
	default:
		return ""
	}
}

// NormalizeMCPToolName strips the mcp__<server>__ prefix from an MCP tool name,
// returning just the tool portion (e.g. "send_message"). For non-MCP tools it
// returns the input unchanged.
func NormalizeMCPToolName(toolName string) string {
	if !strings.HasPrefix(toolName, "mcp__") {
		return toolName
	}
	parts := strings.SplitN(toolName, "__", 3)
	if len(parts) < 3 || parts[2] == "" {
		return toolName
	}
	return parts[2]
}

// ExtractMCPServer parses the server name from an MCP tool name
// (format: mcp__<server>__<tool>). Returns empty string for non-MCP tools
// and for UUIDs (which are not meaningful server names).
func ExtractMCPServer(toolName string) string {
	if !strings.HasPrefix(toolName, "mcp__") {
		return ""
	}
	parts := strings.SplitN(toolName, "__", 3)
	if len(parts) < 2 || parts[1] == "" {
		return ""
	}
	if isUUID(parts[1]) {
		return ""
	}
	return parts[1]
}

var uuidPattern = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

func isUUID(s string) bool {
	return uuidPattern.MatchString(s)
}

// ExtractAgentIDFromResponse is exported for use by source-specific entrypoints
// that need to materialize an agent_id before handing the event to Process.
func ExtractAgentIDFromResponse(v any) string {
	return extractAgentIDFromResponse(v)
}

func extractAgentIDFromResponse(v any) string {
	var m map[string]any
	switch val := v.(type) {
	case string:
		if err := json.Unmarshal([]byte(val), &m); err != nil {
			return ""
		}
	case map[string]any:
		m = val
	default:
		return ""
	}
	id, _ := m["agentId"].(string)
	return id
}

// ValidateOTLPURL clears cfg.OTLPUrl if it is malformed and logs to stderr.
// Returns whether the URL was valid.
func ValidateOTLPURL(cfg *otlp.Config) bool {
	if cfg.OTLPUrl == "" {
		return false
	}
	u, err := url.Parse(cfg.OTLPUrl)
	if err != nil || u.Scheme == "" || u.Host == "" {
		fmt.Fprintf(os.Stderr, "on-event: OTLP URL is not valid: %q\n", cfg.OTLPUrl)
		cfg.OTLPUrl = ""
		return false
	}
	return true
}
