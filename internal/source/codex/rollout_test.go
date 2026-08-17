package codex

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Expected aggregates are the raw sums of the real token_count deltas in each
// bundled fixture: input = Σ input_tokens (inclusive of cache), cache_read =
// Σ cached_input_tokens, output = Σ output_tokens. The cost processor derives
// the uncached input itself, so the reader must NOT pre-subtract cache.
func TestReadTurnUsageOverFixtures(t *testing.T) {
	cases := []struct {
		file       string
		wantInput  int64
		wantCache  int64
		wantOut    int64
		wantReason int64
	}{
		// Main session, 3 calls: 11182+11575+11708 = 34465 (inclusive of cache).
		{"rollout-2026-07-07T12-28-09-019f3be8-053a-78c3-9096-e9ab264c13a0.jsonl", 34465, 29312, 161, 0},
		// Second main session, 3 calls.
		{"rollout-2026-07-07T12-37-33-019f3bf0-9fe5-7821-b583-cd99b1eb0738.jsonl", 35753, 29824, 127, 0},
		// Orchestrator main session, 8 calls, with reasoning tokens.
		{"rollout-2026-07-07T12-40-19-019f3bf3-29e5-7320-a40b-883e09c7601a.jsonl", 117555, 88576, 1500, 263},
		// Sub-agent rollouts (read via agent_transcript_path on SubagentStop).
		{"rollout-2026-07-07T12-40-33-019f3bf3-60f7-7db2-8a74-7cf0618742e6.jsonl", 22763, 18176, 67, 0},
		{"rollout-2026-07-07T12-40-33-019f3bf3-605d-7393-ac4f-63f8dcc20260.jsonl", 34337, 31360, 210, 0},
		{"rollout-2026-07-07T12-40-33-019f3bf3-5f80-7ca3-81a0-298149d46129.jsonl", 34332, 31360, 215, 0},
	}
	for _, tc := range cases {
		t.Run(tc.file, func(t *testing.T) {
			u, err := ReadTurnUsage(filepath.Join("testdata", "rollouts", tc.file))
			require.NoError(t, err)
			require.NotNil(t, u)
			assert.Equal(t, tc.wantInput, u.InputTokens, "input (inclusive of cache)")
			assert.Equal(t, tc.wantCache, u.CacheReadInputTokens, "cache_read")
			assert.Equal(t, tc.wantOut, u.OutputTokens, "output")
			assert.Equal(t, tc.wantReason, u.ReasoningOutputTokens, "reasoning")
			// cache_read is a subset of the (inclusive) input count.
			assert.LessOrEqual(t, u.CacheReadInputTokens, u.InputTokens)
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
	// Only turn 2: input 300+250=550 (inclusive), cache 100+50=150, output 10+5=15.
	assert.Equal(t, int64(550), u.InputTokens)
	assert.Equal(t, int64(150), u.CacheReadInputTokens)
	assert.Equal(t, int64(15), u.OutputTokens)
}

// Tool activity within a turn is recorded as response_item (function_call /
// function_call_output) records interleaved between token_count events. These
// are not turn boundaries and must NOT reset the accumulator, otherwise the
// tool round-trips' usage is discarded (the Codex analog of the Claude
// skill/tool_result reset bug). Only a real user_message starts a new turn.
func TestReadTurnUsageToolCallsDoNotResetTurn(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "rollout.jsonl")
	content := "" +
		`{"type":"event_msg","payload":{"type":"user_message","message":"run the tool"}}` + "\n" +
		`{"type":"event_msg","payload":{"type":"token_count","info":{"last_token_usage":{"input_tokens":500,"cached_input_tokens":100,"output_tokens":40}}}}` + "\n" +
		`{"type":"response_item","payload":{"type":"function_call","name":"shell","call_id":"call_1","arguments":"{}"}}` + "\n" +
		`{"type":"response_item","payload":{"type":"function_call_output","call_id":"call_1","output":"ok"}}` + "\n" +
		`{"type":"event_msg","payload":{"type":"token_count","info":{"last_token_usage":{"input_tokens":300,"cached_input_tokens":50,"output_tokens":10}}}}` + "\n" +
		`{"type":"response_item","payload":{"type":"message","role":"assistant"}}` + "\n" +
		`{"type":"event_msg","payload":{"type":"token_count","info":{"last_token_usage":{"input_tokens":250,"cached_input_tokens":25,"output_tokens":5}}}}` + "\n"
	require.NoError(t, os.WriteFile(path, []byte(content), 0o644))

	u, err := ReadTurnUsage(path)
	require.NoError(t, err)
	require.NotNil(t, u)
	// All three round-trips of the single turn are summed; the tool records did
	// not reset. input 500+300+250=1050, cache 100+50+25=175, output 40+10+5=55.
	assert.Equal(t, int64(1050), u.InputTokens)
	assert.Equal(t, int64(175), u.CacheReadInputTokens)
	assert.Equal(t, int64(55), u.OutputTokens)
}

// A turn that ends on a tool round-trip with no trailing assistant message must
// still report usage (the Codex analog of a skill invoked without follow-up
// chat). The final token_count is retained rather than lost.
func TestReadTurnUsageToolEndedTurnRetainsUsage(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "rollout.jsonl")
	content := "" +
		`{"type":"event_msg","payload":{"type":"user_message","message":"run the tool"}}` + "\n" +
		`{"type":"event_msg","payload":{"type":"token_count","info":{"last_token_usage":{"input_tokens":400,"cached_input_tokens":80,"output_tokens":30}}}}` + "\n" +
		`{"type":"response_item","payload":{"type":"function_call","name":"shell","call_id":"call_1","arguments":"{}"}}` + "\n" +
		`{"type":"response_item","payload":{"type":"function_call_output","call_id":"call_1","output":"ok"}}` + "\n" +
		`{"type":"event_msg","payload":{"type":"token_count","info":{"last_token_usage":{"input_tokens":200,"cached_input_tokens":40,"output_tokens":12}}}}` + "\n"
	require.NoError(t, os.WriteFile(path, []byte(content), 0o644))

	u, err := ReadTurnUsage(path)
	require.NoError(t, err)
	require.NotNil(t, u)
	// Both round-trips summed: input 600, cache 120, output 42.
	assert.Equal(t, int64(600), u.InputTokens)
	assert.Equal(t, int64(120), u.CacheReadInputTokens)
	assert.Equal(t, int64(42), u.OutputTokens)
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

// rate_limits rides on the same token_count records as usage, but as a SIBLING
// of info rather than a child of it. It reports account-level allowance state
// (which plan, how much of it is consumed, when it resets) — not token counts.
func TestReadRolloutParsesRateLimits(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "rollout.jsonl")
	content := "" +
		`{"type":"event_msg","payload":{"type":"user_message","message":"hi"}}` + "\n" +
		`{"type":"event_msg","payload":{"type":"token_count","info":{"last_token_usage":{"input_tokens":100,"cached_input_tokens":10,"output_tokens":5}},"rate_limits":{"limit_id":"codex","primary":{"used_percent":29,"window_minutes":43200,"resets_at":1786008501},"plan_type":"plus"}}}` + "\n"
	require.NoError(t, os.WriteFile(path, []byte(content), 0o644))

	r, err := ReadRollout(path)
	require.NoError(t, err)
	require.NotNil(t, r)
	require.NotNil(t, r.Limits)
	assert.Equal(t, "plus", r.Limits.PlanType)
	require.NotNil(t, r.Limits.Primary)
	assert.Equal(t, 29.0, r.Limits.Primary.UsedPercent)
	assert.Equal(t, int64(43200), r.Limits.Primary.WindowMinutes)
	assert.Equal(t, int64(1786008501), r.Limits.Primary.ResetsAt)

	// The same pass still yields the turn's token usage.
	require.NotNil(t, r.Usage)
	assert.Equal(t, int64(100), r.Usage.InputTokens)
}

// Usage and limits have different lifetimes. A user_message starts a new turn and
// resets the token counts, but the allowance state describes the account, so the
// latest value seen survives the boundary rather than being discarded with it.
func TestReadRolloutLimitsSurviveTurnBoundaries(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "rollout.jsonl")
	content := "" +
		`{"type":"event_msg","payload":{"type":"user_message","message":"turn 1"}}` + "\n" +
		`{"type":"event_msg","payload":{"type":"token_count","info":{"last_token_usage":{"input_tokens":500,"output_tokens":40}},"rate_limits":{"primary":{"used_percent":5},"plan_type":"free"}}}` + "\n" +
		`{"type":"event_msg","payload":{"type":"user_message","message":"turn 2"}}` + "\n" +
		`{"type":"event_msg","payload":{"type":"token_count","info":{"last_token_usage":{"input_tokens":300,"output_tokens":10}},"rate_limits":{"primary":{"used_percent":40},"plan_type":"plus"}}}` + "\n"
	require.NoError(t, os.WriteFile(path, []byte(content), 0o644))

	r, err := ReadRollout(path)
	require.NoError(t, err)

	// Usage is scoped to the last turn only.
	require.NotNil(t, r.Usage)
	assert.Equal(t, int64(300), r.Usage.InputTokens)

	// Limits are the most recent seen, not reset by the turn boundary.
	require.NotNil(t, r.Limits)
	assert.Equal(t, "plus", r.Limits.PlanType)
	require.NotNil(t, r.Limits.Primary)
	assert.Equal(t, 40.0, r.Limits.Primary.UsedPercent)
}

