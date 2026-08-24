// SPDX-FileCopyrightText: Copyright 2026 Dash0 Inc.
// SPDX-License-Identifier: Apache-2.0

package transcript

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func writeTranscript(t *testing.T, path string, lines []string) {
	t.Helper()
	require.NoError(t, os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o644))
}

func TestReadTurnUsageSingleAssistant(t *testing.T) {
	path := filepath.Join(t.TempDir(), "transcript.jsonl")
	writeTranscript(t, path, []string{
		`{"type":"user","message":{"role":"user","content":[{"type":"text","text":"hello"}]}}`,
		`{"type":"assistant","requestId":"req_001","message":{"role":"assistant","content":[{"type":"text","text":"hi"}],"usage":{"input_tokens":100,"output_tokens":50,"cache_creation_input_tokens":200,"cache_read_input_tokens":300}}}`,
	})

	usage, err := ReadTurnUsage(path)
	require.NoError(t, err)
	require.NotNil(t, usage)

	assert.Equal(t, int64(100), usage.InputTokens)
	assert.Equal(t, int64(50), usage.OutputTokens)
	assert.Equal(t, int64(200), usage.CacheCreationInputTokens)
	assert.Equal(t, int64(300), usage.CacheReadInputTokens)
}

func TestReadTurnUsageDeduplicatesRequestID(t *testing.T) {
	path := filepath.Join(t.TempDir(), "transcript.jsonl")
	writeTranscript(t, path, []string{
		`{"type":"user","message":{"role":"user","content":[{"type":"text","text":"hello"}]}}`,
		// Streaming split: thinking block first (partial output_tokens).
		`{"type":"assistant","requestId":"req_001","message":{"role":"assistant","content":[{"type":"thinking","thinking":"..."}],"usage":{"input_tokens":100,"output_tokens":1,"cache_creation_input_tokens":0,"cache_read_input_tokens":0}}}`,
		// Same requestId: text block (final output_tokens).
		`{"type":"assistant","requestId":"req_001","message":{"role":"assistant","content":[{"type":"text","text":"hi"}],"usage":{"input_tokens":100,"output_tokens":150,"cache_creation_input_tokens":0,"cache_read_input_tokens":0}}}`,
	})

	usage, err := ReadTurnUsage(path)
	require.NoError(t, err)
	require.NotNil(t, usage)

	// Counted only once despite two entries with same requestId.
	// Uses the LAST entry's values (final streaming state).
	assert.Equal(t, int64(100), usage.InputTokens)
	assert.Equal(t, int64(150), usage.OutputTokens)
}

func TestReadTurnUsageAggregatesMultipleIterations(t *testing.T) {
	path := filepath.Join(t.TempDir(), "transcript.jsonl")
	writeTranscript(t, path, []string{
		`{"type":"user","message":{"role":"user","content":[{"type":"text","text":"do something"}]}}`,
		// First iteration.
		`{"type":"assistant","requestId":"req_001","message":{"role":"assistant","content":[{"type":"text","text":"ok"}],"usage":{"input_tokens":100,"output_tokens":50,"cache_creation_input_tokens":10,"cache_read_input_tokens":20}}}`,
		// Second iteration (after tool use).
		`{"type":"assistant","requestId":"req_002","message":{"role":"assistant","content":[{"type":"text","text":"done"}],"usage":{"input_tokens":200,"output_tokens":80,"cache_creation_input_tokens":30,"cache_read_input_tokens":40}}}`,
	})

	usage, err := ReadTurnUsage(path)
	require.NoError(t, err)
	require.NotNil(t, usage)

	assert.Equal(t, int64(300), usage.InputTokens)
	assert.Equal(t, int64(130), usage.OutputTokens)
	assert.Equal(t, int64(40), usage.CacheCreationInputTokens)
	assert.Equal(t, int64(60), usage.CacheReadInputTokens)
}

// fallbackUsage is a two-iteration usage payload modeled on a real fallback
// request (Fable attempt, then Opus fallback): the top-level fields mirror only
// the final iteration.
const fallbackUsage = `{"input_tokens":5607,"output_tokens":698,"cache_creation_input_tokens":100,"cache_read_input_tokens":2000,"cache_creation":{"ephemeral_1h_input_tokens":100,"ephemeral_5m_input_tokens":0},"iterations":[{"type":"message","model":"claude-fable-5","input_tokens":5607,"output_tokens":2,"cache_creation_input_tokens":50,"cache_read_input_tokens":1000},{"type":"fallback_message","model":"claude-opus-4-8","input_tokens":5607,"output_tokens":698,"cache_creation_input_tokens":100,"cache_read_input_tokens":2000}]}`

func TestReadTurnUsageSumsFallbackIterations(t *testing.T) {
	path := filepath.Join(t.TempDir(), "transcript.jsonl")
	writeTranscript(t, path, []string{
		`{"type":"user","message":{"role":"user","content":[{"type":"text","text":"hello"}]}}`,
		`{"type":"assistant","requestId":"req_001","message":{"role":"assistant","content":[{"type":"text","text":"hi"}],"usage":` + fallbackUsage + `}}`,
	})

	usage, err := ReadTurnUsage(path)
	require.NoError(t, err)
	require.NotNil(t, usage)

	// Both iterations are billed, so all fields are summed across them.
	assert.Equal(t, int64(11214), usage.InputTokens)
	assert.Equal(t, int64(700), usage.OutputTokens)
	assert.Equal(t, int64(150), usage.CacheCreationInputTokens)
	assert.Equal(t, int64(3000), usage.CacheReadInputTokens)

	// The TTL breakdown is top-level only (Anthropic doesn't split it per
	// iteration), so it reflects the final iteration and best-effort under-sums
	// the flat total (100 of 150) — the flat total stays authoritative.
	assert.Equal(t, int64(0), usage.CacheCreation5mInputTokens)
	assert.Equal(t, int64(100), usage.CacheCreation1hInputTokens)
}

func TestReadTurnUsageCacheCreationTTLBreakdown(t *testing.T) {
	path := filepath.Join(t.TempDir(), "transcript.jsonl")
	writeTranscript(t, path, []string{
		`{"type":"user","message":{"role":"user","content":[{"type":"text","text":"hello"}]}}`,
		// Two API calls in the turn, each carrying the TTL split; both are summed.
		`{"type":"assistant","requestId":"req_001","message":{"role":"assistant","content":[{"type":"text","text":"a"}],"usage":{"input_tokens":100,"output_tokens":50,"cache_creation_input_tokens":200,"cache_read_input_tokens":0,"cache_creation":{"ephemeral_5m_input_tokens":50,"ephemeral_1h_input_tokens":150}}}}`,
		`{"type":"assistant","requestId":"req_002","message":{"role":"assistant","content":[{"type":"text","text":"b"}],"usage":{"input_tokens":100,"output_tokens":50,"cache_creation_input_tokens":100,"cache_read_input_tokens":0,"cache_creation":{"ephemeral_5m_input_tokens":0,"ephemeral_1h_input_tokens":100}}}}`,
	})

	usage, err := ReadTurnUsage(path)
	require.NoError(t, err)
	require.NotNil(t, usage)

	assert.Equal(t, int64(50), usage.CacheCreation5mInputTokens)
	assert.Equal(t, int64(250), usage.CacheCreation1hInputTokens)
	// The split decomposes the flat total exactly when every call carries it.
	assert.Equal(t, usage.CacheCreationInputTokens,
		usage.CacheCreation5mInputTokens+usage.CacheCreation1hInputTokens)
}

