// SPDX-FileCopyrightText: Copyright 2026 Dash0 Inc.
// SPDX-License-Identifier: Apache-2.0

package pipeline

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestProcess_Stop_WaitsForAContinuationAfterEndTurn reproduces the race that
// dropped a whole API call's usage, end to end and deterministically.
//
// Claude Code injects a meta user entry when a response produced no visible
// output, then calls the model again. The transcript at that moment ends:
//
//	user (real)  → assistant stop_reason=end_turn → user isMeta
//
// which reads as a finished turn. Stop fires there, the plugin takes end_turn at
// face value, and the continuation's usage lands in the file afterwards — in no
// span at all, because Stop has already sent the only chat span the turn gets.
//
// Observed live on qa/runs/spec-subagent, 2026-08-25: the chat span carried 251
// output tokens against the transcript's 251 + 427, and the missing 427 was in
// no span. The unit test in internal/transcript pins the predicate; this pins
// the behaviour the predicate exists for, by writing the late entry while the
// pipeline is waiting for it.
func TestProcess_Stop_WaitsForAContinuationAfterEndTurn(t *testing.T) {
	url, spans, mu := mockOTLPServer(t)
	s := newSetup(t, url)

	const (
		firstCall  = `{"type":"assistant","requestId":"r1","message":{"role":"assistant","stop_reason":"end_turn","content":[],"usage":{"input_tokens":10,"output_tokens":251}}}`
		metaNudge  = `{"type":"user","isMeta":true,"message":{"role":"user","content":"[Your previous response had no visible output. Please continue and produce a user-visible response.]"}}`
		secondCall = `{"type":"assistant","requestId":"r2","message":{"role":"assistant","stop_reason":"end_turn","content":[{"type":"text","text":"done"}],"usage":{"input_tokens":10,"output_tokens":427}}}`
	)

	tp := filepath.Join(t.TempDir(), "transcript.jsonl")
	require.NoError(t, os.WriteFile(tp,
		[]byte(`{"type":"user","message":{"role":"user","content":"go"}}`+"\n"+
			firstCall+"\n"+metaNudge+"\n"), 0o644))

	// The continuation lands while Stop is in flight. 100ms is inside
	// turnCompleteWaitBudget and several poll intervals in, so the wait is doing
	// real work rather than being outrun by the writer.
	done := make(chan struct{})
	go func() {
		defer close(done)
		time.Sleep(100 * time.Millisecond)
		f, err := os.OpenFile(tp, os.O_APPEND|os.O_WRONLY, 0o644)
		if err != nil {
			return
		}
		_, _ = f.WriteString(secondCall + "\n")
		_ = f.Close()
	}()

	s.feed(t, map[string]any{"hook_event_name": "SessionStart", "session_id": "sess-1", "model": "haiku"})
	s.feed(t, map[string]any{"hook_event_name": "UserPromptSubmit", "session_id": "sess-1", "prompt": "go"})
	s.feed(t, map[string]any{
		"hook_event_name": "Stop",
		"session_id":      "sess-1",
		"transcript_path": tp,
	})
	<-done

	mu.Lock()
	defer mu.Unlock()
	require.Len(t, *spans, 1)
	assert.Equal(t, "678", intAttr(t, (*spans)[0], "gen_ai.usage.output_tokens"),
		"the turn's output tokens must include the continuation (251+427), not stop at the end_turn before it")
	assert.Equal(t, "20", intAttr(t, (*spans)[0], "gen_ai.usage.input_tokens"))
}
