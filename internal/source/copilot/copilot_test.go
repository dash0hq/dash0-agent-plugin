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

func TestNormalize_postToolUseFields(t *testing.T) {
	e := Normalize("postToolUse", parse(t,
		`{"sessionId":"c","cwd":"/x","toolName":"bash","toolArgs":{"command":"echo hi"},"toolResult":"hi"}`))
	require.NotNil(t, e)
	assert.Equal(t, "PostToolUse", e["hook_event_name"])
	assert.Equal(t, "bash", e["tool_name"])
	assert.NotNil(t, e["tool_input"])
	assert.Equal(t, "hi", e["tool_response"])
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
	for _, name := range []string{"preToolUse", "subagentStop", "subagentStart", "notification", "preCompact", "permissionRequest", "errorOccurred"} {
		assert.Nil(t, Normalize(name, parse(t, `{"sessionId":"c"}`)), "%s must be dropped", name)
	}
}

func TestNormalize_dropsSubAgentSessions(t *testing.T) {
	// Sub-agent turns run under a synthetic "call_<toolCallId>" session id with no
	// link to the parent, so every sub-agent lifecycle event is dropped — otherwise
	// each mints a spurious, token-less conversation.
	assert.Nil(t, Normalize("userPromptSubmitted", parse(t, `{"sessionId":"call_s6uW2cBFL6xsNgNWRM66Zx1o","prompt":"echo hello"}`)))
	assert.Nil(t, Normalize("postToolUse", parse(t, `{"sessionId":"call_abc","toolName":"bash","toolResult":"hello"}`)))
	assert.Nil(t, Normalize("agentStop", parse(t, `{"sessionId":"call_abc","stopReason":"end_turn"}`)))

	// A real conversation (UUID session) is unaffected.
	out := Normalize("postToolUse", parse(t, `{"sessionId":"bd34642e-4962-4930-bb77-fb1b00db2c00","toolName":"bash","toolResult":"hi"}`))
	require.NotNil(t, out)
	assert.Equal(t, "PostToolUse", out["hook_event_name"])
	assert.Equal(t, "bd34642e-4962-4930-bb77-fb1b00db2c00", out["session_id"])
}