func TestReadTurnUsageNoCacheCreationTTL(t *testing.T) {
	path := filepath.Join(t.TempDir(), "transcript.jsonl")
	writeTranscript(t, path, []string{
		`{"type":"user","message":{"role":"user","content":[{"type":"text","text":"hello"}]}}`,
		// No cache_creation object (older CLI): the breakdown defaults to 0/0
		// even though cache_creation_input_tokens is non-zero.
		`{"type":"assistant","requestId":"req_001","message":{"role":"assistant","content":[{"type":"text","text":"hi"}],"usage":{"input_tokens":100,"output_tokens":50,"cache_creation_input_tokens":200,"cache_read_input_tokens":300}}}`,
	})

	usage, err := ReadTurnUsage(path)
	require.NoError(t, err)
	require.NotNil(t, usage)

	assert.Equal(t, int64(0), usage.CacheCreation5mInputTokens)
	assert.Equal(t, int64(0), usage.CacheCreation1hInputTokens)
}

func TestReadTurnUsageSingleIterationMatchesTopLevel(t *testing.T) {
	path := filepath.Join(t.TempDir(), "transcript.jsonl")
	writeTranscript(t, path, []string{
		`{"type":"user","message":{"role":"user","content":[{"type":"text","text":"hello"}]}}`,
		// The single iteration duplicates the top-level fields, as observed in
		// real transcripts; behavior must be identical to no iterations at all.
		`{"type":"assistant","requestId":"req_001","message":{"role":"assistant","content":[{"type":"text","text":"hi"}],"usage":{"input_tokens":100,"output_tokens":50,"cache_creation_input_tokens":200,"cache_read_input_tokens":300,"iterations":[{"type":"message","input_tokens":100,"output_tokens":50,"cache_creation_input_tokens":200,"cache_read_input_tokens":300}]}}}`,
	})

	usage, err := ReadTurnUsage(path)
	require.NoError(t, err)
	require.NotNil(t, usage)

	assert.Equal(t, int64(100), usage.InputTokens)
	assert.Equal(t, int64(50), usage.OutputTokens)
	assert.Equal(t, int64(200), usage.CacheCreationInputTokens)
	assert.Equal(t, int64(300), usage.CacheReadInputTokens)
}

func TestReadTurnUsageDeduplicatesFallbackIterations(t *testing.T) {
	path := filepath.Join(t.TempDir(), "transcript.jsonl")
	writeTranscript(t, path, []string{
		`{"type":"user","message":{"role":"user","content":[{"type":"text","text":"hello"}]}}`,
		// Streaming split: both entries repeat the same usage incl. iterations.
		`{"type":"assistant","requestId":"req_001","message":{"role":"assistant","content":[{"type":"thinking","thinking":"..."}],"usage":` + fallbackUsage + `}}`,
		`{"type":"assistant","requestId":"req_001","message":{"role":"assistant","content":[{"type":"text","text":"hi"}],"usage":` + fallbackUsage + `}}`,
	})

	usage, err := ReadTurnUsage(path)
	require.NoError(t, err)
	require.NotNil(t, usage)

	// Iterations are summed once per requestId, not once per entry.
	assert.Equal(t, int64(11214), usage.InputTokens)
	assert.Equal(t, int64(700), usage.OutputTokens)
}

func TestReadTurnUsageEmptyIterations(t *testing.T) {
	path := filepath.Join(t.TempDir(), "transcript.jsonl")
	writeTranscript(t, path, []string{
		`{"type":"user","message":{"role":"user","content":[{"type":"text","text":"hello"}]}}`,
		`{"type":"assistant","requestId":"req_001","message":{"role":"assistant","content":[{"type":"text","text":"hi"}],"usage":{"input_tokens":100,"output_tokens":50,"cache_creation_input_tokens":0,"cache_read_input_tokens":0,"iterations":[]}}}`,
	})

	usage, err := ReadTurnUsage(path)
	require.NoError(t, err)
	require.NotNil(t, usage)

	assert.Equal(t, int64(100), usage.InputTokens)
	assert.Equal(t, int64(50), usage.OutputTokens)
}

func TestReadTurnUsageIgnoresPreviousTurns(t *testing.T) {
	path := filepath.Join(t.TempDir(), "transcript.jsonl")
	writeTranscript(t, path, []string{
		// First turn.
		`{"type":"user","message":{"role":"user","content":[{"type":"text","text":"first"}]}}`,
		`{"type":"assistant","requestId":"req_001","message":{"role":"assistant","content":[{"type":"text","text":"resp1"}],"usage":{"input_tokens":1000,"output_tokens":500,"cache_creation_input_tokens":0,"cache_read_input_tokens":0}}}`,
		// Second turn — only this should be counted.
		`{"type":"user","message":{"role":"user","content":[{"type":"text","text":"second"}]}}`,
		`{"type":"assistant","requestId":"req_002","message":{"role":"assistant","content":[{"type":"text","text":"resp2"}],"usage":{"input_tokens":100,"output_tokens":50,"cache_creation_input_tokens":0,"cache_read_input_tokens":0}}}`,
	})

	usage, err := ReadTurnUsage(path)
	require.NoError(t, err)
	require.NotNil(t, usage)

	assert.Equal(t, int64(100), usage.InputTokens)
	assert.Equal(t, int64(50), usage.OutputTokens)
}

func TestReadTurnUsageSkipsReplayedHistory(t *testing.T) {
	// Modeled on a real multi-day session: after the current turn, Claude Code
	// re-appends entries of an earlier turn — same message id, original
	// timestamps, original usage — fronted by a tool_result relay that does not
	// close the turn. That history was already billed on the earlier turn's
	// span and must not be counted again.
	path := filepath.Join(t.TempDir(), "transcript.jsonl")
	writeTranscript(t, path, []string{
		`{"type":"user","message":{"role":"user","content":"first prompt"},"timestamp":"2026-07-14T11:00:00.000Z"}`,
		`{"type":"assistant","message":{"id":"msg_old","role":"assistant","content":[{"type":"text","text":"old"}],"usage":{"input_tokens":2,"output_tokens":1000,"cache_creation_input_tokens":5000,"cache_read_input_tokens":900000}},"timestamp":"2026-07-14T11:00:05.000Z"}`,
		`{"type":"user","message":{"role":"user","content":"second prompt"},"timestamp":"2026-07-17T12:12:53.246Z"}`,
		`{"type":"assistant","message":{"id":"msg_new","role":"assistant","content":[{"type":"text","text":"new"}],"usage":{"input_tokens":1,"output_tokens":20,"cache_creation_input_tokens":100,"cache_read_input_tokens":200}},"timestamp":"2026-07-17T12:13:44.905Z"}`,
		// Replayed history, appended after the current turn.
		`{"type":"user","message":{"role":"user","content":[{"tool_use_id":"toolu_old","type":"tool_result","content":"old output"}]},"timestamp":"2026-07-14T11:00:03.000Z"}`,
		`{"type":"assistant","message":{"id":"msg_old","role":"assistant","content":[{"type":"text","text":"old"}],"usage":{"input_tokens":2,"output_tokens":1000,"cache_creation_input_tokens":5000,"cache_read_input_tokens":900000}},"timestamp":"2026-07-14T11:00:05.000Z"}`,
	})

	usage, err := ReadTurnUsage(path)
	require.NoError(t, err)
	require.NotNil(t, usage)

	assert.Equal(t, int64(1), usage.InputTokens)
	assert.Equal(t, int64(20), usage.OutputTokens)
	assert.Equal(t, int64(100), usage.CacheCreationInputTokens)
	assert.Equal(t, int64(200), usage.CacheReadInputTokens)
}

