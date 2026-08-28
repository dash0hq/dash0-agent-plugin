// SPDX-FileCopyrightText: Copyright 2026 Dash0 Inc.
// SPDX-License-Identifier: Apache-2.0

package filelog

import (
	"encoding/json"
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

func TestKeepsEveryEventWithinTheBound(t *testing.T) {
	dir := t.TempDir()

	for i := range 105 {
		require.NoError(t, WriteEvent(map[string]any{"seq": i}, dir), "event %d", i)
	}

	lines := readLines(t, filepath.Join(dir, "events.jsonl"))
	assert.Len(t, lines, 105, "nothing is dropped until the log runs past MaxEvents+trimSlack")
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

func TestFindEventMatchesFromEnd(t *testing.T) {
	dir := t.TempDir()

	require.NoError(t, WriteEvent(map[string]any{
		"hook_event_name": "PreToolUse",
		"tool_use_id":     "tu_1",
		"timestamp":       "2025-06-15T12:00:00Z",
	}, dir))
	require.NoError(t, WriteEvent(map[string]any{
		"hook_event_name": "PreToolUse",
		"tool_use_id":     "tu_2",
		"timestamp":       "2025-06-15T12:01:00Z",
	}, dir))

	got, err := FindEvent(dir, func(e map[string]any) bool {
		name, _ := e["hook_event_name"].(string)
		id, _ := e["tool_use_id"].(string)
		return name == "PreToolUse" && id == "tu_1"
	})
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, "tu_1", got["tool_use_id"])
	assert.Equal(t, "2025-06-15T12:00:00Z", got["timestamp"])
}

func TestFindEventReturnsNilWhenNoMatch(t *testing.T) {
	dir := t.TempDir()

	require.NoError(t, WriteEvent(map[string]any{
		"hook_event_name": "SessionStart",
	}, dir))

	got, err := FindEvent(dir, func(e map[string]any) bool {
		name, _ := e["hook_event_name"].(string)
		return name == "PreToolUse"
	})
	require.NoError(t, err)
	assert.Nil(t, got)
}

func TestFindEventReturnsNilForMissingFile(t *testing.T) {
	dir := t.TempDir()

	got, err := FindEvent(dir, func(e map[string]any) bool { return true })
	require.NoError(t, err)
	assert.Nil(t, got)
}

func TestConcurrentWritesDoNotLoseEvents(t *testing.T) {
	dir := t.TempDir()
	const goroutines = 20

	errs := make(chan error, goroutines)
	for i := range goroutines {
		go func(seq int) {
			errs <- WriteEvent(map[string]any{"seq": seq}, dir)
		}(i)
	}

	for range goroutines {
		require.NoError(t, <-errs)
	}

	lines := readLines(t, filepath.Join(dir, "events.jsonl"))
	assert.Len(t, lines, goroutines, "all events must be present — no lost writes")

	seen := make(map[float64]bool)
	for _, line := range lines {
		var got map[string]any
		require.NoError(t, json.Unmarshal([]byte(line), &got))
		seen[got["seq"].(float64)] = true
	}
	assert.Len(t, seen, goroutines, "all distinct sequence numbers must be present")
}

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

// A long session used to grow events.jsonl without limit — MaxEvents was
// declared and never enforced. Codex has no SessionEnd hook, so nothing deleted
// the file either, and the raw events sat in the user's state directory forever.
func TestWriteEventTrimsToTheMostRecentEvents(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "events.jsonl")

	total := MaxEvents + trimSlack + 1
	for i := range total {
		require.NoError(t, WriteEvent(map[string]any{"hook_event_name": "PostToolUse", "n": i}, dir))
	}

	lines := readLines(t, path)
	require.Len(t, lines, MaxEvents, "the log keeps its most recent MaxEvents and no more")

	// The newest event survives, the oldest does not: FindEvent searches
	// backwards, so trimming the wrong end would break correlation.
	var first, last map[string]any
	require.NoError(t, json.Unmarshal([]byte(lines[0]), &first))
	require.NoError(t, json.Unmarshal([]byte(lines[len(lines)-1]), &last))
	assert.Equal(t, float64(total-MaxEvents), first["n"])
	assert.Equal(t, float64(total-1), last["n"])

	// And no temp file is left next to the log for FindEvent to trip over.
	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	require.Len(t, entries, 1)
	assert.Equal(t, "events.jsonl", entries[0].Name())
}

// Below the slack the log is left alone, so the common case does no rewriting.
func TestWriteEventKeepsAShortLogUntouched(t *testing.T) {
	dir := t.TempDir()
	for i := range MaxEvents + trimSlack {
		require.NoError(t, WriteEvent(map[string]any{"hook_event_name": "PostToolUse", "n": i}, dir))
	}
	assert.Len(t, readLines(t, filepath.Join(dir, "events.jsonl")), MaxEvents+trimSlack)
}
