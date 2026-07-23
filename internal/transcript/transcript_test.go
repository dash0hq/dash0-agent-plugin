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