func TestReadTurnUsageReplayOnlyTurnReportsNoUsage(t *testing.T) {
	// A turn whose only usage entries are replayed history made no new API
	// call, so there is nothing to report — better than claiming zero tokens.
	path := filepath.Join(t.TempDir(), "transcript.jsonl")
	writeTranscript(t, path, []string{
		`{"type":"user","message":{"role":"user","content":"first prompt"}}`,
		`{"type":"assistant","message":{"id":"msg_old","role":"assistant","content":[{"type":"text","text":"old"}],"usage":{"input_tokens":2,"output_tokens":1000,"cache_creation_input_tokens":0,"cache_read_input_tokens":900000}}}`,
		`{"type":"user","message":{"role":"user","content":"second prompt"}}`,
		`{"type":"assistant","message":{"id":"msg_old","role":"assistant","content":[{"type":"text","text":"old"}],"usage":{"input_tokens":2,"output_tokens":1000,"cache_creation_input_tokens":0,"cache_read_input_tokens":900000}}}`,
	})

	usage, err := ReadTurnUsage(path)
	require.NoError(t, err)
	assert.Nil(t, usage)
}

func TestReadTurnUsageKeepsLateFlushedTailOfPreviousTurn(t *testing.T) {
	// The tail of a turn whose transcript flush lost the race with the next
	// prompt: older timestamp, but never counted before (no earlier occurrence
	// of msg_late in the file). It stays attributed to the current turn so the
	// session total remains whole.
	path := filepath.Join(t.TempDir(), "transcript.jsonl")
	writeTranscript(t, path, []string{
		`{"type":"user","message":{"role":"user","content":"first prompt"},"timestamp":"2026-07-17T12:00:00.000Z"}`,
		`{"type":"assistant","message":{"id":"msg_first","role":"assistant","content":[{"type":"text","text":"a"}],"usage":{"input_tokens":1,"output_tokens":10,"cache_creation_input_tokens":0,"cache_read_input_tokens":0}},"timestamp":"2026-07-17T12:00:05.000Z"}`,
		`{"type":"user","message":{"role":"user","content":"second prompt"},"timestamp":"2026-07-17T12:01:00.000Z"}`,
		// Flushed after the new prompt, but belongs to (and was never counted
		// for) the first turn.
		`{"type":"assistant","message":{"id":"msg_late","role":"assistant","content":[{"type":"text","text":"b"}],"usage":{"input_tokens":3,"output_tokens":30,"cache_creation_input_tokens":0,"cache_read_input_tokens":0}},"timestamp":"2026-07-17T12:00:59.000Z"}`,
		`{"type":"assistant","message":{"id":"msg_second","role":"assistant","content":[{"type":"text","text":"c"}],"usage":{"input_tokens":5,"output_tokens":50,"cache_creation_input_tokens":0,"cache_read_input_tokens":0}},"timestamp":"2026-07-17T12:01:05.000Z"}`,
	})

	usage, err := ReadTurnUsage(path)
	require.NoError(t, err)
	require.NotNil(t, usage)

	assert.Equal(t, int64(8), usage.InputTokens)
	assert.Equal(t, int64(80), usage.OutputTokens)
}

func TestReadTurnUsageCountsRepeatedCallInSameTurnOnce(t *testing.T) {
	// Guard the dedup scope: a call replayed WITHIN the turn that first made it
	// must still be counted (once), not skipped as spent history.
	path := filepath.Join(t.TempDir(), "transcript.jsonl")
	writeTranscript(t, path, []string{
		`{"type":"user","message":{"role":"user","content":"only prompt"}}`,
		`{"type":"assistant","message":{"id":"msg_a","role":"assistant","content":[{"type":"thinking","thinking":"..."}],"usage":{"input_tokens":7,"output_tokens":1,"cache_creation_input_tokens":0,"cache_read_input_tokens":0}}}`,
		`{"type":"user","message":{"role":"user","content":[{"tool_use_id":"toolu_1","type":"tool_result","content":"out"}]}}`,
		`{"type":"assistant","message":{"id":"msg_a","role":"assistant","content":[{"type":"text","text":"done"}],"usage":{"input_tokens":7,"output_tokens":70,"cache_creation_input_tokens":0,"cache_read_input_tokens":0}}}`,
	})

	usage, err := ReadTurnUsage(path)
	require.NoError(t, err)
	require.NotNil(t, usage)

	assert.Equal(t, int64(7), usage.InputTokens)
	assert.Equal(t, int64(70), usage.OutputTokens)
}

func TestReadTurnUsageResetsOnStringContentUserMessage(t *testing.T) {
	// Real Claude Code transcripts store typed prompts as a plain string in
	// message.content (not an array of blocks). These must reset the turn,
	// otherwise usage accumulates across the whole session.
	path := filepath.Join(t.TempDir(), "transcript.jsonl")
	writeTranscript(t, path, []string{
		`{"type":"user","message":{"role":"user","content":"first prompt"}}`,
		`{"type":"assistant","requestId":"req_001","message":{"role":"assistant","content":[{"type":"text","text":"resp1"}],"usage":{"input_tokens":1000,"output_tokens":500,"cache_creation_input_tokens":11,"cache_read_input_tokens":22}}}`,
		`{"type":"user","message":{"role":"user","content":"second prompt"}}`,
		`{"type":"assistant","requestId":"req_002","message":{"role":"assistant","content":[{"type":"text","text":"resp2"}],"usage":{"input_tokens":100,"output_tokens":50,"cache_creation_input_tokens":1,"cache_read_input_tokens":2}}}`,
	})

	usage, err := ReadTurnUsage(path)
	require.NoError(t, err)
	require.NotNil(t, usage)

	// Only the second turn is counted.
	assert.Equal(t, int64(100), usage.InputTokens)
	assert.Equal(t, int64(50), usage.OutputTokens)
	assert.Equal(t, int64(1), usage.CacheCreationInputTokens)
	assert.Equal(t, int64(2), usage.CacheReadInputTokens)
}

