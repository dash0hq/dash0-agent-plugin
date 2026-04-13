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
