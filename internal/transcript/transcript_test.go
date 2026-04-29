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
		// Streaming split: thinking block first.
		`{"type":"assistant","requestId":"req_001","message":{"role":"assistant","content":[{"type":"thinking","thinking":"..."}],"usage":{"input_tokens":100,"output_tokens":50,"cache_creation_input_tokens":0,"cache_read_input_tokens":0}}}`,
		// Same requestId: text block.
		`{"type":"assistant","requestId":"req_001","message":{"role":"assistant","content":[{"type":"text","text":"hi"}],"usage":{"input_tokens":100,"output_tokens":50,"cache_creation_input_tokens":0,"cache_read_input_tokens":0}}}`,
	})

	usage, err := ReadTurnUsage(path)
	require.NoError(t, err)
	require.NotNil(t, usage)

	// Counted only once despite two entries with same requestId.
	assert.Equal(t, int64(100), usage.InputTokens)
	assert.Equal(t, int64(50), usage.OutputTokens)
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
