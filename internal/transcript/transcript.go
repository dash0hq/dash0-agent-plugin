package transcript

import (
	"encoding/json"
	"fmt"
	"os"
)

// Usage holds aggregated token usage for a turn.
type Usage struct {
	InputTokens              int64
	OutputTokens             int64
	CacheCreationInputTokens int64
	CacheReadInputTokens     int64
}

// transcriptEntry captures only the fields we need from transcript JSONL entries.
type transcriptEntry struct {
	Type      string           `json:"type"`
	RequestID string           `json:"requestId"`
	Message   *messageEnvelope `json:"message"`
}

type messageEnvelope struct {
	Role  string              `json:"role"`
	Usage *usageData          `json:"usage"`
	Content []json.RawMessage `json:"content"`
}

type usageData struct {
	InputTokens              int64 `json:"input_tokens"`
	OutputTokens             int64 `json:"output_tokens"`
	CacheCreationInputTokens int64 `json:"cache_creation_input_tokens"`
	CacheReadInputTokens     int64 `json:"cache_read_input_tokens"`
}

// contentType is used to peek at a content block's type field without fully
// decoding it.
type contentType struct {
	Type string `json:"type"`
}

// ReadTurnUsage reads the transcript file and returns aggregated token usage
// for the most recent turn (all assistant messages since the last real user
// message). Returns nil when no usage data is found.
//
// Streaming duplicates (same requestId across multiple transcript entries) are
// deduplicated so usage is counted only once per API call.
func ReadTurnUsage(transcriptPath string) (*Usage, error) {
	f, err := os.Open(transcriptPath)
	if err != nil {
		return nil, fmt.Errorf("opening transcript: %w", err)
	}
	defer f.Close()

	dec := json.NewDecoder(f)

	var (
		usage    Usage
		hasUsage bool
		seen     = make(map[string]bool)
	)

	for dec.More() {
		var entry transcriptEntry
		if err := dec.Decode(&entry); err != nil {
			continue // skip malformed entries
		}

		if isRealUserMessage(entry) {
			// New turn — reset accumulator.
			usage = Usage{}
			hasUsage = false
			seen = make(map[string]bool)
			continue
		}

		if entry.Type != "assistant" || entry.Message == nil || entry.Message.Usage == nil {
			continue
		}

		// Deduplicate by requestId (streaming splits assistant messages).
		if entry.RequestID != "" && seen[entry.RequestID] {
			continue
		}
		if entry.RequestID != "" {
			seen[entry.RequestID] = true
		}

		u := entry.Message.Usage
		usage.InputTokens += u.InputTokens
		usage.OutputTokens += u.OutputTokens
		usage.CacheCreationInputTokens += u.CacheCreationInputTokens
		usage.CacheReadInputTokens += u.CacheReadInputTokens
		hasUsage = true
	}

	if !hasUsage {
		return nil, nil
	}
	return &usage, nil
}

// isRealUserMessage returns true if the entry is a user message that is NOT
// a tool_result relay. Tool-result messages have content[0].type == "tool_result"
// and should not reset the turn boundary.
func isRealUserMessage(entry transcriptEntry) bool {
	if entry.Type != "user" {
		return false
	}
	if entry.Message == nil || entry.Message.Role != "user" {
		return false
	}
	if len(entry.Message.Content) > 0 {
		var ct contentType
		if err := json.Unmarshal(entry.Message.Content[0], &ct); err == nil {
			if ct.Type == "tool_result" {
				return false
			}
		}
	}
	return true
}
