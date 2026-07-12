package codex

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Expected aggregates are derived from the real token_count deltas in each
// bundled fixture: input = Σ(input_tokens − cached_input_tokens),
// cache_read = Σ cached_input_tokens, output = Σ output_tokens.
func TestReadTurnUsageOverFixtures(t *testing.T) {
	cases := []struct {
		file       string
		wantInput  int64
		wantCache  int64
		wantOut    int64
		wantReason int64
	}{
		// Main session, 3 calls: (11182-9088)+(11575-9088)+(11708-11136)=5153
		{"rollout-2026-07-07T12-28-09-019f3be8-053a-78c3-9096-e9ab264c13a0.jsonl", 5153, 29312, 161, 0},
		// Second main session, 3 calls.
		{"rollout-2026-07-07T12-37-33-019f3bf0-9fe5-7821-b583-cd99b1eb0738.jsonl", 5929, 29824, 127, 0},
		// Orchestrator main session, 8 calls, with reasoning tokens.
		{"rollout-2026-07-07T12-40-19-019f3bf3-29e5-7320-a40b-883e09c7601a.jsonl", 28979, 88576, 1500, 263},
		// Sub-agent rollouts (read via agent_transcript_path on SubagentStop).
		{"rollout-2026-07-07T12-40-33-019f3bf3-60f7-7db2-8a74-7cf0618742e6.jsonl", 4587, 18176, 67, 0},
		{"rollout-2026-07-07T12-40-33-019f3bf3-605d-7393-ac4f-63f8dcc20260.jsonl", 2977, 31360, 210, 0},
		{"rollout-2026-07-07T12-40-33-019f3bf3-5f80-7ca3-81a0-298149d46129.jsonl", 2972, 31360, 215, 0},
	}
	for _, tc := range cases {
		t.Run(tc.file, func(t *testing.T) {
			u, err := ReadTurnUsage(filepath.Join("testdata", "rollouts", tc.file))
			require.NoError(t, err)
			require.NotNil(t, u)
			assert.Equal(t, tc.wantInput, u.InputTokens, "input (uncached)")
			assert.Equal(t, tc.wantCache, u.CacheReadInputTokens, "cache_read")
			assert.Equal(t, tc.wantOut, u.OutputTokens, "output")
			assert.Equal(t, tc.wantReason, u.ReasoningOutputTokens, "reasoning")
			// input and cache_read must stay disjoint (no double counting).
			assert.GreaterOrEqual(t, u.InputTokens, int64(0))
		})
	}
}

// A user_message mid-file starts a new turn; only usage after the last one counts.
func TestReadTurnUsageScopesToLastTurn(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "rollout.jsonl")
	content := "" +
		`{"type":"event_msg","payload":{"type":"user_message","message":"turn 1"}}` + "\n" +
		`{"type":"event_msg","payload":{"type":"token_count","info":{"last_token_usage":{"input_tokens":500,"cached_input_tokens":100,"output_tokens":40}}}}` + "\n" +
		`{"type":"event_msg","payload":{"type":"user_message","message":"turn 2"}}` + "\n" +
		`{"type":"event_msg","payload":{"type":"token_count","info":{"last_token_usage":{"input_tokens":300,"cached_input_tokens":100,"output_tokens":10}}}}` + "\n" +
		`{"type":"event_msg","payload":{"type":"token_count","info":{"last_token_usage":{"input_tokens":250,"cached_input_tokens":50,"output_tokens":5}}}}` + "\n"
	require.NoError(t, os.WriteFile(path, []byte(content), 0o644))

	u, err := ReadTurnUsage(path)
	require.NoError(t, err)
	require.NotNil(t, u)
	// Only turn 2: input (300-100)+(250-50)=400, cache 100+50=150, output 10+5=15.
	assert.Equal(t, int64(400), u.InputTokens)
	assert.Equal(t, int64(150), u.CacheReadInputTokens)
	assert.Equal(t, int64(15), u.OutputTokens)
}

// A rollout with no token_count events yields (nil, nil) so the caller emits the
// span without token attributes.
func TestReadTurnUsageNoTokenCounts(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "rollout.jsonl")
	require.NoError(t, os.WriteFile(path,
		[]byte(`{"type":"event_msg","payload":{"type":"user_message","message":"hi"}}`+"\n"), 0o644))

	u, err := ReadTurnUsage(path)
	require.NoError(t, err)
	assert.Nil(t, u)
}

// A cached count exceeding input never produces a negative input contribution.
func TestReadTurnUsageClampsNegativeInput(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "rollout.jsonl")
	require.NoError(t, os.WriteFile(path,
		[]byte(`{"type":"event_msg","payload":{"type":"token_count","info":{"last_token_usage":{"input_tokens":80,"cached_input_tokens":100,"output_tokens":5}}}}`+"\n"), 0o644))

	u, err := ReadTurnUsage(path)
	require.NoError(t, err)
	require.NotNil(t, u)
	assert.Equal(t, int64(0), u.InputTokens)
	assert.Equal(t, int64(100), u.CacheReadInputTokens)
}

// A .zst path is skipped (unsupported) without error so the span still emits.
func TestReadTurnUsageSkipsCompressed(t *testing.T) {
	u, err := ReadTurnUsage(filepath.Join("testdata", "rollouts", "does-not-matter.jsonl.zst"))
	require.NoError(t, err)
	assert.Nil(t, u)
}

// A missing (non-.zst) rollout is a real error the caller logs.
func TestReadTurnUsageMissingFileErrors(t *testing.T) {
	_, err := ReadTurnUsage(filepath.Join("testdata", "rollouts", "no-such-rollout.jsonl"))
	assert.Error(t, err)
}