// A turn that reports usage but no rate_limits (Codex CLI before the field
// landed, ~mid-July 2026) must leave Limits nil rather than zero-valued — 0%
// consumed on an empty plan is a meaningful-looking lie.
func TestReadRolloutNoRateLimitsLeavesLimitsNil(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "rollout.jsonl")
	content := "" +
		`{"type":"event_msg","payload":{"type":"user_message","message":"hi"}}` + "\n" +
		`{"type":"event_msg","payload":{"type":"token_count","info":{"last_token_usage":{"input_tokens":100,"output_tokens":5}}}}` + "\n"
	require.NoError(t, os.WriteFile(path, []byte(content), 0o644))

	r, err := ReadRollout(path)
	require.NoError(t, err)
	require.NotNil(t, r.Usage)
	assert.Nil(t, r.Limits)
}

// The rate_limits block routinely carries null sub-objects — `secondary` and
// `individual_limit` are null in every captured record — so `primary` being
// absent is a shape the schema permits. Flattening it would report "0% of your
// allowance consumed", which reads as a measurement rather than missing data.
func TestReadRolloutRateLimitsWithoutPrimaryWindow(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "rollout.jsonl")
	content := `{"type":"event_msg","payload":{"type":"token_count","info":{"last_token_usage":{"input_tokens":1}},"rate_limits":{"plan_type":"plus","primary":null}}}` + "\n"
	require.NoError(t, os.WriteFile(path, []byte(content), 0o644))

	r, err := ReadRollout(path)
	require.NoError(t, err)
	require.NotNil(t, r.Limits)

	// The plan is still known — only the window is missing.
	assert.Equal(t, "plus", r.Limits.PlanType)
	assert.Nil(t, r.Limits.Primary)
}

