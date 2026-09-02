// SPDX-FileCopyrightText: Copyright 2026 Dash0 Inc.
// SPDX-License-Identifier: Apache-2.0

package copilot

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/dash0hq/dash0-agent-plugin/internal/otlp"
	"github.com/dash0hq/dash0-agent-plugin/internal/pipeline"
)

const turnConvID = "aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee"

// capture records the OTLP bodies the pipeline exports.
type capture struct {
	mu     sync.Mutex
	bodies [][]byte
}

func newCapture(t *testing.T) (*capture, otlp.Config) {
	t.Helper()
	c := &capture{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		c.mu.Lock()
		c.bodies = append(c.bodies, b)
		c.mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)

	cfg := otlp.Config{
		OTLPUrl:      srv.URL,
		AuthToken:    "turn-test-token",
		Dataset:      "test",
		AgentName:    "copilot",
		HarnessName:  "github-copilot-cli",
		OmitUserInfo: true,
		OmitIO:       false,
	}
	require.True(t, cfg.ValidateURL())
	return c, cfg
}

func (c *capture) spans(t *testing.T) []otlp.Span {
	t.Helper()
	c.mu.Lock()
	defer c.mu.Unlock()

	var spans []otlp.Span
	for _, b := range c.bodies {
		var req otlp.ExportTracesRequest
		if err := json.Unmarshal(b, &req); err != nil {
			continue
		}
		for _, rs := range req.ResourceSpans {
			for _, ss := range rs.ScopeSpans {
				spans = append(spans, ss.Spans...)
			}
		}
	}
	return spans
}

// feed drives one event the way cmd/copilot-on-event/main.go does: normalize,
// then recover the turn either side of pipeline.Process. The order is the thing
// under test, because the trace context must be read before Process clears it on
// Stop, so this mirrors run() rather than calling into it.
func feedEvent(t *testing.T, eventName, payload, dataDir string, cfg otlp.Config) {
	t.Helper()

	var event map[string]any
	require.NoError(t, json.Unmarshal([]byte(payload), &event))

	event = Normalize(eventName, event)
	if event == nil {
		return
	}

	sessionID, _ := event["session_id"].(string)
	hookEvent, _ := event["hook_event_name"].(string)

	// The marker run() writes on SessionStart. Without it a Stop that recovers no
	// turn is read as a sub-agent's and suppressed, which is a different code path
	// from the one under test here.
	if hookEvent == "SessionStart" {
		MarkSessionStarted(dataDir, sessionID)
	}

	var turn *Turn
	var ctx *otlp.TraceContext
	var cursor, turnSession string
	if hookEvent == "Stop" {
		turn, cursor = PrepareTurn(event, sessionID)
		if turn != nil {
			turnSession = sessionID
		}
		ctx, _ = otlp.LoadTraceContext(pipeline.SessionDir(dataDir, sessionID))
	}

	_, err := pipeline.Process(event, cfg, dataDir, time.Now().UTC())
	require.NoError(t, err)

	EmitTurn(turn, cursor, turnSession, ctx, cfg)
}