func TestReadTurnUsageStringContentTurnWithToolResults(t *testing.T) {
	path := filepath.Join(t.TempDir(), "transcript.jsonl")
	writeTranscript(t, path, []string{
		`{"type":"user","message":{"role":"user","content":"first prompt"}}`,
		`{"type":"assistant","requestId":"req_001","message":{"role":"assistant","content":[{"type":"text","text":"resp1"}],"usage":{"input_tokens":1000,"output_tokens":500}}}`,
		`{"type":"user","message":{"role":"user","content":"second prompt"}}`,
		`{"type":"assistant","requestId":"req_002","message":{"role":"assistant","content":[{"type":"text","text":"using tool"}],"usage":{"input_tokens":100,"output_tokens":50}}}`,
		// Tool result within the second turn — must NOT reset.
		`{"type":"user","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"tu1","content":"ok"}]}}`,
		`{"type":"assistant","requestId":"req_003","message":{"role":"assistant","content":[{"type":"text","text":"done"}],"usage":{"input_tokens":200,"output_tokens":80}}}`,
	})

	usage, err := ReadTurnUsage(path)
	require.NoError(t, err)
	require.NotNil(t, usage)

	// Both API calls of the second turn, nothing from the first.
	assert.Equal(t, int64(300), usage.InputTokens)
	assert.Equal(t, int64(130), usage.OutputTokens)
}

func TestReadTurnUsageIgnoresToolResultUser(t *testing.T) {
	path := filepath.Join(t.TempDir(), "transcript.jsonl")
	writeTranscript(t, path, []string{
		`{"type":"user","message":{"role":"user","content":[{"type":"text","text":"do it"}]}}`,
		// First assistant iteration.
		`{"type":"assistant","requestId":"req_001","message":{"role":"assistant","content":[{"type":"text","text":"using tool"}],"usage":{"input_tokens":100,"output_tokens":50,"cache_creation_input_tokens":0,"cache_read_input_tokens":0}}}`,
		// Tool result — this should NOT reset the turn.
		`{"type":"user","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"tu1","content":"ok"}]}}`,
		// Second assistant iteration — should be aggregated with the first.
		`{"type":"assistant","requestId":"req_002","message":{"role":"assistant","content":[{"type":"text","text":"done"}],"usage":{"input_tokens":200,"output_tokens":80,"cache_creation_input_tokens":0,"cache_read_input_tokens":0}}}`,
	})

	usage, err := ReadTurnUsage(path)
	require.NoError(t, err)
	require.NotNil(t, usage)

	// Both assistant entries aggregated.
	assert.Equal(t, int64(300), usage.InputTokens)
	assert.Equal(t, int64(130), usage.OutputTokens)
}

func TestReadTurnUsageIgnoresMetaUserMessage(t *testing.T) {
	// The skill flow injects a meta user message (isMeta) mid-turn — e.g. the
	// "Skill /x is already loaded above" relay between the tool_use call and the
	// follow-up. It carries string content but is NOT a real prompt, so it must
	// NOT reset the turn, otherwise the skill-invocation API call's usage (often
	// the bulk of the turn) is discarded.
	path := filepath.Join(t.TempDir(), "transcript.jsonl")
	writeTranscript(t, path, []string{
		`{"type":"user","message":{"role":"user","content":"/endpoint-test"}}`,
		`{"type":"assistant","requestId":"req_001","message":{"role":"assistant","content":[{"type":"tool_use","name":"Skill","input":{"skill":"endpoint-test"}}],"usage":{"input_tokens":2,"output_tokens":87,"cache_creation_input_tokens":10513,"cache_read_input_tokens":20977}}}`,
		`{"type":"user","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"tu1","content":"Launching skill: endpoint-test"}]}}`,
		// Meta relay with string content — must NOT reset the turn.
		`{"type":"user","isMeta":true,"message":{"role":"user","content":"Skill /endpoint-test is already loaded above; instructions unchanged."}}`,
		`{"type":"assistant","requestId":"req_002","message":{"role":"assistant","content":[{"type":"text","text":"done"}],"usage":{"input_tokens":2,"output_tokens":75,"cache_creation_input_tokens":131,"cache_read_input_tokens":31490}}}`,
	})

	usage, err := ReadTurnUsage(path)
	require.NoError(t, err)
	require.NotNil(t, usage)

	// Both API calls of the turn are summed; the meta message did not reset.
	assert.Equal(t, int64(4), usage.InputTokens)
	assert.Equal(t, int64(162), usage.OutputTokens)
	assert.Equal(t, int64(10644), usage.CacheCreationInputTokens)
	assert.Equal(t, int64(52467), usage.CacheReadInputTokens)
}

func TestReadTurnUsageSkillOnlyWithoutTrailingChat(t *testing.T) {
	// A skill invoked with no follow-up assistant chat: the turn ends on the
	// injected meta relay. If that meta message reset the turn, hasUsage would
	// be false and usage would be lost entirely (the "no token usage" bug).
	path := filepath.Join(t.TempDir(), "transcript.jsonl")
	writeTranscript(t, path, []string{
		`{"type":"user","message":{"role":"user","content":"/endpoint-test"}}`,
		`{"type":"assistant","requestId":"req_001","message":{"role":"assistant","content":[{"type":"tool_use","name":"Skill","input":{"skill":"endpoint-test"}}],"usage":{"input_tokens":2,"output_tokens":87,"cache_creation_input_tokens":10513,"cache_read_input_tokens":20977}}}`,
		`{"type":"user","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"tu1","content":"Launching skill: endpoint-test"}]}}`,
		`{"type":"user","isMeta":true,"message":{"role":"user","content":"Skill /endpoint-test is already loaded above; instructions unchanged."}}`,
	})

	usage, err := ReadTurnUsage(path)
	require.NoError(t, err)
	require.NotNil(t, usage)

	// The skill-invocation call's usage is retained rather than lost to nil.
	assert.Equal(t, int64(2), usage.InputTokens)
	assert.Equal(t, int64(87), usage.OutputTokens)
	assert.Equal(t, int64(10513), usage.CacheCreationInputTokens)
	assert.Equal(t, int64(20977), usage.CacheReadInputTokens)
}

func TestTurnCompleteTerminalAssistant(t *testing.T) {
	path := filepath.Join(t.TempDir(), "transcript.jsonl")
	writeTranscript(t, path, []string{
		`{"type":"user","message":{"role":"user","content":"do it"}}`,
		`{"type":"assistant","requestId":"req_001","message":{"role":"assistant","stop_reason":"end_turn","content":[{"type":"text","text":"done"}],"usage":{"input_tokens":1,"output_tokens":2}}}`,
	})
	complete, err := TurnComplete(path)
	require.NoError(t, err)
	assert.True(t, complete)
}

func TestTurnCompleteMidToolUseIsIncomplete(t *testing.T) {
	// The transcript ends at a mid-turn tool_use entry (the final assistant
	// message has not been flushed yet). The turn is not complete, so the caller
	// waits rather than reading a partial usage total.
	path := filepath.Join(t.TempDir(), "transcript.jsonl")
	writeTranscript(t, path, []string{
		`{"type":"user","message":{"role":"user","content":"/skill"}}`,
		`{"type":"assistant","requestId":"req_001","message":{"role":"assistant","stop_reason":"tool_use","content":[{"type":"tool_use","name":"Skill"}],"usage":{"input_tokens":2,"output_tokens":87}}}`,
		`{"type":"user","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"tu1","content":"ok"}]}}`,
	})
	complete, err := TurnComplete(path)
	require.NoError(t, err)
	assert.False(t, complete)
}

