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

// A compressed rollout can't be read without a zstd dependency; the Stop event
// gets no token usage but is marked so the gap is visible/queryable in telemetry.
func TestNormalizeMarksCompressedRollout(t *testing.T) {
	dir := t.TempDir()
	out := Normalize(map[string]any{
		"hook_event_name": "Stop",
		"session_id":      "s1",
		"transcript_path": "/home/u/.codex/sessions/rollout-x.jsonl.zst",
	}, dir, time.Now().UTC())

	require.NotNil(t, out)
	assert.Equal(t, true, out["dash0.codex.rollout.compressed"])
	_, hasUsage := out["gen_ai.usage.input_tokens"]
	assert.False(t, hasUsage, "compressed rollout must not produce token usage")
}

// A sub-agent's compressed rollout (agent_transcript_path) is marked the same way.
func TestNormalizeMarksCompressedSubagentRollout(t *testing.T) {
	dir := t.TempDir()
	out := Normalize(map[string]any{
		"hook_event_name":       "SubagentStop",
		"session_id":            "s1",
		"transcript_path":       "/home/u/.codex/sessions/rollout-main.jsonl",
		"agent_transcript_path": "/home/u/.codex/sessions/rollout-worker.jsonl.zst",
	}, dir, time.Now().UTC())

	require.NotNil(t, out)
	assert.Equal(t, true, out["dash0.codex.rollout.compressed"])
}

// writeRollout puts a one-turn rollout on disk and returns its path. rateLimits
// is spliced in verbatim so each case controls the exact wire shape.
func writeRollout(t *testing.T, rateLimits string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "rollout.jsonl")
	payload := `{"type":"token_count","info":{"last_token_usage":{"input_tokens":100,"cached_input_tokens":10,"output_tokens":5}}`
	if rateLimits != "" {
		payload += `,"rate_limits":` + rateLimits
	}
	content := `{"type":"event_msg","payload":{"type":"user_message","message":"hi"}}` + "\n" +
		`{"type":"event_msg","payload":` + payload + `}}` + "\n"
	require.NoError(t, os.WriteFile(path, []byte(content), 0o644))
	return path
}

// A full rate_limits block lands on the span under the harness-neutral
// dash0.gen_ai.* namespace — the same problem exists for Claude Code, Cursor and
// Copilot, so one consumer-side label should serve all four.
func TestNormalizeEmitsRateLimits(t *testing.T) {
	path := writeRollout(t, `{"limit_id":"codex","plan_type":"pro",`+
		`"primary":{"used_percent":29,"window_minutes":43200,"resets_at":1786008501},`+
		`"rate_limit_reached_type":"primary",`+
		`"credits":{"has_credits":true,"unlimited":false,"balance":12.5}}`)

	out := Normalize(map[string]any{
		"hook_event_name": "Stop",
		"session_id":      "s1",
		"transcript_path": path,
	}, t.TempDir(), time.Now().UTC())
	require.NotNil(t, out)

	assert.Equal(t, "subscription", out["dash0.gen_ai.billing_mode"])
	assert.Equal(t, "pro", out["dash0.gen_ai.plan_type"])
	assert.Equal(t, 29.0, out["dash0.gen_ai.rate_limit.primary.used_percent"])
	assert.Equal(t, int64(43200), out["dash0.gen_ai.rate_limit.primary.window_minutes"])
	assert.Equal(t, int64(1786008501), out["dash0.gen_ai.rate_limit.primary.resets_at"])
	assert.Equal(t, "primary", out["dash0.gen_ai.rate_limit.reached_type"])
	assert.Equal(t, true, out["dash0.gen_ai.credits.available"])
	assert.Equal(t, false, out["dash0.gen_ai.credits.unlimited"])
	assert.Equal(t, 12.5, out["dash0.gen_ai.credits.balance"])

	// Token usage still rides along from the same single pass.
	assert.Equal(t, int64(100), out["gen_ai.usage.input_tokens"])
}

// Without a rate_limits block we still say something: "unknown" records that we
// looked and could not tell, which is different from never having looked. Every
// other attribute stays off the span rather than being emitted as a zero.
func TestNormalizeEmitsUnknownBillingModeWithoutRateLimits(t *testing.T) {
	out := Normalize(map[string]any{
		"hook_event_name": "Stop",
		"session_id":      "s1",
		"transcript_path": writeRollout(t, ""),
	}, t.TempDir(), time.Now().UTC())
	require.NotNil(t, out)

	assert.Equal(t, "unknown", out["dash0.gen_ai.billing_mode"])
	for _, k := range []string{
		"dash0.gen_ai.plan_type",
		"dash0.gen_ai.rate_limit.primary.used_percent",
		"dash0.gen_ai.rate_limit.primary.window_minutes",
		"dash0.gen_ai.rate_limit.primary.resets_at",
		"dash0.gen_ai.rate_limit.secondary.used_percent",
		"dash0.gen_ai.rate_limit.reached_type",
		"dash0.gen_ai.credits.available",
		"dash0.gen_ai.credits.unlimited",
		"dash0.gen_ai.credits.balance",
	} {
		_, present := out[k]
		assert.False(t, present, "%s must be absent, not zero-valued", k)
	}
}

