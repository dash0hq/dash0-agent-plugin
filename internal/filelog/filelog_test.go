package filelog

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestWriteEvent(t *testing.T) {
	dir := t.TempDir()
	event := map[string]any{"hook_event_name": "SessionStart", "session_id": "abc123"}

	require.NoError(t, WriteEvent(event, dir))

	lines := readLines(t, filepath.Join(dir, "events.jsonl"))
	require.Len(t, lines, 1)

	var got map[string]any
	require.NoError(t, json.Unmarshal([]byte(lines[0]), &got))
	assert.Equal(t, "SessionStart", got["hook_event_name"])
}

func TestAppendsMultipleEvents(t *testing.T) {
	dir := t.TempDir()

	for _, name := range []string{"first", "second", "third"} {
		require.NoError(t, WriteEvent(map[string]any{"event": name}, dir))
	}

	lines := readLines(t, filepath.Join(dir, "events.jsonl"))
	require.Len(t, lines, 3)

	for i, want := range []string{"first", "second", "third"} {
		var got map[string]any
		require.NoError(t, json.Unmarshal([]byte(lines[i]), &got))
		assert.Equal(t, want, got["event"], "line %d", i)
	}
}

func TestRotatesAtMaxEvents(t *testing.T) {
	dir := t.TempDir()

	for i := range 105 {
		require.NoError(t, WriteEvent(map[string]any{"seq": i}, dir), "event %d", i)
	}

	lines := readLines(t, filepath.Join(dir, "events.jsonl"))
	require.Len(t, lines, MaxEvents)

	var first map[string]any
	require.NoError(t, json.Unmarshal([]byte(lines[0]), &first))
	assert.Equal(t, float64(5), first["seq"], "first retained event")

	var last map[string]any
	require.NoError(t, json.Unmarshal([]byte(lines[len(lines)-1]), &last))
	assert.Equal(t, float64(104), last["seq"], "last event")
}

func TestPreservesNestedJSON(t *testing.T) {
	dir := t.TempDir()
	event := map[string]any{
		"tool_name":  "Bash",
		"tool_input": map[string]any{"command": "ls -la", "timeout": 5000},
		"nested":     map[string]any{"deep": map[string]any{"value": 42}},
	}

	require.NoError(t, WriteEvent(event, dir))

	lines := readLines(t, filepath.Join(dir, "events.jsonl"))
	var got map[string]any
	require.NoError(t, json.Unmarshal([]byte(lines[0]), &got))

	toolInput, ok := got["tool_input"].(map[string]any)
	require.True(t, ok, "tool_input not preserved as object")
	assert.Equal(t, "ls -la", toolInput["command"])

	nested := got["nested"].(map[string]any)["deep"].(map[string]any)
	assert.Equal(t, float64(42), nested["value"])
}

// Suppress unused warning for fmt import.
var _ = fmt.Sprintf

func readLines(t *testing.T, path string) []string {
	t.Helper()
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	var lines []string
	for _, line := range strings.Split(string(data), "\n") {
		if line != "" {
			lines = append(lines, line)
		}
	}
	return lines
}