func TestTurnCompleteScopesToCurrentTurn(t *testing.T) {
	// A previous turn ended in end_turn, then a new prompt arrived but its
	// assistant entries have not been flushed yet. The current turn has no
	// assistant message, so it is not complete — the previous turn's terminal
	// entry must not be mistaken for this turn's.
	path := filepath.Join(t.TempDir(), "transcript.jsonl")
	writeTranscript(t, path, []string{
		`{"type":"user","message":{"role":"user","content":"first"}}`,
		`{"type":"assistant","requestId":"req_001","message":{"role":"assistant","stop_reason":"end_turn","content":[{"type":"text","text":"a"}],"usage":{"input_tokens":1,"output_tokens":2}}}`,
		`{"type":"user","message":{"role":"user","content":"second"}}`,
	})
	complete, err := TurnComplete(path)
	require.NoError(t, err)
	assert.False(t, complete)
}

func TestTurnCompleteIgnoresMetaBoundary(t *testing.T) {
	// The injected meta relay (isMeta) must not be treated as a turn boundary,
	// so the terminal assistant entry after it still counts as the current turn.
	path := filepath.Join(t.TempDir(), "transcript.jsonl")
	writeTranscript(t, path, []string{
		`{"type":"user","message":{"role":"user","content":"/skill"}}`,
		`{"type":"assistant","requestId":"req_001","message":{"role":"assistant","stop_reason":"tool_use","content":[{"type":"tool_use","name":"Skill"}],"usage":{"input_tokens":2,"output_tokens":87}}}`,
		`{"type":"user","isMeta":true,"message":{"role":"user","content":"Skill /x is already loaded above; instructions unchanged."}}`,
		`{"type":"assistant","requestId":"req_002","message":{"role":"assistant","stop_reason":"end_turn","content":[{"type":"text","text":"done"}],"usage":{"input_tokens":2,"output_tokens":74}}}`,
	})
	complete, err := TurnComplete(path)
	require.NoError(t, err)
	assert.True(t, complete)
}

func TestTurnCompleteNoAssistant(t *testing.T) {
	path := filepath.Join(t.TempDir(), "transcript.jsonl")
	writeTranscript(t, path, []string{
		`{"type":"user","message":{"role":"user","content":"hi"}}`,
	})
	complete, err := TurnComplete(path)
	require.NoError(t, err)
	assert.False(t, complete)
}

func TestTurnCompleteMissingFile(t *testing.T) {
	_, err := TurnComplete(filepath.Join(t.TempDir(), "nope.jsonl"))
	assert.Error(t, err)
}

func TestReadTurnUsageMissingFile(t *testing.T) {
	usage, err := ReadTurnUsage(filepath.Join(t.TempDir(), "nonexistent.jsonl"))
	assert.Error(t, err)
	assert.Nil(t, usage)
}

func TestReadTurnUsageEmptyFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "transcript.jsonl")
	require.NoError(t, os.WriteFile(path, []byte(""), 0o644))

	usage, err := ReadTurnUsage(path)
	require.NoError(t, err)
	assert.Nil(t, usage)
}

func TestReadTurnUsageNoAssistantEntries(t *testing.T) {
	path := filepath.Join(t.TempDir(), "transcript.jsonl")
	writeTranscript(t, path, []string{
		`{"type":"user","message":{"role":"user","content":[{"type":"text","text":"hello"}]}}`,
	})

	usage, err := ReadTurnUsage(path)
	require.NoError(t, err)
	assert.Nil(t, usage)
}

func TestReadTurnUsagePrettyPrintedJSON(t *testing.T) {
	path := filepath.Join(t.TempDir(), "transcript.jsonl")
	content := `{
  "type": "user",
  "message": {
    "role": "user",
    "content": [{"type": "text", "text": "hello"}]
  }
}
{
  "type": "assistant",
  "requestId": "req_001",
  "message": {
    "role": "assistant",
    "content": [{"type": "text", "text": "hi"}],
    "usage": {
      "input_tokens": 42,
      "output_tokens": 17,
      "cache_creation_input_tokens": 0,
      "cache_read_input_tokens": 0
    }
  }
}
`
	require.NoError(t, os.WriteFile(path, []byte(content), 0o644))

	usage, err := ReadTurnUsage(path)
	require.NoError(t, err)
	require.NotNil(t, usage)

	assert.Equal(t, int64(42), usage.InputTokens)
	assert.Equal(t, int64(17), usage.OutputTokens)
}

func TestReadTurnUsagePartialUsageFields(t *testing.T) {
	path := filepath.Join(t.TempDir(), "transcript.jsonl")
	writeTranscript(t, path, []string{
		`{"type":"user","message":{"role":"user","content":[{"type":"text","text":"hello"}]}}`,
		`{"type":"assistant","requestId":"req_001","message":{"role":"assistant","content":[{"type":"text","text":"hi"}],"usage":{"input_tokens":100,"output_tokens":50}}}`,
	})

	usage, err := ReadTurnUsage(path)
	require.NoError(t, err)
	require.NotNil(t, usage)

	assert.Equal(t, int64(100), usage.InputTokens)
	assert.Equal(t, int64(50), usage.OutputTokens)
	assert.Equal(t, int64(0), usage.CacheCreationInputTokens)
	assert.Equal(t, int64(0), usage.CacheReadInputTokens)
}

func TestReadSessionTitleFound(t *testing.T) {
	path := filepath.Join(t.TempDir(), "transcript.jsonl")
	writeTranscript(t, path, []string{
		`{"type":"user","message":{"role":"user","content":[{"type":"text","text":"hello"}]}}`,
		`{"type":"assistant","requestId":"req_001","message":{"role":"assistant","content":[{"type":"text","text":"hi"}]}}`,
		`{"type":"custom-title","customTitle":"my session name","sessionId":"sess-1"}`,
	})

	assert.Equal(t, "my session name", ReadSessionTitle(path))
}

func TestReadSessionTitleUsesLatest(t *testing.T) {
	path := filepath.Join(t.TempDir(), "transcript.jsonl")
	writeTranscript(t, path, []string{
		`{"type":"custom-title","customTitle":"first name","sessionId":"sess-1"}`,
		`{"type":"user","message":{"role":"user","content":[{"type":"text","text":"rename"}]}}`,
		`{"type":"custom-title","customTitle":"renamed session","sessionId":"sess-1"}`,
	})

	assert.Equal(t, "renamed session", ReadSessionTitle(path))
}

func TestReadSessionTitleFallsBackToAITitle(t *testing.T) {
	path := filepath.Join(t.TempDir(), "transcript.jsonl")
	writeTranscript(t, path, []string{
		`{"type":"user","message":{"role":"user","content":[{"type":"text","text":"hello"}]}}`,
		`{"type":"ai-title","aiTitle":"first auto name","sessionId":"sess-1"}`,
		`{"type":"ai-title","aiTitle":"refined auto name","sessionId":"sess-1"}`,
	})

	assert.Equal(t, "refined auto name", ReadSessionTitle(path))
}

func TestReadSessionTitlePrefersCustomOverAI(t *testing.T) {
	path := filepath.Join(t.TempDir(), "transcript.jsonl")
	writeTranscript(t, path, []string{
		`{"type":"ai-title","aiTitle":"auto name","sessionId":"sess-1"}`,
		`{"type":"custom-title","customTitle":"renamed by user","sessionId":"sess-1"}`,
	})

	assert.Equal(t, "renamed by user", ReadSessionTitle(path))
}

