// SPDX-FileCopyrightText: Copyright 2026 Dash0 Inc.
// SPDX-License-Identifier: Apache-2.0

package otlp

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSaveAndLoadTraceContext(t *testing.T) {
	dir := t.TempDir()

	ctx := TraceContext{
		TraceID:   "aaaabbbbccccddddaaaabbbbccccdddd",
		SpanID:    "1111222233334444",
		SessionID: "sess-123",
		Model:     "claude-sonnet-4-20250514",
	}
	require.NoError(t, SaveTraceContext(ctx, dir))

	loaded, err := LoadTraceContext(dir)
	require.NoError(t, err)
	require.NotNil(t, loaded)

	assert.Equal(t, ctx.TraceID, loaded.TraceID)
	assert.Equal(t, ctx.SpanID, loaded.SpanID)
	assert.Equal(t, ctx.SessionID, loaded.SessionID)
	assert.Equal(t, ctx.Model, loaded.Model)
}

func TestLoadTraceContextBackwardCompatibility(t *testing.T) {
	dir := t.TempDir()

	// Simulate a trace_context.json written with extra fields — should decode fine.
	data := []byte(`{"trace_id":"aabb","span_id":"1122","session_id":"s1","chat_span_id":"old"}`)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "trace_context.json"), data, 0o644))

	loaded, err := LoadTraceContext(dir)
	require.NoError(t, err)
	require.NotNil(t, loaded)
	assert.Equal(t, "aabb", loaded.TraceID)
}

func TestLoadTraceContextMissing(t *testing.T) {
	dir := t.TempDir()

	loaded, err := LoadTraceContext(dir)
	require.NoError(t, err)
	assert.Nil(t, loaded)
}

func TestSaveAndLoadAgentTraceContext(t *testing.T) {
	dir := t.TempDir()

	ctx := TraceContext{
		TraceID:   "aaaabbbbccccddddaaaabbbbccccdddd",
		SpanID:    "1111222233334444",
		SessionID: "sess-123",
		Model:     "claude-fable-5",
		StartTime: "2026-01-01T12:00:02.000000000Z",
	}
	require.NoError(t, SaveAgentTraceContext(ctx, dir, "ada80f24d6e56175a"))

	// Independent from the session-level context.
	sessionCtx, err := LoadTraceContext(dir)
	require.NoError(t, err)
	assert.Nil(t, sessionCtx)

	loaded, err := LoadAgentTraceContext(dir, "ada80f24d6e56175a")
	require.NoError(t, err)
	require.NotNil(t, loaded)
	assert.Equal(t, ctx, *loaded)

	// A different agent ID has no snapshot.
	other, err := LoadAgentTraceContext(dir, "0000000000000000")
	require.NoError(t, err)
	assert.Nil(t, other)
}

func TestClearAgentTraceContext(t *testing.T) {
	dir := t.TempDir()

	require.NoError(t, SaveAgentTraceContext(TraceContext{TraceID: "aabb"}, dir, "agent1"))
	ClearAgentTraceContext(dir, "agent1")

	loaded, err := LoadAgentTraceContext(dir, "agent1")
	require.NoError(t, err)
	assert.Nil(t, loaded)
}

func TestAgentTraceContextConsumedMarker(t *testing.T) {
	dir := t.TempDir()
	ctx := TraceContext{TraceID: "aaaabbbbccccddddaaaabbbbccccdddd"}
	require.NoError(t, SaveAgentTraceContext(ctx, dir, "agent1"))

	require.NoError(t, MarkAgentTraceContextConsumed(dir, "agent1"))
	consumed, err := AgentTraceContextConsumed(dir, "agent1")
	require.NoError(t, err)
	assert.True(t, consumed)

	ClearAgentTraceContext(dir, "agent1")
	consumed, err = AgentTraceContextConsumed(dir, "agent1")
	require.NoError(t, err)
	assert.True(t, consumed, "clearing the snapshot keeps the fail-closed marker")

	require.NoError(t, SaveAgentTraceContext(ctx, dir, "agent1"))
	consumed, err = AgentTraceContextConsumed(dir, "agent1")
	require.NoError(t, err)
	assert.True(t, consumed, "agent IDs are invocation-unique, so unexpected reuse stays fail-closed")
}

func TestSaveAndLoadToolModel(t *testing.T) {
	dir := t.TempDir()
	traceID := "aaaabbbbccccddddaaaabbbbccccdddd"

	require.NoError(t, SaveToolModel(dir, traceID, "", "claude-opus-4-8"))
	require.NoError(t, SaveToolModel(dir, traceID, "agent1", "claude-haiku-4-5"))

	model, err := LoadToolModel(dir, traceID, "")
	require.NoError(t, err)
	assert.Equal(t, "claude-opus-4-8", model)
	agentModel, err := LoadToolModel(dir, traceID, "agent1")
	require.NoError(t, err)
	assert.Equal(t, "claude-haiku-4-5", agentModel)

	other, err := LoadToolModel(dir, "11112222333344441111222233334444", "")
	require.NoError(t, err)
	assert.Empty(t, other, "a later turn cannot read another trace's cached model")

	ClearToolModels(dir, traceID)
	model, err = LoadToolModel(dir, traceID, "")
	require.NoError(t, err)
	assert.Empty(t, model)
	agentModel, err = LoadToolModel(dir, traceID, "agent1")
	require.NoError(t, err)
	assert.Empty(t, agentModel)
}

func TestToolModelRejectsUnsafeActorID(t *testing.T) {
	dir := t.TempDir()
	traceID := "aaaabbbbccccddddaaaabbbbccccdddd"

	for _, actorID := range []string{"../escape", "a/b", "a.b", "a b"} {
		assert.Error(t, SaveToolModel(dir, traceID, actorID, "claude-opus-4-8"), "actor %q", actorID)
		_, err := LoadToolModel(dir, traceID, actorID)
		assert.Error(t, err, "actor %q", actorID)
	}
}

func TestAgentTraceContextRejectsUnsafeAgentID(t *testing.T) {
	dir := t.TempDir()

	for _, id := range []string{"", "../escape", "a/b", "a.b", "a b"} {
		assert.Error(t, SaveAgentTraceContext(TraceContext{TraceID: "aabb"}, dir, id), "id %q", id)

		loaded, err := LoadAgentTraceContext(dir, id)
		require.NoError(t, err, "id %q", id)
		assert.Nil(t, loaded, "id %q", id)

		ClearAgentTraceContext(dir, id) // must not panic or touch anything
	}

	// Nothing was written outside or inside the dir.
	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	assert.Empty(t, entries)
}
