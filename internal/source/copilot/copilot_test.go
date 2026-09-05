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

func TestNormalize_systemNotificationPromptRole(t *testing.T) {
	// Copilot injects sub-agent notices as a synthetic userPromptSubmitted wrapped
	// in <system_notification>; mark it prompt_role=assistant so the chat span
	// renders it as agent-side, not user input.
	n := Normalize("userPromptSubmitted", parse(t, `{"sessionId":"c","prompt":"<system_notification>\nAgent \"time-ticker\" (task) has finished processing and is now idle."}`))
	require.NotNil(t, n)
	assert.Equal(t, "assistant", n["prompt_role"])

	// A genuine user prompt is untouched (stays user input).
	u := Normalize("userPromptSubmitted", parse(t, `{"sessionId":"c","prompt":"what is 2+2?"}`))
	require.NotNil(t, u)
	_, has := u["prompt_role"]
	assert.False(t, has, "real user prompts must not be tagged")
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
	f := filepath.Join(otelDir, "otel-1.jsonl")

	// Turn 1: one chat span.
	writeLines(t, f, chatSpanLine("s1", "conv-1", 100, 20, 90, 5, 1.0, "gpt-5.3-codex"))
	t1, c1 := ReadTurn("conv-1")
	require.NotNil(t, t1)
	require.NotNil(t, t1.Usage)
	assert.Equal(t, int64(100), t1.Usage.InputTokens)
	assert.Equal(t, int64(20), t1.Usage.OutputTokens)
	assert.Equal(t, int64(90), t1.Usage.CacheReadInputTokens)
	assert.Equal(t, int64(5), t1.Usage.ReasoningOutputTokens)
	assert.Equal(t, "gpt-5.3-codex", t1.Usage.Model)
	assert.Equal(t, "s1", c1)
	SaveCursor("conv-1", c1)

	// Turn 2: append a second span; the reader returns ONLY turn 2.
	appendLines(t, f, chatSpanLine("s2", "conv-1", 200, 30, 150, 0, 2.0, "gpt-5.3-codex"))
	t2, c2 := ReadTurn("conv-1")
	require.NotNil(t, t2)
	require.NotNil(t, t2.Usage)
	assert.Equal(t, int64(200), t2.Usage.InputTokens, "must not double-count turn 1")
	assert.Equal(t, int64(30), t2.Usage.OutputTokens)
	assert.Equal(t, "s2", c2)
	SaveCursor("conv-1", c2)

	// Re-run with no new spans → nil (idempotent, no double-count).
	t3, c3 := ReadTurn("conv-1")
	assert.Nil(t, t3)
	assert.Empty(t, c3)
}

func TestReadTurn_subAgentRollup(t *testing.T) {
	otelDir := t.TempDir()
	t.Setenv("DASH0_COPILOT_OTEL_DIR", otelDir)
	// A turn with a main chat span + two sub-agent chat spans (same conversation.id).
	writeLines(t, filepath.Join(otelDir, "otel.jsonl"),
		chatSpanLine("s1", "conv-1", 100, 20, 0, 0, 1.0, "gpt"),
		chatSpanLine("s2", "conv-1", 50, 10, 0, 0, 0.5, "gpt"),
		chatSpanLine("s3", "conv-1", 40, 8, 0, 0, 0.5, "gpt"))
	turn, c := ReadTurn("conv-1")
	require.NotNil(t, turn)
	require.NotNil(t, turn.Usage)
	assert.Equal(t, int64(190), turn.Usage.InputTokens, "sub-agent input tokens roll into the turn total")
	assert.Equal(t, int64(38), turn.Usage.OutputTokens, "sub-agent output tokens roll into the turn total")
	assert.Equal(t, "s3", c, "cursor is the last consumed span")
}