func TestReadSessionTitleMissing(t *testing.T) {
	path := filepath.Join(t.TempDir(), "transcript.jsonl")
	writeTranscript(t, path, []string{
		`{"type":"user","message":{"role":"user","content":[{"type":"text","text":"hello"}]}}`,
		`{"type":"assistant","requestId":"req_001","message":{"role":"assistant","content":[{"type":"text","text":"hi"}]}}`,
	})

	assert.Empty(t, ReadSessionTitle(path))
}

func TestReadSessionTitleMissingFile(t *testing.T) {
	assert.Empty(t, ReadSessionTitle("/nonexistent/path.jsonl"))
}

func TestReadModelFound(t *testing.T) {
	path := filepath.Join(t.TempDir(), "transcript.jsonl")
	writeTranscript(t, path, []string{
		`{"type":"user","message":{"role":"user","content":[{"type":"text","text":"hello"}]}}`,
		`{"type":"assistant","requestId":"req_001","message":{"role":"assistant","model":"claude-sonnet-4-6","content":[{"type":"text","text":"hi"}]}}`,
	})

	assert.Equal(t, "claude-sonnet-4-6", ReadModel(path))
}

func TestReadModelUsesLatest(t *testing.T) {
	path := filepath.Join(t.TempDir(), "transcript.jsonl")
	writeTranscript(t, path, []string{
		`{"type":"assistant","requestId":"req_001","message":{"role":"assistant","model":"claude-sonnet-4-6","content":[{"type":"text","text":"hi"}]}}`,
		`{"type":"user","message":{"role":"user","content":[{"type":"text","text":"switch model"}]}}`,
		`{"type":"assistant","requestId":"req_002","message":{"role":"assistant","model":"claude-opus-4-7","content":[{"type":"text","text":"ok"}]}}`,
	})

	assert.Equal(t, "claude-opus-4-7", ReadModel(path))
}

func TestReadModelWaitsForTheCurrentTurn(t *testing.T) {
	path := filepath.Join(t.TempDir(), "transcript.jsonl")
	writeTranscript(t, path, []string{
		`{"type":"user","message":{"role":"user","content":"first turn"}}`,
		`{"type":"assistant","message":{"role":"assistant","model":"claude-sonnet-4-6","content":[{"type":"text","text":"done"}]}}`,
		`{"type":"user","message":{"role":"user","content":"second turn"}}`,
	})

	assert.Empty(t, ReadModel(path))
	assert.False(t, HasAssistantEntry(path))
}

func TestReadCurrentTurnModelReturnsOneSnapshot(t *testing.T) {
	path := filepath.Join(t.TempDir(), "transcript.jsonl")
	writeTranscript(t, path, []string{
		`{"type":"user","message":{"role":"user","content":"first turn"}}`,
		`{"type":"assistant","message":{"role":"assistant","model":"claude-sonnet-4-6","content":[{"type":"text","text":"done"}]}}`,
		`{"type":"user","message":{"role":"user","content":"second turn"}}`,
		`{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"streaming"}]}}`,
	})

	model, hasAssistant := ReadCurrentTurnModel(path)
	assert.Empty(t, model)
	assert.True(t, hasAssistant, "model and readiness come from the same current-turn scan")
}

func TestReadModelMissing(t *testing.T) {
	path := filepath.Join(t.TempDir(), "transcript.jsonl")
	writeTranscript(t, path, []string{
		`{"type":"user","message":{"role":"user","content":[{"type":"text","text":"hello"}]}}`,
	})

	assert.Empty(t, ReadModel(path))
}

func TestReadModelMissingFile(t *testing.T) {
	assert.Empty(t, ReadModel("/nonexistent/path.jsonl"))
}

// Claude Code writes no requestId when it reaches Claude through Amazon Bedrock:
// across the local transcripts, all 6011 first-party (msg_*) entries carry one
// and all 171 Bedrock (msg_bdrk_*) entries do not. The streaming entries of one
// API call each repeat that call's full usage, so without a per-call key the same
// tokens get counted once per entry — inflating every Bedrock-backed turn whose
// response spans more than one content block. message.id identifies the call and
// is present either way.
//
// The fixture mirrors a captured Bedrock sub-agent transcript: three entries, no
// requestId, the first two being one call (same message.id) that restates
// cache_creation 15643, and a third call adding 5762.
func TestReadTurnUsageDeduplicatesMessageIDWithoutRequestID(t *testing.T) {
	path := filepath.Join(t.TempDir(), "transcript.jsonl")
	writeTranscript(t, path, []string{
		`{"type":"user","message":{"role":"user","content":[{"type":"text","text":"count the files"}]}}`,
		`{"type":"assistant","message":{"id":"msg_a","role":"assistant","content":[{"type":"text","text":"..."}],"usage":{"input_tokens":2,"output_tokens":1,"cache_creation_input_tokens":15643,"cache_read_input_tokens":0}}}`,
		`{"type":"assistant","message":{"id":"msg_a","role":"assistant","content":[{"type":"tool_use","name":"Bash"}],"usage":{"input_tokens":2,"output_tokens":133,"cache_creation_input_tokens":15643,"cache_read_input_tokens":0}}}`,
		`{"type":"assistant","message":{"id":"msg_b","role":"assistant","content":[{"type":"text","text":"2 files"}],"usage":{"input_tokens":2,"output_tokens":139,"cache_creation_input_tokens":5762,"cache_read_input_tokens":15643}}}`,
	})

	usage, err := ReadTurnUsage(path)
	require.NoError(t, err)
	require.NotNil(t, usage)

	// Two API calls: msg_a counted once (last entry wins) plus msg_b.
	assert.Equal(t, int64(4), usage.InputTokens)
	assert.Equal(t, int64(272), usage.OutputTokens)
	assert.Equal(t, int64(21405), usage.CacheCreationInputTokens,
		"msg_a's cache_creation must be counted once, not once per streamed entry")
	assert.Equal(t, int64(15643), usage.CacheReadInputTokens)
}

// Distinct calls must still accumulate when neither key is present, so a turn
// with several single-entry calls is not collapsed into one.
func TestReadTurnUsageSumsDistinctMessageIDsWithoutRequestID(t *testing.T) {
	path := filepath.Join(t.TempDir(), "transcript.jsonl")
	writeTranscript(t, path, []string{
		`{"type":"user","message":{"role":"user","content":[{"type":"text","text":"hello"}]}}`,
		`{"type":"assistant","message":{"id":"msg_1","role":"assistant","content":[{"type":"text","text":"a"}],"usage":{"input_tokens":10,"output_tokens":5,"cache_creation_input_tokens":100,"cache_read_input_tokens":1000}}}`,
		`{"type":"assistant","message":{"id":"msg_2","role":"assistant","content":[{"type":"text","text":"b"}],"usage":{"input_tokens":20,"output_tokens":7,"cache_creation_input_tokens":200,"cache_read_input_tokens":2000}}}`,
	})

	usage, err := ReadTurnUsage(path)
	require.NoError(t, err)
	require.NotNil(t, usage)

	assert.Equal(t, int64(30), usage.InputTokens)
	assert.Equal(t, int64(12), usage.OutputTokens)
	assert.Equal(t, int64(300), usage.CacheCreationInputTokens)
	assert.Equal(t, int64(3000), usage.CacheReadInputTokens)
}