func TestNormalize_liftsTaskName(t *testing.T) {
	// The task (sub-agent spawn) tool buries its instance name in a JSON-string
	// toolArgs; the normalizer lifts it to task_name so the tool span is identifiable.
	out := Normalize("postToolUse", parse(t, `{"sessionId":"11111111-1111-1111-1111-111111111111","toolName":"task","toolArgs":"{\"agent_type\":\"task\",\"name\":\"echo-runner\",\"description\":\"Run echo\"}","toolResult":"hello"}`))
	require.NotNil(t, out)
	assert.Equal(t, "echo-runner", out["dash0.gen_ai.tool.task.name"])

	// A non-task tool is untouched.
	b := Normalize("postToolUse", parse(t, `{"sessionId":"11111111-1111-1111-1111-111111111111","toolName":"bash","toolArgs":{"command":"echo hi"},"toolResult":"hi"}`))
	require.NotNil(t, b)
	_, has := b["dash0.gen_ai.tool.task.name"]
	assert.False(t, has, "non-task tools must not get the task name attribute")
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

func TestReadTurnUsage_perTurnCursor(t *testing.T) {
	otelDir := t.TempDir()
	t.Setenv("DASH0_COPILOT_OTEL_DIR", otelDir)
	sessionDir := t.TempDir()
	f := filepath.Join(otelDir, "otel-1.jsonl")

	// Turn 1: one chat span.
	writeLines(t, f, chatSpanLine("s1", "conv-1", 100, 20, 90, 5, 1.0, "gpt-5.3-codex"))
	u1, c1 := ReadTurnUsage("conv-1", sessionDir)
	require.NotNil(t, u1)
	assert.Equal(t, int64(100), u1.InputTokens)
	assert.Equal(t, int64(20), u1.OutputTokens)
	assert.Equal(t, int64(90), u1.CacheReadInputTokens)
	assert.Equal(t, int64(5), u1.ReasoningOutputTokens)
	assert.Equal(t, "gpt-5.3-codex", u1.Model)
	assert.Equal(t, "s1", c1)
	SaveCursor(sessionDir, c1)

	// Turn 2: append a second span; the reader returns ONLY turn 2.
	appendLines(t, f, chatSpanLine("s2", "conv-1", 200, 30, 150, 0, 2.0, "gpt-5.3-codex"))
	u2, c2 := ReadTurnUsage("conv-1", sessionDir)
	require.NotNil(t, u2)
	assert.Equal(t, int64(200), u2.InputTokens, "must not double-count turn 1")
	assert.Equal(t, int64(30), u2.OutputTokens)
	assert.Equal(t, "s2", c2)
	SaveCursor(sessionDir, c2)

	// Re-run with no new spans → nil (idempotent, no double-count).
	u3, c3 := ReadTurnUsage("conv-1", sessionDir)
	assert.Nil(t, u3)
	assert.Empty(t, c3)
}

func TestReadTurnUsage_subAgentRollup(t *testing.T) {
	otelDir := t.TempDir()
	t.Setenv("DASH0_COPILOT_OTEL_DIR", otelDir)
	sessionDir := t.TempDir()
	// A turn with a main chat span + two sub-agent chat spans (same conversation.id).
	writeLines(t, filepath.Join(otelDir, "otel.jsonl"),
		chatSpanLine("s1", "conv-1", 100, 20, 0, 0, 1.0, "gpt"),
		chatSpanLine("s2", "conv-1", 50, 10, 0, 0, 0.5, "gpt"),
		chatSpanLine("s3", "conv-1", 40, 8, 0, 0, 0.5, "gpt"))
	u, c := ReadTurnUsage("conv-1", sessionDir)
	require.NotNil(t, u)
	assert.Equal(t, int64(190), u.InputTokens, "sub-agent input tokens roll into the turn total")
	assert.Equal(t, int64(38), u.OutputTokens, "sub-agent output tokens roll into the turn total")
	assert.InDelta(t, 2.0, u.Cost, 0.001)
	assert.Equal(t, "s3", c, "cursor is the last consumed span")
}

// TestReadTurnUsage_resumeRotatedFile is the core cross-launch case: a resumed
// session writes a NEW file (newer mtime) with disjoint span ids. The reader
// must prefer the newest file and, finding the old cursor absent from it, treat
// all its spans as fresh — so the recovered session still reports per-turn usage.
func TestReadTurnUsage_resumeRotatedFile(t *testing.T) {
	otelDir := t.TempDir()
	t.Setenv("DASH0_COPILOT_OTEL_DIR", otelDir)
	sessionDir := t.TempDir()

	// Launch 1.
	fileA := filepath.Join(otelDir, "otel-A.jsonl")
	writeLines(t, fileA, chatSpanLine("a1", "conv-1", 100, 20, 0, 0, 1, "gpt"))
	_, c1 := ReadTurnUsage("conv-1", sessionDir)
	SaveCursor(sessionDir, c1) // cursor = "a1"

	// Launch 2 (resume): brand-new file, disjoint ids, made newer than A.
	fileB := filepath.Join(otelDir, "otel-B.jsonl")
	writeLines(t, fileB, chatSpanLine("b1", "conv-1", 300, 40, 0, 0, 3, "gpt"))
	older := time.Now().Add(-time.Hour)
	require.NoError(t, os.Chtimes(fileA, older, older))

	u, c := ReadTurnUsage("conv-1", sessionDir)
	require.NotNil(t, u, "resumed session must still get per-turn usage")
	assert.Equal(t, int64(300), u.InputTokens)
	assert.Equal(t, "b1", c)
}

func TestReadTurnUsage_fileDiscoveryByConversationID(t *testing.T) {
	otelDir := t.TempDir()
	t.Setenv("DASH0_COPILOT_OTEL_DIR", otelDir)
	sessionDir := t.TempDir()
	// Two concurrent sessions' files; the reader must pick ours by conversation.id.
	writeLines(t, filepath.Join(otelDir, "other.jsonl"), chatSpanLine("o1", "conv-OTHER", 999, 999, 0, 0, 9, "gpt"))
	writeLines(t, filepath.Join(otelDir, "ours.jsonl"), chatSpanLine("m1", "conv-MINE", 100, 20, 0, 0, 1, "gpt"))
	u, _ := ReadTurnUsage("conv-MINE", sessionDir)
	require.NotNil(t, u)
	assert.Equal(t, int64(100), u.InputTokens)
}

func TestReadTurnUsage_absentGraceful(t *testing.T) {
	t.Setenv("DASH0_COPILOT_OTEL_DIR", t.TempDir()) // empty dir
	u1, c1 := ReadTurnUsage("conv-1", t.TempDir())
	assert.Nil(t, u1)
	assert.Empty(t, c1)
	u2, _ := ReadTurnUsage("", t.TempDir())
	assert.Nil(t, u2)
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

// ReadTurnUsage recovers the turn's final assistant text from the chat span's
// gen_ai.output.messages so the pipeline can render gen_ai.output.messages (the
// agent response) — Copilot's agentStop payload carries no response text.
func TestReadTurnUsage_responseText(t *testing.T) {
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

	u, _ := ReadTurnUsage("conv-1", sessionDir)
	require.NotNil(t, u)
	assert.Equal(t, "All done.", u.ResponseText)
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

func TestLatestModel(t *testing.T) {
	otelDir := t.TempDir()
	t.Setenv("DASH0_COPILOT_OTEL_DIR", otelDir)
	writeLines(t, filepath.Join(otelDir, "otel.jsonl"),
		chatSpanLine("s1", "conv-1", 10, 2, 0, 0, 0, "gpt-5.3-codex"),
		chatSpanLine("s2", "conv-1", 10, 2, 0, 0, 0, "gpt-5.3-codex-v2"))
	assert.Equal(t, "gpt-5.3-codex-v2", LatestModel("conv-1"), "returns the most recent chat span's model")
	assert.Empty(t, LatestModel("conv-none"))
	assert.Empty(t, LatestModel(""))
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
