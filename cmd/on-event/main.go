package main

import (
	"bytes"
	"compress/zlib"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/dash0hq/dash0-agent-plugin/internal/dotenv"
	"github.com/dash0hq/dash0-agent-plugin/internal/filelog"
	"github.com/dash0hq/dash0-agent-plugin/internal/otlp"
	"github.com/dash0hq/dash0-agent-plugin/internal/transcript"
	"github.com/dash0hq/dash0-agent-plugin/internal/version"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "on-event: %v\n", err)
		os.Exit(1)
	}
}

func sendToolTrace(event map[string]any, cfg otlp.Config, ts time.Time, dataDir string, failed bool) error {
	// Load trace context (trace_id, chat_span_id, model) from current turn.
	ctx, err := otlp.LoadTraceContext(dataDir)
	if err != nil || ctx == nil {
		return fmt.Errorf("no trace context available for tool span")
	}

	traceID := ctx.TraceID
	parentSpanID := ctx.SpanID // chat span is the default parent

	// Inject model from context if the event doesn't have one.
	if _, hasModel := event["model"]; !hasModel && ctx.Model != "" {
		event["model"] = ctx.Model
	}

	// Fall back to reading model from transcript if still missing.
	if _, hasModel := event["model"]; !hasModel {
		if tp, _ := event["transcript_path"].(string); tp != "" {
			if m := transcript.ReadModel(tp); m != "" {
				event["model"] = m
			}
		}
	}

	// Compute start time from duration_ms (always present on PostToolUse).
	startTime := ts
	if durationMs, ok := event["duration_ms"].(float64); ok && durationMs > 0 {
		startTime = ts.Add(-time.Duration(durationMs) * time.Millisecond)
	}

	toolName, _ := event["tool_name"].(string)
	agentID, _ := event["agent_id"].(string)

	var spanID string
	if toolName == "Agent" {
		// Sub-agent tool call: derive span ID from the spawned agent's ID
		// in the tool call result so child spans can reference it as their parent.
		resultAgentID := extractAgentIDFromResponse(event["tool_response"])
		if resultAgentID != "" {
			spanID = otlp.SpanIDFromAgentID(resultAgentID)
			// Set the spawned agent's ID as a span attribute.
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
		// This tool call was made by a sub-agent: nest it under the Agent
		// tool call span.
		parentSpanID = otlp.SpanIDFromAgentID(agentID)
	}
	// Main-agent tool calls: parentSpanID stays as chat span (from context).

	// Extract VCS metadata before eventAttributes redacts tool_response.
	resp := event["tool_response"]
	if prURL := extractPRURL(resp); prURL != "" {
		event["pr_url"] = prURL
	}
	if issueURL := extractIssueURL(resp); issueURL != "" {
		event["issue_url"] = issueURL
	}
	if sha := extractCommitSHA(resp); sha != "" {
		event["commit_sha"] = sha
	}

	// Extract tool metadata attributes before OMIT_IO redacts tool_input.
	toolInput := event["tool_input"]
	if toolName == "Bash" {
		if family := extractBashCommandFamily(toolInput); family != "" {
			event["bash_command_family"] = family
		}
	}
	if toolName == "Skill" {
		if skill := extractSkillName(toolInput); skill != "" {
			event["skill_name"] = skill
		}
	}
	if server := extractMCPServer(toolName); server != "" {
		event["mcp_server"] = server
	}

	span := otlp.NewToolSpan(traceID, spanID, parentSpanID, startTime, ts, event, failed, cfg)
	return otlp.SendTrace(span, event, cfg)
}

func sendLLMTrace(event map[string]any, cfg otlp.Config, ts time.Time, dataDir string, failed bool) error {
	// Load trace context (trace_id, chat_span_id, model) from current turn.
	ctx, err := otlp.LoadTraceContext(dataDir)
	if err != nil || ctx == nil {
		return fmt.Errorf("no trace context available for LLM span")
	}

	traceID := ctx.TraceID
	spanID := ctx.SpanID // chat span ID (stamped at UserPromptSubmit)

	// Inject model from context if the event doesn't have one.
	if _, hasModel := event["model"]; !hasModel && ctx.Model != "" {
		event["model"] = ctx.Model
	}

	// Look up matching UserPromptSubmit event to get the start timestamp.
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
		// Carry the user prompt onto the Stop event so it appears as an attribute on the span.
		if prompt, ok := promptEvent["prompt"]; ok {
			if _, hasPrompt := event["prompt"]; !hasPrompt {
				event["prompt"] = prompt
			}
		}
	}

	// If this LLM invocation belongs to a sub-agent, nest it under the
	// Agent tool call span.
	agentID, _ := event["agent_id"].(string)
	parentSpanID := "" // chat span is root by default
	if agentID != "" {
		parentSpanID = otlp.SpanIDFromAgentID(agentID)
	}

	// Extract token usage from the transcript file.
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
			event["gen_ai.usage.input_tokens"] = int64(usage.InputTokens)
			event["gen_ai.usage.output_tokens"] = int64(usage.OutputTokens)
			event["gen_ai.usage.cache_creation_input_tokens"] = int64(usage.CacheCreationInputTokens)
			event["gen_ai.usage.cache_read_input_tokens"] = int64(usage.CacheReadInputTokens)
		}

		// Extract session title from transcript (custom-title set by user or auto-generated).
		// Falls back to the user prompt (already set above from UserPromptSubmit).
		if title := transcript.ReadSessionTitle(transcriptPath); title != "" {
			event["gen_ai.conversation.name"] = title
		}

		// Extract model from transcript when not already set (SessionStart may omit it).
		if _, hasModel := event["model"]; !hasModel {
			if m := transcript.ReadModel(transcriptPath); m != "" {
				event["model"] = m
			}
		}
	}

	span := otlp.NewLLMSpan(traceID, spanID, parentSpanID, startTime, ts, event, failed, cfg)
	return otlp.SendTrace(span, event, cfg)
}


