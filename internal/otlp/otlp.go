// SPDX-FileCopyrightText: Copyright 2026 Dash0 Inc.
// SPDX-License-Identifier: Apache-2.0

package otlp

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/dash0hq/dash0-agent-plugin/internal/vcs"
	"github.com/dash0hq/dash0-agent-plugin/internal/version"
)

// OTLP JSON wire format types.

type ExportLogsRequest struct {
	ResourceLogs []ResourceLogs `json:"resourceLogs"`
}

type ResourceLogs struct {
	Resource  Resource    `json:"resource"`
	ScopeLogs []ScopeLogs `json:"scopeLogs"`
}

type ScopeLogs struct {
	Scope      Scope       `json:"scope"`
	LogRecords []LogRecord `json:"logRecords"`
}

type Resource struct {
	Attributes []Attribute `json:"attributes"`
}

type Scope struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

type LogRecord struct {
	TimeUnixNano   string      `json:"timeUnixNano"`
	SeverityNumber int         `json:"severityNumber"`
	SeverityText   string      `json:"severityText"`
	Body           AttrValue   `json:"body"`
	Attributes     []Attribute `json:"attributes"`
	TraceID        string      `json:"traceId,omitempty"`
	SpanID         string      `json:"spanId,omitempty"`
}

type Attribute struct {
	Key   string    `json:"key"`
	Value AttrValue `json:"value"`
}

type AttrValue struct {
	StringValue *string `json:"stringValue,omitempty"`
	IntValue    *string `json:"intValue,omitempty"`
}

