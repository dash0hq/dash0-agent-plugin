package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/dash0hq/dash0-agent-plugin/internal/dotenv"
	"github.com/dash0hq/dash0-agent-plugin/internal/filelog"
	"github.com/dash0hq/dash0-agent-plugin/internal/otlp"
	"github.com/dash0hq/dash0-agent-plugin/internal/transcript"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "on-event: %v\n", err)
		os.Exit(1)
	}
}

func sendSessionTrace(event map[string]any, cfg otlp.Config, ts time.Time, dataDir string) error {
	sessionID, _ := event["session_id"].(string)
	model, _ := event["model"].(string)

	var traceID, spanID string
	if sessionID != "" {
		traceID = otlp.TraceIDFromSessionID(sessionID)
		spanID = otlp.SpanIDFromSessionID(sessionID)
	} else {
		var err error
		traceID, err = otlp.GenerateTraceID()
		if err != nil {
			return err
		}
		spanID, err = otlp.GenerateSpanID()
		if err != nil {
			return err
		}
	}

	span := otlp.NewSessionSpan(traceID, spanID, ts, event, cfg)
	if err := otlp.SendTrace(span, event, cfg); err != nil {
		return err
	}

	return otlp.SaveTraceContext(otlp.TraceContext{
		TraceID:   traceID,
		SpanID:    spanID,
		SessionID: sessionID,
		Model:     model,
	}, dataDir)
}

func sendToolTrace(event map[string]any, cfg otlp.Config, ts time.Time, dataDir string, failed bool) error {
	sessionID, _ := event["session_id"].(string)

	var traceID, parentSpanID, model string
	if sessionID != "" {
		traceID = otlp.TraceIDFromSessionID(sessionID)
		parentSpanID = otlp.SpanIDFromSessionID(sessionID)
	}

	// Always try to load trace context for model and as fallback for IDs.
	ctx, err := otlp.LoadTraceContext(dataDir)
	if err == nil && ctx != nil {
		model = ctx.Model
		if traceID == "" {
			traceID = ctx.TraceID
			parentSpanID = ctx.SpanID
		}
	}
	if traceID == "" {
		return fmt.Errorf("no trace context available for tool span")
	}

	// Inject model from session context if the event doesn't have one.
	if _, hasModel := event["model"]; !hasModel && model != "" {
		event["model"] = model
	}

	// Look up matching PreToolUse event to get the start timestamp.
	toolUseID, _ := event["tool_use_id"].(string)
	startTime := ts
	if toolUseID != "" {
		preEvent, _ := filelog.FindEvent(dataDir, func(e map[string]any) bool {
			name, _ := e["hook_event_name"].(string)
			id, _ := e["tool_use_id"].(string)
			return name == "PreToolUse" && id == toolUseID
		})
		if preEvent != nil {
			if raw, ok := preEvent["timestamp"].(string); ok {
				if parsed, parseErr := time.Parse(time.RFC3339Nano, raw); parseErr == nil {
					startTime = parsed
				}
			}
		}
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
			var err error
			spanID, err = otlp.GenerateSpanID()
			if err != nil {
				return err
			}
		}
	} else {
		var err error
		spanID, err = otlp.GenerateSpanID()
		if err != nil {
			return err
		}
	}

	if toolName != "Agent" && agentID != "" {
		// This tool call was made by a sub-agent: nest it under the Agent
		// tool call span.
		parentSpanID = otlp.SpanIDFromAgentID(agentID)
	} else if agentID == "" {
		// Main-agent tool call: nest under the chat span whose ID was
		// stamped onto the most recent UserPromptSubmit event.
		if chatID := lookupChatSpanID(dataDir); chatID != "" {
			parentSpanID = chatID
		}
	}

	span := otlp.NewToolSpan(traceID, spanID, parentSpanID, startTime, ts, event, failed, cfg)
	return otlp.SendTrace(span, event, cfg)
}

