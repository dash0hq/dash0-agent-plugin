package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestIntegrationWritesAndTimestamps(t *testing.T) {
	dataDir := t.TempDir()
	t.Setenv("CLAUDE_PLUGIN_DATA", dataDir)

	before := time.Now().UTC()
	feed(t, `{"hook_event_name":"SessionStart","session_id":"abc123"}`)
	after := time.Now().UTC()

	lines := readLines(t, filepath.Join(dataDir, "events.jsonl"))
	require.Len(t, lines, 1)

	var got map[string]any
	require.NoError(t, json.Unmarshal([]byte(lines[0]), &got))

	assert.Equal(t, "SessionStart", got["hook_event_name"])

	ts, ok := got["timestamp"].(string)
	require.True(t, ok, "timestamp field missing or not a string")

	parsed, err := time.Parse(time.RFC3339Nano, ts)
	require.NoError(t, err, "timestamp is not valid RFC3339Nano")
	assert.WithinRange(t, parsed, before.Truncate(time.Millisecond), after.Add(time.Millisecond))
}

func TestIntegrationCreatesDataDirectory(t *testing.T) {
	dataDir := filepath.Join(t.TempDir(), "nested", "path")
	t.Setenv("CLAUDE_PLUGIN_DATA", dataDir)

	feed(t, `{"event":"test"}`)

	assert.FileExists(t, filepath.Join(dataDir, "events.jsonl"))
}

func TestIntegrationFailsWithoutPluginData(t *testing.T) {
	t.Setenv("CLAUDE_PLUGIN_DATA", "")

	err := runWithStdin(`{"event":"test"}`)
	assert.Error(t, err)
}

func TestIntegrationFailsOnInvalidJSON(t *testing.T) {
	dataDir := t.TempDir()
	t.Setenv("CLAUDE_PLUGIN_DATA", dataDir)

	err := runWithStdin("not json")
	assert.Error(t, err)
}

// feed pipes input through run() and fails the test on error.
func feed(t *testing.T, input string) {
	t.Helper()
	require.NoError(t, runWithStdin(input))
}

// runWithStdin calls run() with the given string on stdin.
func runWithStdin(input string) error {
	oldStdin := os.Stdin
	defer func() { os.Stdin = oldStdin }()

	r, w, err := os.Pipe()
	if err != nil {
		return err
	}
	os.Stdin = r
	go func() {
		w.WriteString(input)
		w.Close()
	}()

	return run()
}

// readLines reads a file and returns non-empty lines.
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
