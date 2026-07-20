// SPDX-FileCopyrightText: Copyright 2026 Dash0 Inc.
// SPDX-License-Identifier: Apache-2.0

package copilot

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func parse(t *testing.T, raw string) map[string]any {
	t.Helper()
	var m map[string]any
	require.NoError(t, json.Unmarshal([]byte(raw), &m))
	return m
}

func TestNormalize_agentStopToStop(t *testing.T) {
	// Real camelCase agentStop payload (no hook_event_name; transcriptPath present).
	e := Normalize("agentStop", parse(t,
		`{"sessionId":"conv-1","timestamp":123,"cwd":"/x","transcriptPath":"/y/events.jsonl","stopReason":"end_turn"}`))
	require.NotNil(t, e)
	assert.Equal(t, "Stop", e["hook_event_name"])
	assert.Equal(t, "conv-1", e["session_id"])
	_, hasTP := e["transcript_path"]
	assert.False(t, hasTP, "transcriptPath must be dropped so the Claude transcript reader never runs")
	_, hasRaw := e["transcriptPath"]
	assert.False(t, hasRaw)
}

func TestNormalize_userPromptAndSession(t *testing.T) {
	up := Normalize("userPromptSubmitted", parse(t, `{"sessionId":"c","prompt":"do a thing"}`))
	require.NotNil(t, up)
	assert.Equal(t, "UserPromptSubmit", up["hook_event_name"])
	assert.Equal(t, "do a thing", up["prompt"])

	ss := Normalize("sessionStart", parse(t, `{"sessionId":"c","source":"new","initialPrompt":"secret"}`))
	require.NotNil(t, ss)
	assert.Equal(t, "SessionStart", ss["hook_event_name"])
	_, hasIP := ss["initialPrompt"]
	assert.False(t, hasIP, "initialPrompt (user content) is dropped")
}

func TestNormalize_dropsUnconsumedEvents(t *testing.T) {
	// postToolUse/postToolUseFailure are deliberately unconsumed: tool spans come
	// from the native-OTel file (real durations, sub-agent nesting), not hooks.
	for _, name := range []string{"preToolUse", "postToolUse", "postToolUseFailure", "subagentStop", "subagentStart", "notification", "preCompact", "permissionRequest", "errorOccurred"} {
		assert.Nil(t, Normalize(name, parse(t, `{"sessionId":"c"}`)), "%s must be dropped", name)
	}
}

func TestNormalize_dropsSubAgentSessions(t *testing.T) {
	// Sub-agent turns run under a synthetic "call_<toolCallId>" session id with no
	// link to the parent, so every sub-agent lifecycle event is dropped — otherwise
	// each mints a spurious, token-less conversation.
	assert.Nil(t, Normalize("userPromptSubmitted", parse(t, `{"sessionId":"call_s6uW2cBFL6xsNgNWRM66Zx1o","prompt":"echo hello"}`)))
	assert.Nil(t, Normalize("agentStop", parse(t, `{"sessionId":"call_abc","stopReason":"end_turn"}`)))

	// A real conversation (UUID session) is unaffected.
	out := Normalize("agentStop", parse(t, `{"sessionId":"bd34642e-4962-4930-bb77-fb1b00db2c00","stopReason":"end_turn"}`))
	require.NotNil(t, out)
	assert.Equal(t, "Stop", out["hook_event_name"])
	assert.Equal(t, "bd34642e-4962-4930-bb77-fb1b00db2c00", out["session_id"])
}

func TestNormalize_nilEventDoesNotPanic(t *testing.T) {
	// A JSON `null` payload decodes to a nil map; Normalize must return nil, not
	// panic — the process is required to stay fail-open (exit 0).
	assert.NotPanics(t, func() {
		assert.Nil(t, Normalize("agentStop", nil))
	})
}

// --- native-OTel file reader ---

