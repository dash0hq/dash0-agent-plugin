// SPDX-FileCopyrightText: Copyright 2026 Dash0 Inc.
// SPDX-License-Identifier: Apache-2.0

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
	Role  string     `json:"role"`
	Model string     `json:"model"`
	Usage *usageData `json:"usage"`
	// Content is either a plain string (typed user prompts) or an array of
	// content blocks (tool results, assistant messages), so it is kept raw
	// and inspected in isRealUserMessage.
	Content json.RawMessage `json:"content"`
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
	defer func() { _ = f.Close() }()

	dec := json.NewDecoder(f)

	// perReq tracks per-requestId usage, keeping only the last entry for
	// each requestId. Streaming splits a single API call into multiple
	// transcript entries (thinking block, then text block); the last entry
	// carries the final output_tokens count.
	perReq := make(map[string]*usageData)
	// noReq collects entries without a requestId (shouldn't happen in
	// practice but handled for safety).
	var noReqUsage Usage
	var hasUsage bool

	for dec.More() {
		var entry transcriptEntry
		if err := dec.Decode(&entry); err != nil {
			continue // skip malformed entries
		}

		if isRealUserMessage(entry) {
			// New turn — reset accumulator.
			perReq = make(map[string]*usageData)
			noReqUsage = Usage{}
			hasUsage = false
			continue
		}

		if entry.Type != "assistant" || entry.Message == nil || entry.Message.Usage == nil {
			continue
		}

		hasUsage = true
		u := entry.Message.Usage
		if entry.RequestID != "" {
			perReq[entry.RequestID] = u
		} else {
			noReqUsage.InputTokens += u.InputTokens
			noReqUsage.OutputTokens += u.OutputTokens
			noReqUsage.CacheCreationInputTokens += u.CacheCreationInputTokens
			noReqUsage.CacheReadInputTokens += u.CacheReadInputTokens
		}
	}

	// Sum final usage across all API calls in the turn.
	usage := noReqUsage
	for _, u := range perReq {
		usage.InputTokens += u.InputTokens
		usage.OutputTokens += u.OutputTokens
		usage.CacheCreationInputTokens += u.CacheCreationInputTokens
		usage.CacheReadInputTokens += u.CacheReadInputTokens
	}

	if !hasUsage {
		return nil, nil
	}
	return &usage, nil
}

// titleEntry captures the custom-title field from transcript JSONL entries.
type titleEntry struct {
	Type        string `json:"type"`
	CustomTitle string `json:"customTitle"`
}

// ReadSessionTitle reads the transcript file and returns the most recent
// custom-title value, or empty string if none is found.
func ReadSessionTitle(transcriptPath string) string {
	f, err := os.Open(transcriptPath)
	if err != nil {
		return ""
	}
	defer func() { _ = f.Close() }()

	dec := json.NewDecoder(f)
	var title string
	for dec.More() {
		var entry titleEntry
		if err := dec.Decode(&entry); err != nil {
			continue
		}
		if entry.Type == "custom-title" && entry.CustomTitle != "" {
			title = entry.CustomTitle
		}
	}
	return title
}

// ReadModel reads the transcript file and returns the model from the most
// recent assistant message, or empty string if none is found.
func ReadModel(transcriptPath string) string {
	f, err := os.Open(transcriptPath)
	if err != nil {
		return ""
	}
	defer func() { _ = f.Close() }()

	dec := json.NewDecoder(f)
	var model string
	for dec.More() {
		var entry transcriptEntry
		if err := dec.Decode(&entry); err != nil {
			continue
		}
		if entry.Type == "assistant" && entry.Message != nil && entry.Message.Model != "" {
			model = entry.Message.Model
		}
	}
	return model
}

// isRealUserMessage returns true if the entry is a user message that is NOT
// a tool_result relay. Typed prompts carry content as a plain string;
// tool-result relays carry an array with content[0].type == "tool_result"
// and should not reset the turn boundary.
func isRealUserMessage(entry transcriptEntry) bool {
	if entry.Type != "user" {
		return false
	}
	if entry.Message == nil || entry.Message.Role != "user" {
		return false
	}
	var blocks []json.RawMessage
	if err := json.Unmarshal(entry.Message.Content, &blocks); err != nil {
		// Not an array — string content, i.e. a typed prompt.
		return true
	}
	if len(blocks) > 0 {
		var ct contentType
		if err := json.Unmarshal(blocks[0], &ct); err == nil {
			if ct.Type == "tool_result" {
				return false
			}
		}
	}
	return true
}