func sendLLMTrace(event map[string]any, cfg otlp.Config, ts time.Time, dataDir string, failed bool) error {
	sessionID, _ := event["session_id"].(string)

	var traceID, parentSpanID, model string
	if sessionID != "" {
		traceID = otlp.TraceIDFromSessionID(sessionID)
		parentSpanID = otlp.SpanIDFromSessionID(sessionID)
	}

	// Always try to load trace context for model and as fallback for IDs.
	ctx, err := otlp.LoadTraceContext(dataDir)
	if err == nil && ctx != nil {
		model = ctx.Model
		if traceID == "" {
			traceID = ctx.TraceID
			parentSpanID = ctx.SpanID
		}
	}
	if traceID == "" {
		return fmt.Errorf("no trace context available for LLM span")
	}

	// Inject model from session context if the event doesn't have one.
	if _, hasModel := event["model"]; !hasModel && model != "" {
		event["model"] = model
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
	if agentID != "" {
		parentSpanID = otlp.SpanIDFromAgentID(agentID)
	}

	var spanID string
	if agentID == "" {
		// Main-agent chat: use the chat span ID that was stamped onto the
		// UserPromptSubmit event so tool spans referencing it as parent
		// are correctly nested.
		if chatID := lookupChatSpanID(dataDir); chatID != "" {
			spanID = chatID
		}
	}
	if spanID == "" {
		var err error
		spanID, err = otlp.GenerateSpanID()
		if err != nil {
			return err
		}
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
	}

	span := otlp.NewLLMSpan(traceID, spanID, parentSpanID, startTime, ts, event, failed, cfg)
	return otlp.SendTrace(span, event, cfg)
}

// lookupChatSpanID finds the chat_span_id stamped onto the most recent
// UserPromptSubmit event (for the main agent) in the event log. This avoids
// relying on shared mutable file state that concurrent sessions can stomp on.
func lookupChatSpanID(dataDir string) string {
	evt, _ := filelog.FindEvent(dataDir, func(e map[string]any) bool {
		name, _ := e["hook_event_name"].(string)
		if name != "UserPromptSubmit" {
			return false
		}
		// Sub-agent events carry an agent_id — skip them.
		agentID, _ := e["agent_id"].(string)
		return agentID == ""
	})
	if evt == nil {
		return ""
	}
	id, _ := evt["chat_span_id"].(string)
	return id
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

// envBool returns true when the environment variable is set to "true" or "1".
func envBool(key string) bool {
	v := strings.ToLower(strings.TrimSpace(os.Getenv(key)))
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

	// Stamp a chat_span_id onto main-agent UserPromptSubmit events before
	// they are logged, so that later tool/LLM spans can look it up from the
	// event log without relying on shared mutable file state.
	hookEvent, _ := event["hook_event_name"].(string)
	agentID, _ := event["agent_id"].(string)
	if hookEvent == "UserPromptSubmit" && agentID == "" {
		chatSpanID, err := otlp.GenerateSpanID()
		if err != nil {
			return err
		}
		event["chat_span_id"] = chatSpanID
	}

	if err := filelog.WriteEvent(event, dataDir); err != nil {
		return err
	}

	cfg := otlp.Config{
		OTLPUrl:      os.Getenv("DASH0_OTLP_URL"),
		AuthToken:    os.Getenv("DASH0_AUTH_TOKEN"),
		Dataset:      os.Getenv("DASH0_DATASET"),
		AgentName:    os.Getenv("DASH0_AGENT_NAME"),
		OmitUserInfo: envBool("DASH0_OMIT_USER_INFO"),
		OmitIO:       envBool("DASH0_OMIT_IO"),
		Debug:        envBool("DASH0_DEBUG"),
	}
	if err := otlp.SendLog(event, cfg); err != nil {
		fmt.Fprintf(os.Stderr, "on-event: otlp export: %v\n", err)
	}

	switch hookEvent {
	case "SessionStart":
		if err := sendSessionTrace(event, cfg, now, dataDir); err != nil {
			fmt.Fprintf(os.Stderr, "on-event: trace export: %v\n", err)
		}
	case "PostToolUse", "PostToolUseFailure":
		if err := sendToolTrace(event, cfg, now, dataDir, hookEvent == "PostToolUseFailure"); err != nil {
			fmt.Fprintf(os.Stderr, "on-event: trace export: %v\n", err)
		}
	case "Stop", "StopFailure":
		if err := sendLLMTrace(event, cfg, now, dataDir, hookEvent == "StopFailure"); err != nil {
			fmt.Fprintf(os.Stderr, "on-event: trace export: %v\n", err)
		}
	}

	return nil
}