func chatSpanLine(spanID, conv string, in, out, cacheRead, reasoning int, cost float64, model string) string {
	return fmt.Sprintf(`{"type":"span","spanId":%q,"name":"chat %s","attributes":{"gen_ai.conversation.id":%q,"gen_ai.request.model":%q,"gen_ai.usage.input_tokens":%d,"gen_ai.usage.output_tokens":%d,"gen_ai.usage.cache_read.input_tokens":%d,"gen_ai.usage.reasoning.output_tokens":%d,"github.copilot.cost":%g}}`,
		spanID, model, conv, model, in, out, cacheRead, reasoning, cost)
}

func TestReadTurn_perTurnCursor(t *testing.T) {
	otelDir := t.TempDir()
	t.Setenv("DASH0_COPILOT_OTEL_DIR", otelDir)
	sessionDir := t.TempDir()
	f := filepath.Join(otelDir, "otel-1.jsonl")

	// Turn 1: one chat span.
	writeLines(t, f, chatSpanLine("s1", "conv-1", 100, 20, 90, 5, 1.0, "gpt-5.3-codex"))
	t1, c1 := ReadTurn("conv-1", sessionDir)
	require.NotNil(t, t1)
	require.NotNil(t, t1.Usage)
	assert.Equal(t, int64(100), t1.Usage.InputTokens)
	assert.Equal(t, int64(20), t1.Usage.OutputTokens)
	assert.Equal(t, int64(90), t1.Usage.CacheReadInputTokens)
	assert.Equal(t, int64(5), t1.Usage.ReasoningOutputTokens)
	assert.Equal(t, "gpt-5.3-codex", t1.Usage.Model)
	assert.Equal(t, "s1", c1)
	SaveCursor(sessionDir, c1)

	// Turn 2: append a second span; the reader returns ONLY turn 2.
	appendLines(t, f, chatSpanLine("s2", "conv-1", 200, 30, 150, 0, 2.0, "gpt-5.3-codex"))
	t2, c2 := ReadTurn("conv-1", sessionDir)
	require.NotNil(t, t2)
	require.NotNil(t, t2.Usage)
	assert.Equal(t, int64(200), t2.Usage.InputTokens, "must not double-count turn 1")
	assert.Equal(t, int64(30), t2.Usage.OutputTokens)
	assert.Equal(t, "s2", c2)
	SaveCursor(sessionDir, c2)

	// Re-run with no new spans → nil (idempotent, no double-count).
	t3, c3 := ReadTurn("conv-1", sessionDir)
	assert.Nil(t, t3)
	assert.Empty(t, c3)
}

func TestReadTurn_subAgentRollup(t *testing.T) {
	otelDir := t.TempDir()
	t.Setenv("DASH0_COPILOT_OTEL_DIR", otelDir)
	sessionDir := t.TempDir()
	// A turn with a main chat span + two sub-agent chat spans (same conversation.id).
	writeLines(t, filepath.Join(otelDir, "otel.jsonl"),
		chatSpanLine("s1", "conv-1", 100, 20, 0, 0, 1.0, "gpt"),
		chatSpanLine("s2", "conv-1", 50, 10, 0, 0, 0.5, "gpt"),
		chatSpanLine("s3", "conv-1", 40, 8, 0, 0, 0.5, "gpt"))
	turn, c := ReadTurn("conv-1", sessionDir)
	require.NotNil(t, turn)
	require.NotNil(t, turn.Usage)
	assert.Equal(t, int64(190), turn.Usage.InputTokens, "sub-agent input tokens roll into the turn total")
	assert.Equal(t, int64(38), turn.Usage.OutputTokens, "sub-agent output tokens roll into the turn total")
	assert.InDelta(t, 2.0, turn.Usage.Cost, 0.001)
	assert.Equal(t, "s3", c, "cursor is the last consumed span")
}