// stageTurn writes a realistic native-OTel file for one turn: an invoke_agent
// root, a chat span with usage and response, a top-level bash tool lasting 0.5s,
// and a task spawn whose sub-agent runs its own bash under an invoke_agent-task
// layer the reader must collapse.
func stageTurn(t *testing.T, dir, conv string) {
	t.Helper()
	const trace = "11111111111111111111111111111111"
	writeLines(t, filepath.Join(dir, "otel.jsonl"),
		nativeSpanLine(t, trace, "aaaaaaaaaaaaaa01", "aaaaaaaaaaaaaa00", "chat gpt-5.3-codex", 1000, 1001, 0, map[string]any{
			"gen_ai.conversation.id":               conv,
			"gen_ai.request.model":                 "gpt-5.3-codex",
			"gen_ai.usage.input_tokens":            14613,
			"gen_ai.usage.output_tokens":           68,
			"gen_ai.usage.cache_read.input_tokens": 14592,
			"github.copilot.cost":                  1.0,
			"gen_ai.output.messages":               `[{"role":"assistant","parts":[{"type":"text","content":"Echo complete."}]}]`,
		}),
		nativeSpanLine(t, trace, "aaaaaaaaaaaaaa02", "aaaaaaaaaaaaaa00", "execute_tool bash", 1001, 1001.5, 0, map[string]any{
			"gen_ai.tool.name":           "bash",
			"gen_ai.tool.call.id":        "call_top",
			"gen_ai.tool.call.arguments": `{"command":"echo hi"}`,
			"gen_ai.tool.call.result":    "hi",
		}),
		nativeSpanLine(t, trace, "aaaaaaaaaaaaaa05", "aaaaaaaaaaaaaa04", "execute_tool bash", 1002, 1002.25, 0, map[string]any{
			"gen_ai.tool.name":           "bash",
			"gen_ai.tool.call.id":        "call_sub",
			"gen_ai.tool.call.arguments": `{"command":"echo hello"}`,
			"gen_ai.tool.call.result":    "hello",
		}),
		nativeSpanLine(t, trace, "aaaaaaaaaaaaaa04", "aaaaaaaaaaaaaa03", "invoke_agent task", 1001.6, 1003, 0, map[string]any{
			"gen_ai.conversation.id": conv,
			"gen_ai.agent.name":      "task",
		}),
		nativeSpanLine(t, trace, "aaaaaaaaaaaaaa03", "aaaaaaaaaaaaaa00", "execute_tool task", 1001.6, 1003.1, 0, map[string]any{
			"gen_ai.tool.name":           "task",
			"gen_ai.tool.call.id":        "call_spawn",
			"gen_ai.tool.call.arguments": `{"agent_type":"task","name":"echo-runner"}`,
			"gen_ai.tool.call.result":    "done",
		}),
		nativeSpanLine(t, trace, "aaaaaaaaaaaaaa00", "", "invoke_agent", 1000, 1004, 0, map[string]any{
			"gen_ai.conversation.id": conv,
		}),
	)
}

func sessionEvent(fields string) string {
	return `{"sessionId":"` + turnConvID + `"` + fields + `}`
}

// TestEmitTurn_SpanTree drives a whole turn and asserts the spans it produces: a
// chat span carrying the per-turn tokens and the response, and OTel-sourced
// spans with real durations reproducing the native tree:
//
//	chat → execute_tool task → invoke_agent task → execute_tool bash
//
// with the top-level tool directly under the chat span.
func TestEmitTurn_SpanTree(t *testing.T) {
	otelDir := t.TempDir()
	t.Setenv("DASH0_COPILOT_OTEL_DIR", otelDir)
	stageTurn(t, otelDir, turnConvID)

	cap, cfg := newCapture(t)
	dataDir := t.TempDir()

	// strconv.Quote, not a bare interpolation: on Windows t.TempDir() is
	// "C:\Users\...", and \U is not a valid JSON escape, so the event would be
	// rejected before any of this is exercised.
	feedEvent(t, "sessionStart", sessionEvent(`,"cwd":`+strconv.Quote(t.TempDir())+`,"source":"new"`), dataDir, cfg)
	feedEvent(t, "userPromptSubmitted", sessionEvent(`,"prompt":"run echo hi"`), dataDir, cfg)
	feedEvent(t, "agentStop", sessionEvent(`,"stopReason":"end_turn"`), dataDir, cfg)

	spans := cap.spans(t)
	require.NotEmpty(t, spans)

	var chatSpanID string
	var chatWithUsage, chatWithResponse bool
	tools := map[string]otlp.Span{}  // by native span id
	agents := map[string]otlp.Span{} // likewise
	for _, s := range spans {
		switch {
		case strings.HasPrefix(s.Name, "chat"):
			chatSpanID = s.SpanID
			for _, a := range s.Attributes {
				if a.Key == "gen_ai.usage.input_tokens" && a.Value.IntValue != nil && *a.Value.IntValue == "14613" {
					chatWithUsage = true
				}
				if a.Key == "gen_ai.output.messages" && a.Value.StringValue != nil &&
					strings.Contains(*a.Value.StringValue, "Echo complete.") {
					chatWithResponse = true
				}
			}
		case strings.HasPrefix(s.Name, "execute_tool"):
			tools[s.SpanID] = s
		case strings.HasPrefix(s.Name, "invoke_agent"):
			agents[s.SpanID] = s
		}
	}
	assert.True(t, chatWithUsage, "the chat span must carry the per-turn tokens from the native-OTel file")
	assert.True(t, chatWithResponse, "the chat span must carry the agent response from the native-OTel file")

	require.Len(t, tools, 3, "every execute_tool span must be emitted from the native-OTel file")

	topBash, ok := tools["aaaaaaaaaaaaaa02"]
	require.True(t, ok, "the top-level bash keeps its native span id")
	assert.Equal(t, chatSpanID, topBash.ParentSpanID, "a top-level tool parents under the turn's chat span")
	assert.NotEqual(t, topBash.StartTimeUnixNano, topBash.EndTimeUnixNano,
		"tool spans carry the real duration, not a zero-length instant")

	task, ok := tools["aaaaaaaaaaaaaa03"]
	require.True(t, ok, "the task spawn is emitted")
	assert.Equal(t, chatSpanID, task.ParentSpanID)

	// The sub-agent gets a span of its own rather than collapsing into the tool
	// that spawned it, and gen_ai.agent.id is the spawning call id rather than the
	// type-wide "builtin:task".
	require.Len(t, agents, 1, "the turn's own root invoke_agent is collapsed; the sub-agent's is not")
	agent, ok := agents["aaaaaaaaaaaaaa04"]
	require.True(t, ok, "the sub-agent keeps its native span id")
	assert.Equal(t, "aaaaaaaaaaaaaa03", agent.ParentSpanID,
		"a sub-agent nests under the task tool that spawned it")
	assert.Equal(t, "call_spawn", spanStringAttr(agent, "gen_ai.agent.id"),
		"the sub-agent is identified by the spawning call, not by its kind")

	subBash, ok := tools["aaaaaaaaaaaaaa05"]
	require.True(t, ok, "the sub-agent's tool is emitted")
	assert.Equal(t, "aaaaaaaaaaaaaa04", subBash.ParentSpanID,
		"a sub-agent's tool nests under its invoke_agent span")
}

