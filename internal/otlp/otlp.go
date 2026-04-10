package otlp

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/dash0hq/dash0-agent-plugin/internal/vcs"
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
}

type Attribute struct {
	Key   string    `json:"key"`
	Value AttrValue `json:"value"`
}

type AttrValue struct {
	StringValue *string `json:"stringValue,omitempty"`
}

func StringVal(s string) AttrValue {
	return AttrValue{StringValue: &s}
}

// Config holds the OTLP export configuration.
type Config struct {
	OTLPUrl   string
	AuthToken string
	Dataset   string
	AgentName string
}

// SendLog sends the event as an OTLP log record to the configured endpoint.
// Returns nil without sending if OTLPUrl is empty.
func SendLog(event map[string]any, cfg Config) error {
	if cfg.OTLPUrl == "" {
		return nil
	}

	hookEventName, _ := event["hook_event_name"].(string)

	ts := time.Now().UTC()
	if raw, ok := event["timestamp"].(string); ok {
		if parsed, err := time.Parse(time.RFC3339Nano, raw); err == nil {
			ts = parsed
		}
	}

	attrs := eventAttributes(event)

	serviceName := "claude-code"
	if cfg.AgentName != "" {
		serviceName = cfg.AgentName
	}
	resourceAttrs := []Attribute{
		{Key: "service.name", Value: StringVal(serviceName)},
		{Key: "gen_ai.provider.name", Value: StringVal("anthropic")},
	}
	if cfg.AgentName != "" {
		resourceAttrs = append(resourceAttrs, Attribute{Key: "gen_ai.agent.name", Value: StringVal(cfg.AgentName)})
	}
	resourceAttrs = append(resourceAttrs, vcsResourceAttributes()...)

	req := ExportLogsRequest{
		ResourceLogs: []ResourceLogs{{
			Resource: Resource{
				Attributes: resourceAttrs,
			},
			ScopeLogs: []ScopeLogs{{
				Scope: Scope{
					Name:    "dash0-agent-plugin",
					Version: "0.1.0",
				},
				LogRecords: []LogRecord{{
					TimeUnixNano:   strconv.FormatInt(ts.UnixNano(), 10),
					SeverityNumber: 9, // INFO
					SeverityText:   "INFO",
					Body:           StringVal(hookEventName),
					Attributes:     attrs,
				}},
			}},
		}},
	}

	payload, err := json.Marshal(req)
	if err != nil {
		return fmt.Errorf("marshalling OTLP request: %w", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, cfg.OTLPUrl+"/v1/logs", bytes.NewReader(payload))
	if err != nil {
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
		return fmt.Errorf("sending OTLP request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 300 {
		return fmt.Errorf("OTLP endpoint returned %s", resp.Status)
	}

	return nil
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
}

// attrKeyMap maps event field names to OTLP semantic convention attribute keys.
var attrKeyMap = map[string]string{
	"session_id":    "gen_ai.conversation.id",
	"cwd":           "process.working_directory",
	"model":         "gen_ai.request.model",
	"tool_name":     "gen_ai.tool.name",
	"tool_input":    "gen_ai.tool.call.arguments",
	"tool_response": "gen_ai.tool.call.result",
	"tool_use_id":   "gen_ai.tool.call.id",
	"error":         "exception.message",
	"agent_id":      "gen_ai.agent.id",
	"agent_type":    "gen_ai.agent.name",
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
func eventAttributes(event map[string]any) []Attribute {
	var attrs []Attribute
	for k, v := range event {
		if attrSkipKeys[k] {
			continue
		}
		if t, ok := attrTransformMap[k]; ok {
			s := t.transform(v)
			if s != "" {
				attrs = append(attrs, Attribute{Key: t.key, Value: StringVal(s)})
			}
			continue
		}
		s := stringifyValue(v)
		if s != "" {
			key := k
			if mapped, ok := attrKeyMap[k]; ok {
				key = mapped
			}
			attrs = append(attrs, Attribute{Key: key, Value: StringVal(s)})
		}
	}
	return attrs
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

func vcsResourceAttributes() []Attribute {
	info := vcs.Detect()
	if info == nil {
		return nil
	}

	attr := func(key, val string) Attribute {
		return Attribute{Key: key, Value: StringVal(val)}
	}

	var attrs []Attribute
	if info.RepositoryURLFull != "" {
		attrs = append(attrs, attr("vcs.repository.url.full", info.RepositoryURLFull))
	}
	if info.RepositoryName != "" {
		attrs = append(attrs, attr("vcs.repository.name", info.RepositoryName))
	}
	if info.OwnerName != "" {
		attrs = append(attrs, attr("vcs.owner.name", info.OwnerName))
	}
	if info.ProviderName != "" {
		attrs = append(attrs, attr("vcs.provider.name", info.ProviderName))
	}
	if info.RefHeadName != "" {
		attrs = append(attrs, attr("vcs.ref.head.name", info.RefHeadName))
	}
	if info.RefHeadRevision != "" {
		attrs = append(attrs, attr("vcs.ref.head.revision", info.RefHeadRevision))
	}
	if info.RefHeadType != "" {
		attrs = append(attrs, attr("vcs.ref.head.type", info.RefHeadType))
	}
	if info.UserName != "" {
		attrs = append(attrs, attr("user.name", info.UserName))
	}
	if info.UserEmail != "" {
		attrs = append(attrs, attr("user.email", info.UserEmail))
	}

	return attrs
}