// TestReadTurn_resumeRotatedFile is the core cross-launch case: a resumed
// session writes a NEW file (newer mtime) with disjoint span ids. The reader
// must prefer the newest file and, finding the old cursor absent from it, treat
// all its spans as fresh — so the recovered session still reports per-turn usage.
func TestReadTurn_resumeRotatedFile(t *testing.T) {
	otelDir := t.TempDir()
	t.Setenv("DASH0_COPILOT_OTEL_DIR", otelDir)

	// Launch 1.
	fileA := filepath.Join(otelDir, "otel-A.jsonl")
	writeLines(t, fileA, chatSpanLine("a1", "conv-1", 100, 20, 0, 0, 1, "gpt"))
	_, c1 := ReadTurn("conv-1")
	SaveCursor("conv-1", c1) // cursor = "a1"

	// Launch 2 (resume): brand-new file, disjoint ids, made newer than A.
	fileB := filepath.Join(otelDir, "otel-B.jsonl")
	writeLines(t, fileB, chatSpanLine("b1", "conv-1", 300, 40, 0, 0, 3, "gpt"))
	older := time.Now().Add(-time.Hour)
	require.NoError(t, os.Chtimes(fileA, older, older))

	turn, c := ReadTurn("conv-1")
	require.NotNil(t, turn, "resumed session must still get per-turn usage")
	require.NotNil(t, turn.Usage)
	assert.Equal(t, int64(300), turn.Usage.InputTokens)
	assert.Equal(t, "b1", c)
}

// TestReadTurn_cursorSurvivesSessionEnd is the other cross-launch case, and the
// one that shipped broken. When both launches write to ONE native-OTel file —
// which is what a fixed COPILOT_OTEL_FILE_EXPORTER_PATH gives you, the
// documented alternative to the dash0-configure launch function — the cursor is
// the only thing keeping turn 2 from re-reading turn 1. It used to live in the
// per-session directory that pipeline.Process deletes on SessionEnd, so the
// resumed launch found none and summed the whole file again.
//
// Measured on qa/runs/probe-two-turns before the fix: turn 2's chat span
// carried 59068 input tokens for a turn of 29655, and turn 1's tool span was
// emitted a second time under turn 2's trace.
//
// This asserts the cursor's location rather than simulating the wipe, because
// there is no longer a session directory in the path to wipe — which is the
// property that makes the wipe survivable. Nothing drives the wipe end to end any
// more, so this location check is the whole of that coverage.
func TestReadTurn_cursorSurvivesSessionEnd(t *testing.T) {
	otelDir := t.TempDir()
	t.Setenv("DASH0_COPILOT_OTEL_DIR", otelDir)
	f := filepath.Join(otelDir, "otel.jsonl")

	writeLines(t, f, chatSpanLine("s1", "conv-1", 100, 20, 0, 0, 1, "gpt"))
	_, c1 := ReadTurn("conv-1")
	SaveCursor("conv-1", c1)

	// The cursor is keyed by conversation and lives beside the OTel files, so it
	// is reachable with nothing but the session id — no session directory is
	// consulted, which is precisely why the SessionEnd wipe can no longer reach
	// it. This is the only assertion on that path; the e2e that drove it is gone.
	require.FileExists(t, filepath.Join(otelDir, "cursor-conv-1.json"))

	// The resumed launch appends to the same file.
	appendLines(t, f, chatSpanLine("s2", "conv-1", 200, 30, 0, 0, 2, "gpt"))
	turn, _ := ReadTurn("conv-1")
	require.NotNil(t, turn)
	require.NotNil(t, turn.Usage)
	assert.Equal(t, int64(200), turn.Usage.InputTokens,
		"a resumed launch sharing one OTel file must not re-count the previous launch")
	assert.Equal(t, int64(30), turn.Usage.OutputTokens)
}