// debugLog writes a debug line to stderr and optionally to a file.
func debugLog(cfg Config, prefix string, payload []byte) {
	line := fmt.Sprintf("[dash0:%s] %s\n", prefix, payload)
	fmt.Fprint(os.Stderr, line)
	if cfg.DebugFile != "" {
		f, err := os.OpenFile(cfg.DebugFile, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
		if err != nil {
			return
		}
		_, _ = f.WriteString(line)
		_ = f.Close()
	}
}

func StringVal(s string) AttrValue {
	return AttrValue{StringValue: &s}
}

func IntVal(n int64) AttrValue {
	s := strconv.FormatInt(n, 10)
	return AttrValue{IntValue: &s}
}

// Config holds the OTLP export configuration.
type Config struct {
	OTLPUrl      string
	AuthToken    string
	Dataset      string
	AgentName    string
	HarnessName  string // coding agent platform identity (e.g. "claude-code", "cursor")
	TeamName     string // when set, tag all spans with the dash0.team.name attribute
	Provider     string // fallback gen_ai.provider.name for events whose model can't be inferred (e.g. SessionStart, PreToolUse). Set by the entrypoint based on host runtime; Cursor leaves it empty since each call's provider is derived from event["model"].
	OmitUserInfo bool   // when true, hash user.name and omit user.email (both span attributes)
	OmitIO       bool   // when true (default), omit tool inputs/outputs and prompt/response content
	Debug        bool   // when true, print OTel payloads to stderr (and DebugFile if set)
	DebugFile    string // optional file path to append debug output to
}

// SendLog sends the event as an OTLP log record to the configured endpoint.
// Returns nil without sending if OTLPUrl is empty and debug is off.
func SendLog(event map[string]any, cfg Config) error {
	if cfg.OTLPUrl == "" && !cfg.Debug {
		return nil
	}

	hookEventName, _ := event["hook_event_name"].(string)

	ts := time.Now().UTC()
	if raw, ok := event["timestamp"].(string); ok {
		if parsed, err := time.Parse(time.RFC3339Nano, raw); err == nil {
			ts = parsed
		}
	}

	attrs := eventAttributes(event, cfg)
	attrs = append(attrs, genAIIdentityAttributes(event, cfg)...)

	serviceName := "claude-code"
	if cfg.AgentName != "" {
		serviceName = cfg.AgentName
	}
	resourceAttrs := []Attribute{
		{Key: "service.name", Value: StringVal(serviceName)},
		{Key: "service.version", Value: StringVal(version.Version)},
	}
	// Correlate log record with the session trace.
	var traceID, spanID string
	if sessionID, _ := event["session_id"].(string); sessionID != "" {
		traceID = TraceIDFromSessionID(sessionID)
		spanID = SpanIDFromSessionID(sessionID)
	}

	req := ExportLogsRequest{
		ResourceLogs: []ResourceLogs{{
			Resource: Resource{
				Attributes: resourceAttrs,
			},
			ScopeLogs: []ScopeLogs{{
				Scope: Scope{
					Name:    "dash0-agent-plugin",
					Version: version.Version,
				},
				LogRecords: []LogRecord{{
					TimeUnixNano:   strconv.FormatInt(ts.UnixNano(), 10),
					SeverityNumber: 9, // INFO
					SeverityText:   "INFO",
					Body:           StringVal(hookEventName),
					Attributes:     attrs,
					TraceID:        traceID,
					SpanID:         spanID,
				}},
			}},
		}},
	}

	payload, err := json.Marshal(req)
	if err != nil {
		return fmt.Errorf("marshalling OTLP request: %w", err)
	}

	if cfg.Debug {
		debugLog(cfg, "log", payload)
	}

	if cfg.OTLPUrl == "" {
		return nil
	}

	return sendOTLP(cfg, "/v1/logs", payload)
}

// sendOTLP sends a payload to the configured OTLP endpoint with a single retry
// on transient failures (network errors or 5xx responses).
// SendRawMetrics sends a pre-marshalled OTLP/JSON metrics payload to the
// configured endpoint. The caller owns the payload shape; this only handles
// debug logging and transport. Returns nil without sending if OTLPUrl is empty
// and debug is off.
func SendRawMetrics(payload []byte, cfg Config) error {
	if cfg.OTLPUrl == "" && !cfg.Debug {
		return nil
	}

	if cfg.Debug {
		debugLog(cfg, "metric", payload)
	}

	if cfg.OTLPUrl == "" {
		return nil
	}

	return sendOTLP(cfg, "/v1/metrics", payload)
}

// CheckConnectivity verifies the OTLP endpoint is reachable and the auth token
// is valid by sending an empty traces export. Returns nil on success.
func CheckConnectivity(cfg Config) error {
	if cfg.OTLPUrl == "" {
		return fmt.Errorf("no OTLP_URL configured")
	}
	empty := []byte(`{"resourceSpans":[]}`)
	return sendOTLP(cfg, "/v1/traces", empty)
}

func sendOTLP(cfg Config, path string, payload []byte) error {
	const maxAttempts = 2
	const retryDelay = 500 * time.Millisecond

	var lastErr error
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)

		httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, cfg.OTLPUrl+path, bytes.NewReader(payload))
		if err != nil {
			cancel()
			return fmt.Errorf("creating HTTP request: %w", err)
		}
		httpReq.Header.Set("Content-Type", "application/json")
		if cfg.AuthToken != "" {
			httpReq.Header.Set("Authorization", "Bearer "+cfg.AuthToken)
		}
		if cfg.Dataset != "" {
			httpReq.Header.Set("Dash0-Dataset", cfg.Dataset)
		}

		resp, err := http.DefaultClient.Do(httpReq)
		if err != nil {
			cancel()
			lastErr = fmt.Errorf("sending OTLP request: %w", err)
			if attempt < maxAttempts {
				time.Sleep(retryDelay)
				continue
			}
			return lastErr
		}
		_ = resp.Body.Close()
		cancel()

		if resp.StatusCode < 300 {
			return nil
		}

		lastErr = fmt.Errorf("OTLP endpoint returned %s", resp.Status)

		// Retry on 5xx (server errors). Don't retry on 4xx (client errors).
		if resp.StatusCode >= 500 && attempt < maxAttempts {
			time.Sleep(retryDelay)
			continue
		}
		return lastErr
	}
	return lastErr
}

// attrSkipKeys lists event fields that should not appear as log attributes.
var attrSkipKeys = map[string]bool{
	"hook_event_name":       true,
	"transcript_path":       true,
	"agent_transcript_path": true,
	"stop_hook_active":      true,
	"permission_mode":       true,
	"is_interrupt":          true,
	"timestamp":             true,
	"source":                true,
	"duration_ms":           true,
}

// MaxContentBytes is the maximum size for content attributes (tool I/O, prompts).
// Larger values are truncated with a marker. 16KB balances useful context
// (stack traces, build errors) against payload size (50 tool calls at max = ~800KB).
const MaxContentBytes = 16 * 1024

const redactedValue = "<REDACTED>"

// contentKeys lists event fields that contain input/output content.
// These are redacted when Config.OmitIO is true, or truncated when included.
var contentKeys = map[string]bool{
	"tool_input":             true,
	"tool_response":          true,
	"last_assistant_message": true,
	"prompt":                 true,
}

// userInfoKeys lists event fields that contain user-identifying information.
// These are redacted when Config.OmitUserInfo is true.
var userInfoKeys = map[string]bool{
	"cwd": true,
}