// Both windows are emitted under matching keys, so a consumer reads whichever it
// needs by window_minutes rather than by guessing which slot holds which
// duration.
func TestNormalizeEmitsBothRateLimitWindows(t *testing.T) {
	out := Normalize(map[string]any{
		"hook_event_name": "Stop",
		"session_id":      "s1",
		"transcript_path": writeRollout(t, `{"plan_type":"pro",`+
			`"primary":{"used_percent":29,"window_minutes":43200,"resets_at":1786008501},`+
			`"secondary":{"used_percent":80,"window_minutes":300,"resets_at":1786000000}}`),
	}, t.TempDir(), time.Now().UTC())
	require.NotNil(t, out)

	assert.Equal(t, 29.0, out["dash0.gen_ai.rate_limit.primary.used_percent"])
	assert.Equal(t, int64(43200), out["dash0.gen_ai.rate_limit.primary.window_minutes"])
	assert.Equal(t, 80.0, out["dash0.gen_ai.rate_limit.secondary.used_percent"])
	assert.Equal(t, int64(300), out["dash0.gen_ai.rate_limit.secondary.window_minutes"])
	assert.Equal(t, int64(1786000000), out["dash0.gen_ai.rate_limit.secondary.resets_at"])
}

// A window the plan does not report is omitted entirely — the asymmetry between
// the two slots must not surface as a fabricated zero.
func TestNormalizeOmitsNullSecondaryWindow(t *testing.T) {
	out := Normalize(map[string]any{
		"hook_event_name": "Stop",
		"session_id":      "s1",
		"transcript_path": writeRollout(t, `{"plan_type":"free",`+
			`"primary":{"used_percent":5,"window_minutes":43200,"resets_at":1},"secondary":null}`),
	}, t.TempDir(), time.Now().UTC())
	require.NotNil(t, out)

	assert.Equal(t, 5.0, out["dash0.gen_ai.rate_limit.primary.used_percent"])
	for _, k := range []string{
		"dash0.gen_ai.rate_limit.secondary.used_percent",
		"dash0.gen_ai.rate_limit.secondary.window_minutes",
		"dash0.gen_ai.rate_limit.secondary.resets_at",
	} {
		_, present := out[k]
		assert.False(t, present, "%s must be absent when the plan reports no second window", k)
	}
}

// A compressed rollout is unreadable without a zstd dependency this module
// avoids, so we never learn the billing mode — and must not guess it. The span
// carries the reader diagnostic instead, keeping the gap visible in telemetry.
func TestNormalizeCompressedRolloutEmitsNoBillingMode(t *testing.T) {
	out := Normalize(map[string]any{
		"hook_event_name": "Stop",
		"session_id":      "s1",
		"transcript_path": "/home/u/.codex/sessions/rollout-x.jsonl.zst",
	}, t.TempDir(), time.Now().UTC())
	require.NotNil(t, out)

	assert.Equal(t, true, out["dash0.codex.rollout.compressed"])
	_, present := out["dash0.gen_ai.billing_mode"]
	assert.False(t, present, "an unreadable rollout tells us nothing, not \"unknown\"")
}

// A limit that has not been hit reports null, and a null throttle event is not
// an event — it must not appear on the span at all.
func TestNormalizeOmitsUnreachedThrottleAndAbsentBalance(t *testing.T) {
	out := Normalize(map[string]any{
		"hook_event_name": "Stop",
		"session_id":      "s1",
		"transcript_path": writeRollout(t, `{"plan_type":"free","primary":{"used_percent":5,"window_minutes":43200,"resets_at":1},`+
			`"rate_limit_reached_type":null,"credits":{"has_credits":false,"unlimited":false,"balance":null}}`),
	}, t.TempDir(), time.Now().UTC())
	require.NotNil(t, out)

	assert.Equal(t, "subscription", out["dash0.gen_ai.billing_mode"])
	assert.Equal(t, false, out["dash0.gen_ai.credits.available"])

	_, hasReached := out["dash0.gen_ai.rate_limit.reached_type"]
	assert.False(t, hasReached, "an unreached limit is not a throttle event")
	_, hasBalance := out["dash0.gen_ai.credits.balance"]
	assert.False(t, hasBalance, "an unreported balance is not a balance of zero")
}

func cloneMap(m map[string]any) map[string]any {
	out := make(map[string]any, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}