// TestSweepOldOtelFilesKeepsCursors pins the exemption. The sweep runs on any
// session's start and cannot tell a conversation that is over from one idle
// over a weekend — and an idle conversation's cursor ages while a shared
// native-OTel file stays fresh under other sessions' writes. Deleting it sends
// the next ReadTurn back to the top of that file and double-counts everything
// it already reported, which is the defect the cursor exists to prevent.
func TestSweepOldOtelFilesKeepsCursors(t *testing.T) {
	otelDir := t.TempDir()
	t.Setenv("DASH0_COPILOT_OTEL_DIR", otelDir)

	SaveCursor("conv-idle", "s1")
	old := time.Now().Add(-4 * 7 * 24 * time.Hour)
	require.NoError(t, os.Chtimes(filepath.Join(otelDir, "cursor-conv-idle.json"), old, old))

	// A stale native-OTel file in the same directory still goes.
	stale := filepath.Join(otelDir, "otel-dead.jsonl")
	writeLines(t, stale, chatSpanLine("x", "conv-other", 1, 1, 0, 0, 0, "gpt"))
	require.NoError(t, os.Chtimes(stale, old, old))

	SweepOldOtelFiles(time.Now())

	assert.NoFileExists(t, stale, "a native-OTel file left by an unclean exit is still swept")
	assert.FileExists(t, filepath.Join(otelDir, "cursor-conv-idle.json"),
		"a cursor is never swept: losing one double-counts the conversation's next turn")
}

// TestReadTurn_turnRootUnderARemoteParent covers a trace this plugin does not
// root itself. Copilot already injects a traceparent into an interactive
// session's hook payloads, so a turn whose trace continues one from outside
// would carry a parent id on its own invoke_agent span naming something this
// file does not hold.
//
// That span is still the turn, and the pipeline's chat span already represents
// it. Reading it as a sub-agent — which an "is the parent id empty" test does —
// mints an invoke_agent span duplicating the turn, and re-parents the whole tool
// tree beneath it, because parenting resolves to the nearest emitted ancestor.
//
// The same fixture as TestReadTurn_toolCalls, with one difference: the root
// invoke_agent names a parent outside the file.
func TestReadTurn_turnRootUnderARemoteParent(t *testing.T) {
	otelDir := t.TempDir()
	t.Setenv("DASH0_COPILOT_OTEL_DIR", otelDir)

	writeLines(t, filepath.Join(otelDir, "otel.jsonl"),
		nativeSpanLine(t, "t1", "ch1", "ia1", "chat gpt", 100, 101, 0, map[string]any{
			"gen_ai.conversation.id": "conv-1", "gen_ai.request.model": "gpt",
			"gen_ai.usage.input_tokens": 100, "gen_ai.usage.output_tokens": 10,
		}),
		nativeSpanLine(t, "t1", "e1", "ia1", "execute_tool bash", 101, 101.5, 0, map[string]any{
			"gen_ai.tool.name": "bash", "gen_ai.tool.call.id": "call_A",
		}),
		// The turn's root, under a span no file of this conversation carries.
		nativeSpanLine(t, "t1", "ia1", "REMOTEPARENT0001", "invoke_agent", 100, 104, 0, map[string]any{
			"gen_ai.conversation.id": "conv-1", "gen_ai.agent.name": "default",
		}),
	)

	turn, _ := ReadTurn("conv-1")
	require.NotNil(t, turn)

	assert.Empty(t, turn.Agents,
		"the turn's own agent is the turn, however its trace is rooted; emitting it duplicates the turn")
	require.Len(t, turn.Tools, 1)
	assert.Empty(t, turn.Tools[0].ParentSpanID,
		"a top-level tool still resolves to the turn's chat span, not to a spurious agent span")
	require.NotNil(t, turn.Usage)
	assert.Equal(t, int64(100), turn.Usage.InputTokens, "usage is unaffected")
}