// prURLPattern matches GitHub, GitLab, and Bitbucket pull/merge request URLs,
// including self-hosted instances. Excludes /pull/new/ (pre-creation links from git push).
var prURLPattern = regexp.MustCompile(`https?://[^\s"'<>\x60\])]+/(?:pull/\d+|pull-requests/\d+|-/merge_requests/\d+)`)

// issueURLPattern matches GitHub and GitLab issue URLs.
var issueURLPattern = regexp.MustCompile(`https?://[^\s"'<>\x60\])]+/issues/\d+`)

// commitSHAPattern matches a git commit output line: [branch SHA] message
var commitSHAPattern = regexp.MustCompile(`^\[[\w/.-]+ ([0-9a-f]{7,40})\]`)

// toolResponseText extracts the scannable text from a tool response.
// Bash tool responses are dicts with stdout/stderr; other responses may be
// plain strings or arbitrary dicts.
func toolResponseText(v any) string {
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

// extractPRURL scans a tool response for a pull/merge request URL.
func extractPRURL(v any) string {
	return prURLPattern.FindString(toolResponseText(v))
}

// extractIssueURL scans a tool response for an issue URL.
func extractIssueURL(v any) string {
	return issueURLPattern.FindString(toolResponseText(v))
}

// extractCommitSHA scans a tool response for a git commit SHA from the
// standard git commit output format: [branch SHA] message
func extractCommitSHA(v any) string {
	text := toolResponseText(v)
	for _, line := range strings.Split(text, "\n") {
		if m := commitSHAPattern.FindStringSubmatch(line); len(m) > 1 {
			return m[1]
		}
	}
	return ""
}

// extractBashCommandFamily extracts the leading binary name from a Bash tool
// input, skipping environment variable assignments (KEY=val prefixes).
// Input may be a string ("git status") or a map with a "command" field.
func extractBashCommandFamily(v any) string {
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

// extractSkillName parses the skill name from a Skill tool's input.
// Input may be a JSON string or an already-decoded map with a "skill" field.
func extractSkillName(v any) string {
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

// extractMCPServer parses the server name from an MCP tool name
// (format: mcp__<server>__<tool>).
func extractMCPServer(toolName string) string {
	if !strings.HasPrefix(toolName, "mcp__") {
		return ""
	}
	parts := strings.SplitN(toolName, "__", 3)
	if len(parts) < 2 || parts[1] == "" {
		return ""
	}
	return parts[1]
}

// extractAgentIDFromResponse parses the agentId from an Agent tool's response.
// The response may be a JSON string or an already-decoded map.
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

// printHookResponse outputs a JSON response that Claude Code renders as both
// a user-visible message (systemMessage) and model context (additionalContext).
func printHookResponse(userMessage, modelContext string) {
	resp := map[string]string{}
	if userMessage != "" {
		resp["systemMessage"] = userMessage
	}
	if modelContext != "" {
		resp["additionalContext"] = modelContext
	}
	out, _ := json.Marshal(resp)
	fmt.Fprintln(os.Stdout, string(out))
}

// deriveAppURL maps an OTLP ingress URL to the corresponding Dash0 app URL.
// Returns empty string if the URL doesn't match a known Dash0 pattern.
func deriveAppURL(otlpURL string) string {
	if otlpURL == "" {
		return ""
	}
	u, err := url.Parse(otlpURL)
	if err != nil {
		return ""
	}
	host := u.Hostname()
	switch {
	case strings.HasSuffix(host, ".dash0.com"):
		return "https://app.dash0.com"
	case strings.HasSuffix(host, ".dash0-dev.com"):
		return "https://app.dash0-dev.com"
	default:
		return ""
	}
}

// buildSessionURL constructs a full Dash0 session details URL with the encoded
// URL state parameter that the Dash0 UI expects.
func buildSessionURL(appURL, sessionID string) string {
	state := map[string]any{
		"/agent-monitoring/claude-code/sessions/details": map[string]any{
			"agentSession": map[string]any{
				"sessionId": sessionID,
			},
		},
	}
	stateJSON, err := json.Marshal(state)
	if err != nil {
		return appURL + "/agent-monitoring/claude-code/sessions/details"
	}
	var buf bytes.Buffer
	w := zlib.NewWriter(&buf)
	w.Write(stateJSON)
	w.Close()
	encoded := base64.URLEncoding.WithPadding(base64.NoPadding).EncodeToString(buf.Bytes())
	return appURL + "/agent-monitoring/claude-code/sessions/details?s=" + encoded
}

// envBool returns true when the environment variable is set to "true" or "1".
func envBool(key string) bool {
	v := strings.ToLower(strings.TrimSpace(os.Getenv(key)))
	return v == "true" || v == "1"
}

// pluginOption returns the configured value for the given key, preferring
// the userConfig-derived CLAUDE_PLUGIN_OPTION_<key> over the legacy DASH0_<key>.
// An empty CLAUDE_PLUGIN_OPTION_<key> falls through to DASH0_<key>.
//
// Note: sensitive values (AUTH_TOKEN) must use pluginOptionSecure instead to
// prevent env var leakage into tool-spawned shells.
func pluginOption(key string) string {
	if v := os.Getenv("CLAUDE_PLUGIN_OPTION_" + key); v != "" {
		return v
	}
	return os.Getenv("DASH0_" + key)
}

// pluginOptionSecure reads only from CLAUDE_PLUGIN_OPTION_<key> without falling
// back to DASH0_<key>. Use for sensitive values like auth tokens that must not
// leak into tool-spawned shell environments.
func pluginOptionSecure(key string) string {
	return os.Getenv("CLAUDE_PLUGIN_OPTION_" + key)
}

// pluginOptionBool is the boolean counterpart of pluginOption.
func pluginOptionBool(key string) bool {
	v := strings.ToLower(strings.TrimSpace(pluginOption(key)))
	return v == "true" || v == "1"
}

// pluginOptionBoolDefault returns defaultVal when the option is unset/empty,
// and parses as boolean otherwise.
func pluginOptionBoolDefault(key string, defaultVal bool) bool {
	v := strings.ToLower(strings.TrimSpace(pluginOption(key)))
	if v == "" {
		return defaultVal
	}
	return v == "true" || v == "1"
}

func run() error {
	dotenv.Load(".env")

	dataDir := os.Getenv("CLAUDE_PLUGIN_DATA")
	if dataDir == "" {
		return fmt.Errorf("CLAUDE_PLUGIN_DATA is not set")
	}

	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		return fmt.Errorf("creating data directory: %w", err)
	}

	raw, err := io.ReadAll(os.Stdin)
	if err != nil {
		return fmt.Errorf("reading stdin: %w", err)
	}

	var event map[string]any
	if err := json.Unmarshal(raw, &event); err != nil {
		return fmt.Errorf("parsing JSON from stdin: %w", err)
	}

	now := time.Now().UTC()
	event["timestamp"] = now.Format(time.RFC3339Nano)

	hookEvent, _ := event["hook_event_name"].(string)
	agentID, _ := event["agent_id"].(string)

	// If session_id is missing, generate a random one so spans don't all
	// merge. Log a warning so the user knows.
	sessionID, _ := event["session_id"].(string)
	if sessionID == "" {
		fmt.Fprintf(os.Stderr, "on-event: session_id missing in %s event, using random ID\n", hookEvent)
		randID, err := otlp.GenerateTraceID()
		if err != nil {
			return err
		}
		event["session_id"] = randID[:16]
		sessionID = event["session_id"].(string)
		event["dash0.warning"] = "session_id was missing from hook payload"
	}

	// Scope the data directory per session to prevent concurrent sessions
	// from corrupting each other's state.
	sessionDir := filepath.Join(dataDir, sessionID)
	if err := os.MkdirAll(sessionDir, 0o755); err != nil {
		return fmt.Errorf("creating session directory: %w", err)
	}

	// At SessionStart, save the model to trace context for later turns.
	if hookEvent == "SessionStart" {
		model, _ := event["model"].(string)
		if err := otlp.SaveTraceContext(otlp.TraceContext{
			SessionID: sessionID,
			Model:     model,
		}, sessionDir); err != nil {
			return err
		}
	}

	// At UserPromptSubmit, generate a new trace_id and chat_span_id for this
	// turn. Save to trace context so tool spans and the chat span can use them.
	if hookEvent == "UserPromptSubmit" && agentID == "" {
		traceID, err := otlp.GenerateTraceID()
		if err != nil {
			return err
		}
		chatSpanID, err := otlp.GenerateSpanID()
		if err != nil {
			return err
		}
		event["chat_span_id"] = chatSpanID

		// Get model from existing context (set at SessionStart).
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
			return err
		}
	}

	if err := filelog.WriteEvent(event, sessionDir); err != nil {
		return err
	}

	cfg := otlp.Config{
		OTLPUrl:      pluginOption("OTLP_URL"),
		AuthToken:    pluginOptionSecure("AUTH_TOKEN"),
		Dataset:      pluginOption("DATASET"),
		AgentName:    pluginOption("AGENT_NAME"),
		OmitUserInfo: pluginOptionBoolDefault("OMIT_USER_INFO", false),
		OmitIO:       pluginOptionBoolDefault("OMIT_IO", true),
		Debug:        pluginOptionBool("DEBUG"),
		DebugFile:    pluginOption("DEBUG_FILE"),
	}

	if cfg.OTLPUrl != "" {
		u, err := url.Parse(cfg.OTLPUrl)
		if err != nil || u.Scheme == "" || u.Host == "" {
			fmt.Fprintf(os.Stderr, "on-event: OTLP URL is not valid: %q\n", cfg.OTLPUrl)
			cfg.OTLPUrl = "" // disable export to prevent cryptic errors
		}
	}

	if hookEvent == "SessionStart" {
		if cfg.OTLPUrl == "" {
			printHookResponse(
				"dash0: telemetry is not active — configure the plugin to start sending data. Run /plugin → Installed → dash0 → Configure, then /reload-plugins.",
				"",
			)
		} else if err := otlp.CheckConnectivity(cfg); err != nil {
			printHookResponse(
				fmt.Sprintf("dash0: connectivity check failed — %v", err),
				"",
			)
		} else {
			printHookResponse(fmt.Sprintf("dash0: connected (v%s)", version.Version), "")
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
		if appURL := deriveAppURL(cfg.OTLPUrl); appURL != "" {
			sessionURL := buildSessionURL(appURL, sessionID)
			printHookResponse(fmt.Sprintf("dash0: view session → %s", sessionURL), "")
		}
		// Clear trace context so SessionEnd knows the chat span was already emitted.
		otlp.ClearTraceContext(sessionDir)
	case "SessionEnd":
		// If trace context exists, Stop never fired — emit a partial chat
		// span with error status so orphaned tool spans have a parent.
		if ctx, err := otlp.LoadTraceContext(sessionDir); err == nil && ctx != nil && ctx.TraceID != "" {
			event["error"] = "session ended before completion"
			if err := sendLLMTrace(event, cfg, now, sessionDir, true); err != nil {
				fmt.Fprintf(os.Stderr, "on-event: trace export (session end fallback): %v\n", err)
			}
		}
	}

	// Clean up session directory at SessionEnd.
	if hookEvent == "SessionEnd" {
		os.RemoveAll(sessionDir)
	}

	return nil
}