// attrKeyMap maps event field names to OTLP semantic convention attribute keys.
var attrKeyMap = map[string]string{
	"session_id":          "gen_ai.conversation.id",
	"cwd":                 "process.working_directory",
	"model":               "gen_ai.request.model",
	"tool_name":           "gen_ai.tool.name",
	"tool_input":          "gen_ai.tool.call.arguments",
	"tool_response":       "gen_ai.tool.call.result",
	"tool_use_id":         "gen_ai.tool.call.id",
	"error":               "exception.message",
	"agent_id":            "gen_ai.agent.id",
	"agent_type":          "gen_ai.agent.name",
	"pr_url":              "dash0.gen_ai.vcs.pull_request.url",
	"issue_url":           "dash0.gen_ai.vcs.issue.url",
	"commit_sha":          "dash0.gen_ai.vcs.commit.sha",
	"lines_added":         "dash0.gen_ai.code.lines_added",
	"lines_removed":       "dash0.gen_ai.code.lines_removed",
	"bash_command_family": "dash0.gen_ai.tool.bash.command_family",
	"skill_name":          "dash0.gen_ai.tool.skill.name",
	"mcp_server":          "dash0.gen_ai.tool.mcp_server",
	"user_email":          "user.email",
}

// attrTransformMap maps event field names to a target key and a value transform function.
var attrTransformMap = map[string]struct {
	key       string
	transform func(any) string
}{
	"last_assistant_message": {
		key:       "gen_ai.output.messages",
		transform: transformAssistantMessage,
	},
	"prompt": {
		key:       "gen_ai.input.messages",
		transform: transformUserMessage,
	},
}

func transformMessage(role string, v any) string {
	content := stringifyValue(v)
	msg := []map[string]any{{
		"role": role,
		"parts": []map[string]any{{
			"type":    "text",
			"content": content,
		}},
	}}
	b, err := json.Marshal(msg)
	if err != nil {
		return content
	}
	return string(b)
}

func transformAssistantMessage(v any) string { return transformMessage("assistant", v) }
func transformUserMessage(v any) string      { return transformMessage("user", v) }

// eventAttributes converts all fields in the event map to OTLP log attributes.
func eventAttributes(event map[string]any, cfg Config) []Attribute {
	var attrs []Attribute
	for k, v := range event {
		if attrSkipKeys[k] {
			continue
		}
		if cfg.OmitUserInfo && userInfoKeys[k] {
			key := k
			if mapped, ok := attrKeyMap[k]; ok {
				key = mapped
			}
			if s, ok := v.(string); ok {
				attrs = append(attrs, Attribute{Key: key, Value: StringVal(redactHomeDir(s))})
			}
			continue
		}
		if cfg.OmitIO && contentKeys[k] {
			key := k
			if mapped, ok := attrKeyMap[k]; ok {
				key = mapped
			}
			if t, ok := attrTransformMap[k]; ok {
				// Apply transform with redacted placeholder to preserve JSON structure
				redactedTransformed := t.transform(redactedValue)
				attrs = append(attrs, Attribute{Key: t.key, Value: StringVal(redactedTransformed)})
			} else {
				attrs = append(attrs, Attribute{Key: key, Value: StringVal(redactedValue)})
			}
			continue
		}
		if t, ok := attrTransformMap[k]; ok {
			s := t.transform(v)
			if s != "" {
				if contentKeys[k] {
					s = truncateContent(s)
				}
				attrs = append(attrs, Attribute{Key: t.key, Value: StringVal(s)})
			}
			continue
		}
		key := k
		if mapped, ok := attrKeyMap[k]; ok {
			key = mapped
		}
		av := toAttrValue(v)
		if av.StringValue != nil && contentKeys[k] {
			truncated := truncateContent(*av.StringValue)
			av = StringVal(truncated)
		}
		if av.StringValue != nil || av.IntValue != nil {
			attrs = append(attrs, Attribute{Key: key, Value: av})
		}
	}
	return attrs
}

// truncateContent caps a string to MaxContentBytes, appending a marker if truncated.
func truncateContent(s string) string {
	if len(s) <= MaxContentBytes {
		return s
	}
	marker := fmt.Sprintf("... [truncated, %dKB total]", len(s)/1024)
	return s[:MaxContentBytes-len(marker)] + marker
}

// toAttrValue converts a Go value to an OTLP attribute value. Explicitly typed
// int64 values (injected programmatically) produce IntVal; float64 from JSON
// unmarshaling produces StringVal for backward compatibility.
func toAttrValue(v any) AttrValue {
	switch val := v.(type) {
	case int64:
		return IntVal(val)
	case string:
		if val == "" {
			return AttrValue{}
		}
		return StringVal(val)
	case float64:
		if val == float64(int64(val)) {
			return StringVal(strconv.FormatInt(int64(val), 10))
		}
		return StringVal(strconv.FormatFloat(val, 'f', -1, 64))
	case bool:
		return StringVal(strconv.FormatBool(val))
	case nil:
		return AttrValue{}
	default:
		b, err := json.Marshal(val)
		if err != nil {
			return StringVal(fmt.Sprintf("%v", val))
		}
		return StringVal(string(b))
	}
}