// TestReadTurn_laterTurnRootChainedInFile is the in-file half of the case above.
// A turn whose root continues an earlier turn's trace carries a parent id that
// this file DOES hold, so "the parent is some span of this conversation" reads
// that root as a sub-agent: it would be emitted under the turn's own chat span,
// duplicating the turn, and every tool of the turn would re-parent beneath it.
//
// What actually marks a sub-agent is hanging under the execute_tool call that
// spawned it, so that is what the reader tests.
func TestReadTurn_laterTurnRootChainedInFile(t *testing.T) {
	otelDir := t.TempDir()
	t.Setenv("DASH0_COPILOT_OTEL_DIR", otelDir)
	f := filepath.Join(otelDir, "otel.jsonl")

	// Turn 1, consumed and cursored away.
	writeLines(t, f,
		nativeSpanLine(t, "t1", "ch1", "ia1", "chat gpt", 100, 101, 0, map[string]any{
			"gen_ai.conversation.id": "conv-1", "gen_ai.request.model": "gpt",
			"gen_ai.usage.input_tokens": 100, "gen_ai.usage.output_tokens": 10,
		}),
		nativeSpanLine(t, "t1", "ia1", "", "invoke_agent", 100, 102, 0, map[string]any{
			"gen_ai.conversation.id": "conv-1", "gen_ai.agent.name": "default",
		}),
	)
	_, c1 := ReadTurn("conv-1")
	SaveCursor("conv-1", c1)

	// Turn 2, whose root hangs off turn 1's root rather than starting a trace.
	appendLines(t, f,
		nativeSpanLine(t, "t1", "ch2", "ia2", "chat gpt", 103, 104, 0, map[string]any{
			"gen_ai.conversation.id": "conv-1", "gen_ai.request.model": "gpt",
			"gen_ai.usage.input_tokens": 200, "gen_ai.usage.output_tokens": 20,
		}),
		nativeSpanLine(t, "t1", "e2", "ia2", "execute_tool bash", 103, 103.5, 0, map[string]any{
			"gen_ai.tool.name": "bash", "gen_ai.tool.call.id": "call_B",
		}),
		nativeSpanLine(t, "t1", "ia2", "ia1", "invoke_agent", 103, 105, 0, map[string]any{
			"gen_ai.conversation.id": "conv-1", "gen_ai.agent.name": "default",
		}),
	)

	turn, _ := ReadTurn("conv-1")
	require.NotNil(t, turn)

	assert.Empty(t, turn.Agents,
		"a turn root chained onto an earlier turn is still a turn, not a sub-agent")
	require.Len(t, turn.Tools, 1)
	assert.Empty(t, turn.Tools[0].ParentSpanID,
		"and its tools still resolve to the turn's chat span")
	require.NotNil(t, turn.Usage)
	assert.Equal(t, int64(200), turn.Usage.InputTokens, "turn 2's usage only")
}

func TestReadTurn_fileDiscoveryByConversationID(t *testing.T) {
	otelDir := t.TempDir()
	t.Setenv("DASH0_COPILOT_OTEL_DIR", otelDir)
	// Two concurrent sessions' files; the reader must pick ours by conversation.id.
	writeLines(t, filepath.Join(otelDir, "other.jsonl"), chatSpanLine("o1", "conv-OTHER", 999, 999, 0, 0, 9, "gpt"))
	writeLines(t, filepath.Join(otelDir, "ours.jsonl"), chatSpanLine("m1", "conv-MINE", 100, 20, 0, 0, 1, "gpt"))
	turn, _ := ReadTurn("conv-MINE")
	require.NotNil(t, turn)
	require.NotNil(t, turn.Usage)
	assert.Equal(t, int64(100), turn.Usage.InputTokens)
}