// TestReadTurn_resumeRotatedFile is the core cross-launch case: a resumed
// session writes a NEW file (newer mtime) with disjoint span ids. The reader
// must prefer the newest file and, finding the old cursor absent from it, treat
// all its spans as fresh — so the recovered session still reports per-turn usage.
func TestReadTurn_resumeRotatedFile(t *testing.T) {
	otelDir := t.TempDir()
	t.Setenv("DASH0_COPILOT_OTEL_DIR", otelDir)
	sessionDir := t.TempDir()

	// Launch 1.
	fileA := filepath.Join(otelDir, "otel-A.jsonl")
	writeLines(t, fileA, chatSpanLine("a1", "conv-1", 100, 20, 0, 0, 1, "gpt"))
	_, c1 := ReadTurn("conv-1", sessionDir)
	SaveCursor(sessionDir, c1) // cursor = "a1"

	// Launch 2 (resume): brand-new file, disjoint ids, made newer than A.
	fileB := filepath.Join(otelDir, "otel-B.jsonl")
	writeLines(t, fileB, chatSpanLine("b1", "conv-1", 300, 40, 0, 0, 3, "gpt"))
	older := time.Now().Add(-time.Hour)
	require.NoError(t, os.Chtimes(fileA, older, older))

	turn, c := ReadTurn("conv-1", sessionDir)
	require.NotNil(t, turn, "resumed session must still get per-turn usage")
	require.NotNil(t, turn.Usage)
	assert.Equal(t, int64(300), turn.Usage.InputTokens)
	assert.Equal(t, "b1", c)
}

func TestReadTurn_fileDiscoveryByConversationID(t *testing.T) {
	otelDir := t.TempDir()
	t.Setenv("DASH0_COPILOT_OTEL_DIR", otelDir)
	sessionDir := t.TempDir()
	// Two concurrent sessions' files; the reader must pick ours by conversation.id.
	writeLines(t, filepath.Join(otelDir, "other.jsonl"), chatSpanLine("o1", "conv-OTHER", 999, 999, 0, 0, 9, "gpt"))
	writeLines(t, filepath.Join(otelDir, "ours.jsonl"), chatSpanLine("m1", "conv-MINE", 100, 20, 0, 0, 1, "gpt"))
	turn, _ := ReadTurn("conv-MINE", sessionDir)
	require.NotNil(t, turn)
	require.NotNil(t, turn.Usage)
	assert.Equal(t, int64(100), turn.Usage.InputTokens)
}

func TestReadTurn_absentGraceful(t *testing.T) {
	t.Setenv("DASH0_COPILOT_OTEL_DIR", t.TempDir()) // empty dir
	t1, c1 := ReadTurn("conv-1", t.TempDir())
	assert.Nil(t, t1)
	assert.Empty(t, c1)
	t2, _ := ReadTurn("", t.TempDir())
	assert.Nil(t, t2)
}

// chatSpanWithOutput builds a chat-span line carrying gen_ai.output.messages
// (whose value is itself a JSON string), so it survives json round-tripping.
func chatSpanWithOutput(t *testing.T, spanID, conv, outputMessages string) string {
	t.Helper()
	line, err := json.Marshal(map[string]any{
		"type":   "span",
		"spanId": spanID,
		"name":   "chat gpt",
		"attributes": map[string]any{
			"gen_ai.conversation.id":    conv,
			"gen_ai.request.model":      "gpt",
			"gen_ai.usage.input_tokens": 10,
			"gen_ai.output.messages":    outputMessages,
		},
	})
	require.NoError(t, err)
	return string(line)
}

// ReadTurn recovers the turn's final assistant text from the chat span's
// gen_ai.output.messages so the pipeline can render gen_ai.output.messages (the
// agent response) — Copilot's agentStop payload carries no response text.
func TestReadTurn_responseText(t *testing.T) {
	otelDir := t.TempDir()
	t.Setenv("DASH0_COPILOT_OTEL_DIR", otelDir)
	sessionDir := t.TempDir()
	f := filepath.Join(otelDir, "otel.jsonl")

	// Two spans in the turn: the first ends in a tool call, the second in text.
	// The recovered response is the LAST non-empty assistant text of the turn.
	writeLines(t, f,
		chatSpanWithOutput(t, "s1", "conv-1",
			`[{"role":"assistant","parts":[{"type":"tool_call","content":"echo"}]}]`),
		chatSpanWithOutput(t, "s2", "conv-1",
			`[{"role":"assistant","parts":[{"type":"text","content":"All done."}],"finish_reason":"stop"}]`))

	turn, _ := ReadTurn("conv-1", sessionDir)
	require.NotNil(t, turn)
	require.NotNil(t, turn.Usage)
	assert.Equal(t, "All done.", turn.Usage.ResponseText)
}

