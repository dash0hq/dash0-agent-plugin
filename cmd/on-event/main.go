package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
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
	}

	span := otlp.NewLLMSpan(traceID, spanID, parentSpanID, startTime, ts, event, failed, cfg)
	return otlp.SendTrace(span, event, cfg)
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

	// Clean up session directory at SessionEnd.
	if hookEvent == "SessionEnd" {
		os.RemoveAll(sessionDir)
	}

	cfg := otlp.Config{
		OTLPUrl:      os.Getenv("DASH0_OTLP_URL"),
		AuthToken:    os.Getenv("DASH0_AUTH_TOKEN"),
		Dataset:      os.Getenv("DASH0_DATASET"),
		AgentName:    os.Getenv("DASH0_AGENT_NAME"),
		OmitUserInfo: envBool("DASH0_OMIT_USER_INFO"),
		OmitIO:       envBool("DASH0_OMIT_IO"),
		Debug:        envBool("DASH0_DEBUG"),
		DebugFile:    os.Getenv("DASH0_DEBUG_FILE"),
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
	}

	return nil
}