func TestReadTurn_absentGraceful(t *testing.T) {
	t.Setenv("DASH0_COPILOT_OTEL_DIR", t.TempDir()) // empty dir
	t1, c1 := ReadTurn("conv-1")
	assert.Nil(t, t1)
	assert.Empty(t, c1)
	t2, _ := ReadTurn("")
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
	f := filepath.Join(otelDir, "otel.jsonl")

	// Two spans in the turn: the first ends in a tool call, the second in text.
	// The recovered response is the LAST non-empty assistant text of the turn.
	writeLines(t, f,
		chatSpanWithOutput(t, "s1", "conv-1",
			`[{"role":"assistant","parts":[{"type":"tool_call","content":"echo"}]}]`),
		chatSpanWithOutput(t, "s2", "conv-1",
			`[{"role":"assistant","parts":[{"type":"text","content":"All done."}],"finish_reason":"stop"}]`))

	turn, _ := ReadTurn("conv-1")
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
// the native tree — a sub-agent's tool nests under the sub-agent's invoke_agent
// span, that agent under the `task` tool that spawned it, and top-level tools
// resolve to "" (→ the caller's chat span). Only the turn's own root
// invoke_agent and the native chat spans are collapsed away.
func TestReadTurn_toolCalls(t *testing.T) {
	otelDir := t.TempDir()
	t.Setenv("DASH0_COPILOT_OTEL_DIR", otelDir)

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
			// As captured: the agent's kind, and an id shared by every sub-agent
			// of that kind. The plugin takes the name and ignores the id.
			"gen_ai.agent.name": "task",
			"gen_ai.agent.id":   "builtin:task",
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

	turn, c := ReadTurn("conv-1")
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
	assert.Equal(t, "ia2", sub.ParentSpanID, "sub-agent tool nests under the sub-agent's own invoke_agent span")
	assert.True(t, sub.Failed, "native status code 2 marks the tool failed")

	// The sub-agent itself. Its identity is the spawning tool call's id, not the
	// native gen_ai.agent.id, which is a per-type value every task sub-agent
	// shares. The turn's root invoke_agent is NOT among these: it is the turn,
	// which the pipeline's chat span already represents.
	require.Len(t, turn.Agents, 1, "only the nested invoke_agent is a sub-agent")
	agent := turn.Agents[0]
	assert.Equal(t, "ia2", agent.SpanID, "native span id reused verbatim")
	assert.Equal(t, "e2", agent.ParentSpanID, "the sub-agent hangs under the task tool that spawned it")
	assert.Equal(t, "task", agent.AgentType)
	assert.Equal(t, "call_X", agent.CallID, "per-invocation identity comes from the spawning tool call")

	// The cursor covers ALL consumed spans (tools included): after persisting it,
	// a re-read finds nothing new.
	SaveCursor("conv-1", c)
	again, _ := ReadTurn("conv-1")
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

// The skill tool is the one tool Copilot describes with no
// gen_ai.tool.call.arguments at all — it names the invoked skill in
// github.copilot.tool.parameters.skill_name instead. The shared extractor reads
// the arguments, so without carrying this attribute through, every Copilot skill
// invocation produced a span saying only "a tool called skill ran".
func TestReadTurn_skillNameFromVendorAttribute(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("DASH0_COPILOT_OTEL_DIR", dir)

	const (
		conv  = "bbbbbbbb-cccc-4ddd-8eee-ffffffffffff"
		trace = "22222222222222222222222222222222"
	)
	writeLines(t, filepath.Join(dir, "otel.jsonl"),
		chatSpanLine("bbbbbbbbbbbbbb01", conv, 10, 5, 0, 0, 0.1, "gpt-5.3-codex"),
		// Deliberately no gen_ai.tool.call.arguments: that is what Copilot emits.
		nativeSpanLine(t, trace, "bbbbbbbbbbbbbb02", "bbbbbbbbbbbbbb00", "execute_tool skill", 1001, 1001.5, 0, map[string]any{
			"gen_ai.conversation.id":                    conv,
			"gen_ai.tool.name":                          "skill",
			"gen_ai.tool.call.id":                       "call_skill",
			"github.copilot.tool.parameters.skill_name": "dash0-e2e-probe",
		}),
	)

	turn, _ := ReadTurn(conv)
	require.NotNil(t, turn, "the turn must be recovered from the native-OTel file")
	require.Len(t, turn.Tools, 1, "the skill invocation is one tool call")

	assert.Equal(t, "skill", turn.Tools[0].Name)
	assert.Empty(t, turn.Tools[0].Arguments, "Copilot ships no arguments for the skill tool")
	assert.Equal(t, "dash0-e2e-probe", turn.Tools[0].SkillName,
		"the skill name has to come from the vendor attribute, since the arguments are empty")
}