func TestReadTurnUsageReasoningTokens(t *testing.T) {
	path := filepath.Join(t.TempDir(), "transcript.jsonl")
	writeTranscript(t, path, []string{
		`{"type":"user","message":{"role":"user","content":"think"}}`,
		`{"type":"assistant","message":{"id":"msg_a","role":"assistant","content":[{"type":"text","text":"hi"}],"usage":{"input_tokens":10,"output_tokens":116,"cache_creation_input_tokens":0,"cache_read_input_tokens":0,"output_tokens_details":{"thinking_tokens":104}}}}`,
	})

	usage, err := ReadTurnUsage(path)
	require.NoError(t, err)
	require.NotNil(t, usage)
	assert.Equal(t, int64(116), usage.OutputTokens)
	assert.Equal(t, int64(104), usage.ReasoningOutputTokens, "a subset of OutputTokens, never added to it")
}

func TestReadTurnUsageReasoningTokensAbsent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "transcript.jsonl")
	writeTranscript(t, path, []string{
		`{"type":"user","message":{"role":"user","content":"say hi"}}`,
		`{"type":"assistant","message":{"id":"msg_a","role":"assistant","content":[{"type":"text","text":"hi"}],"usage":{"input_tokens":10,"output_tokens":12,"cache_creation_input_tokens":0,"cache_read_input_tokens":0}}}`,
	})

	usage, err := ReadTurnUsage(path)
	require.NoError(t, err)
	require.NotNil(t, usage)
	assert.Equal(t, int64(0), usage.ReasoningOutputTokens, "no detail means no thinking, which is a real zero")
}

func TestReadTurnUsageReasoningTokensSumAcrossCalls(t *testing.T) {
	path := filepath.Join(t.TempDir(), "transcript.jsonl")
	writeTranscript(t, path, []string{
		`{"type":"user","message":{"role":"user","content":"do it"}}`,
		`{"type":"assistant","message":{"id":"msg_a","role":"assistant","content":[{"type":"text","text":"a"}],"usage":{"input_tokens":10,"output_tokens":50,"cache_creation_input_tokens":0,"cache_read_input_tokens":0,"output_tokens_details":{"thinking_tokens":30}}}}`,
		`{"type":"user","isMeta":true,"message":{"role":"user","content":[{"type":"text","text":"relay"}]}}`,
		`{"type":"assistant","message":{"id":"msg_b","role":"assistant","content":[{"type":"text","text":"b"}],"usage":{"input_tokens":10,"output_tokens":40,"cache_creation_input_tokens":0,"cache_read_input_tokens":0,"output_tokens_details":{"thinking_tokens":11}}}}`,
	})

	usage, err := ReadTurnUsage(path)
	require.NoError(t, err)
	require.NotNil(t, usage)
	assert.Equal(t, int64(90), usage.OutputTokens)
	assert.Equal(t, int64(41), usage.ReasoningOutputTokens)
}

func TestReadTurnUsageReasoningTokensSumsFallbackIterations(t *testing.T) {
	path := filepath.Join(t.TempDir(), "transcript.jsonl")
	writeTranscript(t, path, []string{
		`{"type":"user","message":{"role":"user","content":"do it"}}`,
		`{"type":"assistant","message":{"id":"msg_a","role":"assistant","content":[{"type":"text","text":"a"}],"usage":{"input_tokens":5,"output_tokens":9,"cache_creation_input_tokens":0,"cache_read_input_tokens":0,"output_tokens_details":{"thinking_tokens":9},"iterations":[{"input_tokens":5,"output_tokens":4,"output_tokens_details":{"thinking_tokens":4}},{"input_tokens":5,"output_tokens":9,"output_tokens_details":{"thinking_tokens":9}}]}}}`,
	})

	usage, err := ReadTurnUsage(path)
	require.NoError(t, err)
	require.NotNil(t, usage)
	assert.Equal(t, int64(13), usage.OutputTokens, "both billed attempts")
	assert.Equal(t, int64(13), usage.ReasoningOutputTokens, "the split follows the same summing rule")
}

// A slash command is expanded client-side, so the transcript is the only record
// that a skill was invoked at all.
func TestReadTurnSkillCommand(t *testing.T) {
	const commandEntry = `{"type":"user","message":{"role":"user","content":"<command-message>writing:unslop</command-message>\n<command-name>/writing:unslop</command-name>\n<command-args>text</command-args>"}}`
	const skillRelay = `{"type":"user","isMeta":true,"message":{"role":"user","content":[{"type":"text","text":"Base directory for this skill: /home/me/.claude/plugins/cache/mp/writing/0.3.0/skills/unslop\n\n# Unslop\n"}]}}`
	const answer = `{"type":"assistant","message":{"id":"msg_a","role":"assistant","stop_reason":"end_turn","content":[{"type":"text","text":"done"}],"usage":{"input_tokens":1,"output_tokens":1}}}`

	tests := []struct {
		name  string
		lines []string
		want  string
	}{
		{
			name:  "command and skill relay",
			lines: []string{commandEntry, skillRelay, answer},
			want:  "writing:unslop",
		},
		{
			name: "project skill without a plugin prefix",
			lines: []string{
				`{"type":"user","message":{"role":"user","content":"<command-name>/tidy</command-name>"}}`,
				`{"type":"user","isMeta":true,"message":{"role":"user","content":[{"type":"text","text":"Base directory for this skill: /repo/.claude/skills/tidy\n"}]}}`,
				answer,
			},
			want: "tidy",
		},
		{
			name: "built-in command loads no skill",
			lines: []string{
				`{"type":"user","message":{"role":"user","content":"<command-name>/compact</command-name>\n<command-args></command-args>"}}`,
				answer,
			},
			want: "",
		},
		{
			name: "a skill the model loaded mid-turn is not a command invocation",
			lines: []string{
				`{"type":"user","message":{"role":"user","content":"please unslop this"}}`,
				skillRelay,
				answer,
			},
			want: "",
		},
		{
			name: "a command whose relay names a different skill is not counted",
			lines: []string{
				`{"type":"user","message":{"role":"user","content":"<command-name>/writing:unslop</command-name>"}}`,
				`{"type":"user","isMeta":true,"message":{"role":"user","content":[{"type":"text","text":"Base directory for this skill: /home/me/.claude/skills/other\n"}]}}`,
				answer,
			},
			want: "",
		},
		{
			name:  "only the current turn counts",
			lines: []string{commandEntry, skillRelay, answer, `{"type":"user","message":{"role":"user","content":"and now something else"}}`, answer},
			want:  "",
		},
		{
			// The relay is re-injected on every invocation, so a command whose own
			// turn has none did not load a skill — an earlier turn's relay must not
			// stand in for it.
			name: "an earlier turn's relay does not satisfy a later command",
			lines: []string{
				`{"type":"user","message":{"role":"user","content":"please unslop this"}}`,
				skillRelay,
				answer,
				commandEntry,
				answer,
			},
			want: "",
		},
		{
			name:  "a second invocation is read from its own turn",
			lines: []string{commandEntry, skillRelay, answer, commandEntry, skillRelay, answer},
			want:  "writing:unslop",
		},
		{
			// A space in the path is not the end of it. A home directory is enough
			// to produce one, and stopping there captured "/Users/guy".
			name: "a skill under a path containing a space",
			lines: []string{
				commandEntry,
				`{"type":"user","isMeta":true,"message":{"role":"user","content":[{"type":"text",` +
					`"text":"Base directory for this skill: /Users/guy moses/.claude/plugins/cache/mp/writing/0.3.0/skills/unslop\n\n# Unslop\n"}]}}`,
				answer,
			},
			want: "writing:unslop",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "transcript.jsonl")
			writeTranscript(t, path, tc.lines)
			assert.Equal(t, tc.want, ReadTurnSkillCommand(path))
		})
	}
}