func stringifyValue(v any) string {
	switch val := v.(type) {
	case string:
		return val
	case float64:
		if val == float64(int64(val)) {
			return strconv.FormatInt(int64(val), 10)
		}
		return strconv.FormatFloat(val, 'f', -1, 64)
	case bool:
		return strconv.FormatBool(val)
	case nil:
		return ""
	default:
		b, err := json.Marshal(val)
		if err != nil {
			return fmt.Sprintf("%v", val)
		}
		return string(b)
	}
}

// genAIIdentityAttributes returns the coding-agent identity attributes
// (gen_ai.provider.name, gen_ai.agent.name, gen_ai.harness.name) that describe
// which agent/harness/provider produced the telemetry. These belong on each
// span/log record rather than on the resource.
//
// gen_ai.agent.name is set per-event from agent_type by eventAttributes (via
// attrKeyMap) for sub-agent invocations; we only fall back to the configured
// agent name when the event doesn't carry one, so the key is never emitted twice.
func genAIIdentityAttributes(event map[string]any, cfg Config) []Attribute {
	var attrs []Attribute
	if provider := resolveProvider(event, cfg); provider != "" {
		attrs = append(attrs, Attribute{Key: "gen_ai.provider.name", Value: StringVal(provider)})
	}
	if agentType, _ := event["agent_type"].(string); agentType == "" && cfg.AgentName != "" {
		attrs = append(attrs, Attribute{Key: "gen_ai.agent.name", Value: StringVal(cfg.AgentName)})
	}
	if cfg.HarnessName != "" {
		attrs = append(attrs, Attribute{Key: "gen_ai.harness.name", Value: StringVal(cfg.HarnessName)})
	}
	return attrs
}

// teamSpanAttributes returns the dash0.team.name span attribute when a team name
// is configured. Returns nil otherwise.
func teamSpanAttributes(cfg Config) []Attribute {
	if cfg.TeamName == "" {
		return nil
	}
	return []Attribute{{Key: "dash0.team.name", Value: StringVal(cfg.TeamName)}}
}

// vcsSpanAttributes returns dash0.gen_ai.vcs.* and user.* span attributes derived from the
// current git state. Returns nil if not inside a git repository.
func vcsSpanAttributes(cfg Config) []Attribute {
	info := vcs.Detect()
	if info == nil {
		return nil
	}

	attr := func(key, val string) Attribute {
		return Attribute{Key: key, Value: StringVal(val)}
	}

	var attrs []Attribute
	if info.RepositoryURLFull != "" {
		attrs = append(attrs, attr("dash0.gen_ai.vcs.repository.url.full", info.RepositoryURLFull))
	}
	if info.RepositoryName != "" {
		attrs = append(attrs, attr("dash0.gen_ai.vcs.repository.name", info.RepositoryName))
	}
	if info.OwnerName != "" {
		attrs = append(attrs, attr("dash0.gen_ai.vcs.owner.name", info.OwnerName))
	}
	if info.ProviderName != "" {
		attrs = append(attrs, attr("dash0.gen_ai.vcs.provider.name", info.ProviderName))
	}
	if info.RefHeadName != "" {
		attrs = append(attrs, attr("dash0.gen_ai.vcs.ref.head.name", info.RefHeadName))
	}
	if info.RefHeadRevision != "" {
		attrs = append(attrs, attr("dash0.gen_ai.vcs.ref.head.revision", info.RefHeadRevision))
	}
	if info.RefHeadType != "" {
		attrs = append(attrs, attr("dash0.gen_ai.vcs.ref.head.type", info.RefHeadType))
	}
	if info.UserName != "" {
		if cfg.OmitUserInfo {
			attrs = append(attrs, attr("user.name", hashIdentity(info.UserName)))
		} else {
			attrs = append(attrs, attr("user.name", info.UserName))
		}
	}
	if info.UserEmail != "" && !cfg.OmitUserInfo {
		attrs = append(attrs, attr("user.email", info.UserEmail))
	}

	return attrs
}

// hashIdentity returns a short, stable, non-reversible identifier derived from
// the input string. Used to anonymize user.name while preserving groupability.
func hashIdentity(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:8])
}

// redactHomeDir replaces the user's home directory prefix in the path with "~".
// If the path doesn't start with the home directory, it is returned unchanged.
func redactHomeDir(path string) string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return path
	}
	home = filepath.Clean(home)
	path = filepath.Clean(path)
	if path == home {
		return "~"
	}
	if strings.HasPrefix(path, home+string(filepath.Separator)) {
		return "~" + path[len(home):]
	}
	return path
}