// primary and secondary are both RateLimitWindow in the Codex source, so they
// parse identically. Which duration lands in which slot is NOT fixed — read
// window_minutes to tell them apart rather than assuming one is the short one.
//
// secondary was null in all 76 captured events (a free-plan account), so the
// populated shape here comes from the type names embedded in the codex 0.142.5
// binary, not from observed data.
func TestReadRolloutParsesSecondaryWindow(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "rollout.jsonl")
	content := `{"type":"event_msg","payload":{"type":"token_count","info":{"last_token_usage":{"input_tokens":1}},` +
		`"rate_limits":{"plan_type":"pro",` +
		`"primary":{"used_percent":29,"window_minutes":43200,"resets_at":1786008501},` +
		`"secondary":{"used_percent":80,"window_minutes":300,"resets_at":1786000000}}}}` + "\n"
	require.NoError(t, os.WriteFile(path, []byte(content), 0o644))

	r, err := ReadRollout(path)
	require.NoError(t, err)
	require.NotNil(t, r.Limits)

	require.NotNil(t, r.Limits.Primary)
	assert.Equal(t, 29.0, r.Limits.Primary.UsedPercent)
	assert.Equal(t, int64(43200), r.Limits.Primary.WindowMinutes)

	require.NotNil(t, r.Limits.Secondary)
	assert.Equal(t, 80.0, r.Limits.Secondary.UsedPercent)
	assert.Equal(t, int64(300), r.Limits.Secondary.WindowMinutes)
	assert.Equal(t, int64(1786000000), r.Limits.Secondary.ResetsAt)
}

// The common real shape: a plan reporting one window and leaving the other null.
func TestReadRolloutNullSecondaryLeavesItNil(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "rollout.jsonl")
	content := `{"type":"event_msg","payload":{"type":"token_count","info":{"last_token_usage":{"input_tokens":1}},` +
		`"rate_limits":{"plan_type":"free","primary":{"used_percent":5,"window_minutes":43200,"resets_at":1},"secondary":null}}}` + "\n"
	require.NoError(t, os.WriteFile(path, []byte(content), 0o644))

	r, err := ReadRollout(path)
	require.NoError(t, err)
	require.NotNil(t, r.Limits)
	require.NotNil(t, r.Limits.Primary)
	assert.Nil(t, r.Limits.Secondary)
}