func TestAssistantTextFromOutput(t *testing.T) {
	// Multiple text parts of one message join with newlines; the LAST assistant
	// message wins; non-assistant roles and non-text parts are ignored.
	assert.Equal(t, "Hi", assistantTextFromOutput(
		`[{"role":"assistant","parts":[{"type":"text","content":"Hi"}]}]`))
	assert.Equal(t, "final", assistantTextFromOutput(
		`[{"role":"assistant","parts":[{"type":"text","content":"first"}]},{"role":"assistant","parts":[{"type":"text","content":"final"}]}]`))
	assert.Equal(t, "a\nb", assistantTextFromOutput(
		`[{"role":"assistant","parts":[{"type":"text","content":"a"},{"type":"text","content":"b"}]}]`))
	assert.Equal(t, "keep", assistantTextFromOutput(
		`[{"role":"assistant","parts":[{"type":"text","content":"keep"}]},{"role":"assistant","parts":[{"type":"tool_call","content":"x"}]}]`),
		"a trailing tool-only message must not blank the earlier text")
	assert.Empty(t, assistantTextFromOutput(
		`[{"role":"user","parts":[{"type":"text","content":"prompt"}]}]`), "user role ignored")
	assert.Empty(t, assistantTextFromOutput(""))
	assert.Empty(t, assistantTextFromOutput("not json"))
}

func TestSweepOldOtelFiles(t *testing.T) {
	otelDir := t.TempDir()
	t.Setenv("DASH0_COPILOT_OTEL_DIR", otelDir)
	oldF := filepath.Join(otelDir, "otel-old.jsonl")
	freshF := filepath.Join(otelDir, "otel-fresh.jsonl")
	writeLines(t, oldF, chatSpanLine("x", "c", 1, 1, 0, 0, 0, "m"))
	writeLines(t, freshF, chatSpanLine("y", "c", 1, 1, 0, 0, 0, "m"))
	old := time.Now().Add(-48 * time.Hour)
	require.NoError(t, os.Chtimes(oldF, old, old))

	SweepOldOtelFiles(time.Now())
	assert.NoFileExists(t, oldF, "stale file (>24h) should be swept")
	assert.FileExists(t, freshF, "recent file should be kept")
}

// nativeSpanLine builds a full native-OTel span record in the file-exporter
// format: top-level traceId/spanId/parentSpanId/name, [sec,nsec] timestamps,
// and a status object — the shape execute_tool recovery depends on.
func nativeSpanLine(t *testing.T, traceID, spanID, parentID, name string, startSec, endSec float64, statusCode int, attrs map[string]any) string {
	t.Helper()
	rec := map[string]any{
		"type":         "span",
		"traceId":      traceID,
		"spanId":       spanID,
		"parentSpanId": parentID,
		"name":         name,
		"startTime":    []any{int64(startSec), int64((startSec - float64(int64(startSec))) * 1e9)},
		"endTime":      []any{int64(endSec), int64((endSec - float64(int64(endSec))) * 1e9)},
		"status":       map[string]any{"code": statusCode},
		"attributes":   attrs,
	}
	line, err := json.Marshal(rec)
	require.NoError(t, err)
	return string(line)
}

