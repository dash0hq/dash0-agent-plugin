package codex

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/dash0hq/dash0-agent-plugin/internal/filelog"
)

// logEvent mirrors what pipeline.Process does before each hook: stamp a
// timestamp and append to the session events.jsonl.
func logEvent(t *testing.T, sessionDir string, event map[string]any, ts time.Time) {
	t.Helper()
	event["timestamp"] = ts.Format(time.RFC3339Nano)
	require.NoError(t, filelog.WriteEvent(event, sessionDir))
}

func TestNormalizeDerivesDurationFromPreToolUse(t *testing.T) {
	dir := t.TempDir()
	pre := time.Date(2026, 7, 7, 12, 0, 0, 0, time.UTC)
	post := pre.Add(1500 * time.Millisecond)

	logEvent(t, dir, map[string]any{
		"hook_event_name": "PreToolUse",
		"tool_use_id":     "call_abc",
		"tool_name":       "Bash",
	}, pre)

	event := Normalize(map[string]any{
		"hook_event_name": "PostToolUse",
		"tool_use_id":     "call_abc",
		"tool_name":       "Bash",
		"tool_response":   "done",
	}, dir, post)

	require.NotNil(t, event)
	d, ok := event["duration_ms"].(float64)
	require.True(t, ok, "duration_ms should be injected as float64")
	assert.Equal(t, float64(1500), d)
}

func TestNormalizeKeepsExistingDuration(t *testing.T) {
	dir := t.TempDir()
	event := Normalize(map[string]any{
		"hook_event_name": "PostToolUse",
		"tool_use_id":     "call_abc",
		"duration_ms":     float64(42),
	}, dir, time.Now().UTC())
	assert.Equal(t, float64(42), event["duration_ms"])
}

func TestNormalizeNoMatchingPreToolUse(t *testing.T) {
	dir := t.TempDir()
	event := Normalize(map[string]any{
		"hook_event_name": "PostToolUse",
		"tool_use_id":     "call_missing",
	}, dir, time.Now().UTC())
	_, ok := event["duration_ms"]
	assert.False(t, ok, "no duration when no matching PreToolUse exists")
}

func TestNormalizeNonToolEventsPassThrough(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"SessionStart", "UserPromptSubmit", "Stop", "SubagentStop"} {
		in := map[string]any{"hook_event_name": name, "session_id": "s1"}
		out := Normalize(in, dir, time.Now().UTC())
		require.NotNil(t, out)
		_, ok := out["duration_ms"]
		assert.False(t, ok, "%s must not gain duration_ms", name)
		assert.Equal(t, "s1", out["session_id"])
	}
}

// TestNormalizeOverCapturedFixtures replays the real captured hook stream the
// way the pipeline would (log each event, then normalize), and asserts every
// PostToolUse that has a preceding PreToolUse with the same tool_use_id gets a
// non-negative duration. This guards the normalizer against real payload shapes.
func TestNormalizeOverCapturedFixtures(t *testing.T) {
	f, err := os.Open(filepath.Join("testdata", "captured_events.jsonl"))
	require.NoError(t, err)
	defer f.Close()

	dir := t.TempDir()
	base := time.Date(2026, 7, 7, 12, 0, 0, 0, time.UTC)

	seenPre := map[string]bool{}
	posts, withDuration := 0, 0

	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1024*1024), 1024*1024)
	i := 0
	for sc.Scan() {
		line := sc.Bytes()
		if len(line) == 0 {
			continue
		}
		var event map[string]any
		require.NoError(t, json.Unmarshal(line, &event))

		ts := base.Add(time.Duration(i) * time.Second)
		i++
		name, _ := event["hook_event_name"].(string)
		id, _ := event["tool_use_id"].(string)

		if name == "PreToolUse" && id != "" {
			seenPre[id] = true
		}

		// Log then normalize, mirroring pipeline ordering (pre-events already on disk).
		logEvent(t, dir, cloneMap(event), ts)
		out := Normalize(event, dir, ts)
		require.NotNil(t, out)

		if name == "PostToolUse" {
			posts++
			if _, ok := out["duration_ms"].(float64); ok {
				withDuration++
			} else {
				// Only acceptable when there was no matching PreToolUse.
				assert.False(t, seenPre[id], "PostToolUse %s had a PreToolUse but no duration_ms", id)
			}
		}
	}
	require.NoError(t, sc.Err())

	assert.Positive(t, posts, "fixture should contain PostToolUse events")
	assert.Positive(t, withDuration, "at least some PostToolUse events should get a derived duration")
	t.Logf("PostToolUse: %d total, %d with derived duration", posts, withDuration)
}

func cloneMap(m map[string]any) map[string]any {
	out := make(map[string]any, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}