func TestReadTurnSkillCommandMissingFile(t *testing.T) {
	assert.Equal(t, "", ReadTurnSkillCommand(filepath.Join(t.TempDir(), "nope.jsonl")))
}

// A transcript whose final entry is only half-written is the normal state, not a
// corrupted one: every reader here runs inside a hook while Claude Code is still
// appending. Each of these calls used to spin forever on that input, because a
// json.Decoder does not advance past a syntax error and the readers' `continue`
// re-read the same bytes. The assertions below are secondary — the test's real
// claim is that it terminates at all, which is why each reader is exercised and
// why the whole package runs under a timeout in CI.
func TestReadersTerminateOnATornFinalEntry(t *testing.T) {
	path := filepath.Join(t.TempDir(), "transcript.jsonl")
	content := strings.Join([]string{
		`{"type":"user","message":{"role":"user","content":"<command-name>/writing:unslop</command-name>"}}`,
		`{"type":"user","isMeta":true,"message":{"role":"user","content":[{"type":"text","text":"Base directory for this skill: /p/skills/unslop"}]}}`,
		`{"type":"assistant","requestId":"req_1","message":{"role":"assistant","model":"claude-opus-4-8","stop_reason":"end_turn","content":[{"type":"text","text":"hi"}],"usage":{"input_tokens":10,"output_tokens":20}}}`,
		`{"type":"assistant","requestId":"req_2","message":{"role":"assis`,
	}, "\n")
	require.NoError(t, os.WriteFile(path, []byte(content), 0o644))

	// Everything before the torn entry is still reported: a syntax error ends the
	// read, it does not discard what was already decoded.
	usage, err := ReadTurnUsage(path)
	require.NoError(t, err)
	require.NotNil(t, usage)
	assert.Equal(t, int64(10), usage.InputTokens)
	assert.Equal(t, int64(20), usage.OutputTokens)

	assert.Equal(t, "claude-opus-4-8", ReadModel(path))
	assert.True(t, HasAssistantEntry(path))
	assert.Equal(t, "writing:unslop", ReadTurnSkillCommand(path))

	complete, err := TurnComplete(path)
	require.NoError(t, err)
	assert.True(t, complete)
}

// A torn entry in the MIDDLE of the file ends the read, so later entries are not
// reported. That is the honest limit of a stream decoder: once it hits a syntax
// error it cannot resynchronize, so the alternative to stopping was the infinite
// loop, not recovery. Pinned here so the trade-off is visible if it ever changes.
func TestATornEntryMidFileEndsTheRead(t *testing.T) {
	path := filepath.Join(t.TempDir(), "transcript.jsonl")
	content := strings.Join([]string{
		`{"type":"user","message":{"role":"user","content":"hello"}}`,
		`{"type":"assistant","requestId":"req_1","message":{"role":"assistant","model":"first-model","stop_reason":"end_turn","content":[],"usage":{"input_tokens":10,"output_tokens":20}}}`,
		`{"type":"assistant","requestId":"broken","message":{"role":"assis`,
		`{"type":"assistant","requestId":"req_2","message":{"role":"assistant","model":"later-model","stop_reason":"end_turn","content":[],"usage":{"input_tokens":99,"output_tokens":99}}}`,
	}, "\n")
	require.NoError(t, os.WriteFile(path, []byte(content), 0o644))

	assert.Equal(t, "first-model", ReadModel(path), "the entry after the torn one is not reached")

	usage, err := ReadTurnUsage(path)
	require.NoError(t, err)
	require.NotNil(t, usage)
	assert.Equal(t, int64(10), usage.InputTokens, "only the entries before the tear are counted")
}

// A type mismatch is different from a syntax error: the decoder has consumed the
// value, so the stream is intact and that one entry can be skipped. Entries after
// it must still be read.
func TestATypeMismatchSkipsOnlyThatEntry(t *testing.T) {
	path := filepath.Join(t.TempDir(), "transcript.jsonl")
	content := strings.Join([]string{
		`{"type":"user","message":{"role":"user","content":"hello"}}`,
		`{"type":"assistant","requestId":42,"message":{"role":"assistant","model":"skipped-model"}}`,
		`{"type":"assistant","requestId":"req_1","message":{"role":"assistant","model":"later-model","stop_reason":"end_turn","content":[],"usage":{"input_tokens":7,"output_tokens":3}}}`,
	}, "\n")
	require.NoError(t, os.WriteFile(path, []byte(content), 0o644))

	assert.Equal(t, "later-model", ReadModel(path), "the stream survives a type error")

	usage, err := ReadTurnUsage(path)
	require.NoError(t, err)
	require.NotNil(t, usage)
	assert.Equal(t, int64(7), usage.InputTokens)
}

func TestSubagentPath(t *testing.T) {
	dir := t.TempDir()
	sessionTranscript := filepath.Join(dir, "sess-1.jsonl")
	require.NoError(t, os.WriteFile(sessionTranscript, []byte("{}\n"), 0o644))

	agentDir := filepath.Join(dir, "sess-1", "subagents")
	require.NoError(t, os.MkdirAll(agentDir, 0o755))
	agentTranscript := filepath.Join(agentDir, "agent-a1.jsonl")
	require.NoError(t, os.WriteFile(agentTranscript, []byte("{}\n"), 0o644))

	t.Run("derives the sub-agent transcript beside the session's", func(t *testing.T) {
		assert.Equal(t, agentTranscript, SubagentPath(sessionTranscript, "sess-1", "a1"))
	})

	t.Run("empty when the sub-agent has not written yet", func(t *testing.T) {
		assert.Empty(t, SubagentPath(sessionTranscript, "sess-1", "a2"))
	})

	t.Run("empty when a part is missing", func(t *testing.T) {
		assert.Empty(t, SubagentPath("", "sess-1", "a1"))
		assert.Empty(t, SubagentPath(sessionTranscript, "", "a1"))
		assert.Empty(t, SubagentPath(sessionTranscript, "sess-1", ""))
	})

	t.Run("a separator cannot escape the session directory", func(t *testing.T) {
		// Both parts name one path segment. Without the guard, "../.." in either
		// would resolve to a file outside the session's own directory.
		assert.Empty(t, SubagentPath(sessionTranscript, "../sess-1", "a1"))
		assert.Empty(t, SubagentPath(sessionTranscript, "sess-1", "../../a1"))
	})
}