// TestEmitTurn_DefersWhenTraceContextMissing guards the invariant EmitTurn exists
// for: a Stop without an intact trace context must DEFER the turn, not consume
// it. No spans emit and the cursor stays put, so the usage and tools fold into
// the next valid turn instead of being dropped.
func TestEmitTurn_DefersWhenTraceContextMissing(t *testing.T) {
	otelDir := t.TempDir()
	t.Setenv("DASH0_COPILOT_OTEL_DIR", otelDir)
	stageTurn(t, otelDir, turnConvID)

	cap, cfg := newCapture(t)
	dataDir := t.TempDir()
	cursorFile := filepath.Join(otelDir, "cursor-"+turnConvID+".json")

	countKinds := func() (chat, tools int) {
		for _, s := range cap.spans(t) {
			switch {
			case strings.HasPrefix(s.Name, "chat"):
				chat++
			case strings.HasPrefix(s.Name, "execute_tool"):
				tools++
			}
		}
		return chat, tools
	}

	// sessionStart records only the SessionID, minting no TraceID, so a Stop now
	// has no intact context and the turn must be deferred.
	feedEvent(t, "sessionStart", sessionEvent(`,"cwd":`+strconv.Quote(t.TempDir())+`,"source":"new"`), dataDir, cfg)
	feedEvent(t, "agentStop", sessionEvent(`,"stopReason":"end_turn"`), dataDir, cfg)

	chat, tools := countKinds()
	assert.Zero(t, chat, "a Stop without trace context emits no chat span")
	assert.Zero(t, tools, "a Stop without trace context emits no tool spans")
	require.NoFileExists(t, cursorFile, "the cursor must not advance for a deferred turn")

	// A normal turn mints the trace, re-reads the SAME native-OTel spans because
	// the cursor never moved, and emits them now.
	feedEvent(t, "userPromptSubmitted", sessionEvent(`,"prompt":"run echo hi"`), dataDir, cfg)
	feedEvent(t, "agentStop", sessionEvent(`,"stopReason":"end_turn"`), dataDir, cfg)

	chat, tools = countKinds()
	assert.Equal(t, 1, chat, "the deferred turn's chat span emits on the next valid turn")
	assert.Equal(t, 3, tools, "the deferred turn's tool spans fold into the next valid turn")
	assert.FileExists(t, cursorFile, "the cursor advances once the turn is actually emitted")
}

func spanStringAttr(s otlp.Span, key string) string {
	for _, a := range s.Attributes {
		if a.Key == key && a.Value.StringValue != nil {
			return *a.Value.StringValue
		}
	}
	return ""
}