// credits is version-gated: rollouts before ~14 Jul 2026 report it as null.
// Balance is nullable independently of that, so "no balance reported" and "a
// balance of zero" must stay distinguishable — hence a pointer, not a float.
func TestReadRolloutParsesCredits(t *testing.T) {
	cases := []struct {
		name        string
		rateLimits  string
		wantNil     bool
		wantHas     bool
		wantUnlim   bool
		wantBalance *float64
	}{
		{
			name:       "credits null (pre-July CLI)",
			rateLimits: `{"plan_type":"free","credits":null}`,
			wantNil:    true,
		},
		{
			name:       "no credits held",
			rateLimits: `{"plan_type":"free","credits":{"has_credits":false,"unlimited":false,"balance":null}}`,
			wantHas:    false,
		},
		{
			name:        "credits held with a balance",
			rateLimits:  `{"plan_type":"pro","credits":{"has_credits":true,"unlimited":false,"balance":12.5}}`,
			wantHas:     true,
			wantBalance: floatPtr(12.5),
		},
		{
			name:        "zero balance is not the same as no balance",
			rateLimits:  `{"plan_type":"pro","credits":{"has_credits":true,"unlimited":false,"balance":0}}`,
			wantHas:     true,
			wantBalance: floatPtr(0.0),
		},
		{
			name:       "unlimited",
			rateLimits: `{"plan_type":"business","credits":{"has_credits":true,"unlimited":true,"balance":null}}`,
			wantHas:    true,
			wantUnlim:  true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "rollout.jsonl")
			content := `{"type":"event_msg","payload":{"type":"token_count","info":{"last_token_usage":{"input_tokens":1}},"rate_limits":` + tc.rateLimits + `}}` + "\n"
			require.NoError(t, os.WriteFile(path, []byte(content), 0o644))

			r, err := ReadRollout(path)
			require.NoError(t, err)
			require.NotNil(t, r.Limits)

			if tc.wantNil {
				assert.Nil(t, r.Limits.Credits)
				return
			}
			require.NotNil(t, r.Limits.Credits)
			assert.Equal(t, tc.wantHas, r.Limits.Credits.HasCredits)
			assert.Equal(t, tc.wantUnlim, r.Limits.Credits.Unlimited)
			assert.Equal(t, tc.wantBalance, r.Limits.Credits.Balance)
		})
	}
}

// rate_limit_reached_type is null until a limit is actually hit, at which point
// it names which window blocked. Never observed populated in captured sessions,
// so the non-null shape here is the documented field, not a verified sample.
func TestReadRolloutParsesReachedType(t *testing.T) {
	cases := []struct{ name, rateLimits, want string }{
		{"not reached", `{"plan_type":"free","rate_limit_reached_type":null}`, ""},
		{"reached", `{"plan_type":"plus","rate_limit_reached_type":"primary"}`, "primary"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "rollout.jsonl")
			content := `{"type":"event_msg","payload":{"type":"token_count","info":{"last_token_usage":{"input_tokens":1}},"rate_limits":` + tc.rateLimits + `}}` + "\n"
			require.NoError(t, os.WriteFile(path, []byte(content), 0o644))

			r, err := ReadRollout(path)
			require.NoError(t, err)
			require.NotNil(t, r.Limits)
			assert.Equal(t, tc.want, r.Limits.ReachedType)
		})
	}
}

// Never returns "api": an absent plan is consistent with API-key auth but does
// not prove it (SIG-189), and claiming "api" would assert the cost figure is
// real spend — the exact error this work exists to stop.
func TestBillingMode(t *testing.T) {
	cases := []struct {
		name   string
		limits *Limits
		want   string
	}{
		{"no rate_limits at all (pre-July CLI)", nil, "unknown"},
		{"plan reported", &Limits{PlanType: "plus"}, "subscription"},
		// Free pays nothing, but the marginal token is still not priced, so the
		// cost figure is just as much a counterfactual as on a paid plan.
		{"free is still not per-token billed", &Limits{PlanType: "free"}, "subscription"},
		// Limits present but no plan named: consistent with API-key auth, unproven.
		{"limits without a plan", &Limits{Primary: &Window{UsedPercent: 5}}, "unknown"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, tc.limits.BillingMode())
		})
	}
}

func floatPtr(v float64) *float64 { return &v }