// TestReadTurn_toolCalls covers the OTel-sourced tool recovery: execute_tool
// spans (which carry NO conversation.id — membership goes via the shared
// traceId) come back with real timings and failure status, and parents collapse
// the invoke_agent layers — a sub-agent's tool nests under its spawning `task`
// span, top-level tools resolve to "" (→ the caller's chat span).
func TestReadTurn_toolCalls(t *testing.T) {
	otelDir := t.TempDir()
	t.Setenv("DASH0_COPILOT_OTEL_DIR", otelDir)
	sessionDir := t.TempDir()

	// Mirrors a real capture:
	//   invoke_agent (conv-1)
	//     chat gpt
	//     execute_tool bash            (top-level, 0.5s, ok)
	//     execute_tool task (call_X)
	//       invoke_agent task (conv-1)
	//         execute_tool bash        (sub-agent, failed)
	writeLines(t, filepath.Join(otelDir, "otel.jsonl"),
		nativeSpanLine(t, "t1", "ch1", "ia1", "chat gpt", 100, 101, 0, map[string]any{
			"gen_ai.conversation.id": "conv-1", "gen_ai.request.model": "gpt",
			"gen_ai.usage.input_tokens": 100, "gen_ai.usage.output_tokens": 10,
		}),
		nativeSpanLine(t, "t1", "e1", "ia1", "execute_tool bash", 101, 101.5, 0, map[string]any{
			"gen_ai.tool.name": "bash", "gen_ai.tool.call.id": "call_A",
			"gen_ai.tool.call.arguments": `{"command":"echo one"}`, "gen_ai.tool.call.result": "one",
		}),
		nativeSpanLine(t, "t1", "e3", "ia2", "execute_tool bash", 102, 102.25, 2, map[string]any{
			"gen_ai.tool.name": "bash", "gen_ai.tool.call.id": "call_B",
			"gen_ai.tool.call.arguments": `{"command":"false"}`, "gen_ai.tool.call.result": "exit 1",
		}),
		nativeSpanLine(t, "t1", "ia2", "e2", "invoke_agent task", 101.5, 103, 0, map[string]any{
			"gen_ai.conversation.id": "conv-1",
		}),
		nativeSpanLine(t, "t1", "e2", "ia1", "execute_tool task", 101.5, 103.5, 0, map[string]any{
			"gen_ai.tool.name": "task", "gen_ai.tool.call.id": "call_X",
			"gen_ai.tool.call.arguments": `{"agent_type":"task","name":"echo-runner"}`,
			"gen_ai.tool.call.result":    "done",
		}),
		nativeSpanLine(t, "t1", "ia1", "", "invoke_agent", 100, 104, 0, map[string]any{
			"gen_ai.conversation.id": "conv-1",
		}),
	)

	turn, c := ReadTurn("conv-1", sessionDir)
	require.NotNil(t, turn)
	require.NotNil(t, turn.Usage, "chat span still summed for usage")
	assert.Equal(t, int64(100), turn.Usage.InputTokens)
	require.Len(t, turn.Tools, 3, "every execute_tool span recovered — invoke_agent/chat layers are not tools")
	byID := map[string]ToolCall{}
	for _, tc := range turn.Tools {
		byID[tc.SpanID] = tc
	}

	top := byID["e1"]
	assert.Equal(t, "bash", top.Name)
	assert.Equal(t, "call_A", top.CallID)
	assert.Empty(t, top.ParentSpanID, "top-level tool resolves to the chat span (empty here)")
	assert.Equal(t, 500*time.Millisecond, top.End.Sub(top.Start), "real duration from native timestamps")
	assert.False(t, top.Failed)

	task := byID["e2"]
	assert.Equal(t, "task", task.Name)
	assert.Equal(t, "call_X", task.CallID)
	assert.Empty(t, task.ParentSpanID)

	sub := byID["e3"]
	assert.Equal(t, "e2", sub.ParentSpanID, "sub-agent tool nests under its spawning task span (invoke_agent layer collapsed)")
	assert.True(t, sub.Failed, "native status code 2 marks the tool failed")

	// The cursor covers ALL consumed spans (tools included): after persisting it,
	// a re-read finds nothing new.
	SaveCursor(sessionDir, c)
	again, _ := ReadTurn("conv-1", sessionDir)
	assert.Nil(t, again, "re-run after SaveCursor must not re-emit tools or re-count usage")
}

func writeLines(t *testing.T, path string, lines ...string) {
	t.Helper()
	data := ""
	for _, l := range lines {
		data += l + "\n"
	}
	require.NoError(t, os.WriteFile(path, []byte(data), 0o644))
}

func appendLines(t *testing.T, path string, lines ...string) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
	require.NoError(t, err)
	defer f.Close()
	for _, l := range lines {
		_, err := f.WriteString(l + "\n")
		require.NoError(t, err)
	}
}
