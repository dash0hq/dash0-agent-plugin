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
	IsMeta    bool             `json:"isMeta"`
	Message   *messageEnvelope `json:"message"`
}

type messageEnvelope struct {
	Role       string     `json:"role"`
	Model      string     `json:"model"`
	StopReason string     `json:"stop_reason"`
	Usage      *usageData `json:"usage"`
	// Content is either a plain string (typed user prompts) or an array of
	// content blocks (tool results, assistant messages), so it is kept raw
	// and inspected in isRealUserMessage.
	Content json.RawMessage `json:"content"`
}

type usageData struct {
	InputTokens              int64       `json:"input_tokens"`
	OutputTokens             int64       `json:"output_tokens"`
	CacheCreationInputTokens int64       `json:"cache_creation_input_tokens"`
	CacheReadInputTokens     int64       `json:"cache_read_input_tokens"`
	Iterations               []usageData `json:"iterations"`
}

// effective returns the token counts to attribute to this API call. When a
// request is retried on a fallback model, the top-level fields mirror only the
// final iteration while usage.iterations lists every billed attempt — in that
// case the iterations are summed. With zero or one iteration the top-level
// fields already hold the full picture and are returned unchanged.
func (u *usageData) effective() usageData {
	if len(u.Iterations) <= 1 {
		return *u
	}
	var sum usageData
	for _, it := range u.Iterations {
		sum.InputTokens += it.InputTokens
		sum.OutputTokens += it.OutputTokens
		sum.CacheCreationInputTokens += it.CacheCreationInputTokens
		sum.CacheReadInputTokens += it.CacheReadInputTokens
	}
	return sum
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
// deduplicated so usage is counted only once per API call. When a call was
// retried on a fallback model, all billed iterations are summed (see
// usageData.effective).
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
			eff := u.effective()
			noReqUsage.InputTokens += eff.InputTokens
			noReqUsage.OutputTokens += eff.OutputTokens
			noReqUsage.CacheCreationInputTokens += eff.CacheCreationInputTokens
			noReqUsage.CacheReadInputTokens += eff.CacheReadInputTokens
		}
	}

	// Sum final usage across all API calls in the turn.
	usage := noReqUsage
	for _, u := range perReq {
		eff := u.effective()
		usage.InputTokens += eff.InputTokens
		usage.OutputTokens += eff.OutputTokens
		usage.CacheCreationInputTokens += eff.CacheCreationInputTokens
		usage.CacheReadInputTokens += eff.CacheReadInputTokens
	}

	if !hasUsage {
		return nil, nil
	}
	return &usage, nil
}

// terminalStopReasons are the stop_reason values that mark an assistant message
// as the last one of its turn. "tool_use" is excluded: it is emitted mid-turn,
// before the model sees the tool result and continues.
var terminalStopReasons = map[string]bool{
	"end_turn":      true,
	"stop_sequence": true,
	"max_tokens":    true,
}

// TurnComplete reports whether the most recent assistant message of the current
// turn (the entries since the last real user message) is terminal — i.e. the
// turn has been fully written to the transcript.
//
// Claude Code flushes the transcript asynchronously and may lag the in-memory
// conversation, so when a Stop hook fires the file can still end at a mid-turn
// tool_use entry, with the final (often largest, cache-heavy) API call's usage
// not yet on disk. Callers poll this before reading usage so the last call is
// not dropped. Returns false when the current turn has no assistant entry yet.
func TurnComplete(transcriptPath string) (bool, error) {
	f, err := os.Open(transcriptPath)
	if err != nil {
		return false, fmt.Errorf("opening transcript: %w", err)
	}
	defer func() { _ = f.Close() }()

	dec := json.NewDecoder(f)
	var lastReason string
	var sawAssistant bool
	for dec.More() {
		var entry transcriptEntry
		if err := dec.Decode(&entry); err != nil {
			continue // skip malformed entries
		}
		if isRealUserMessage(entry) {
			// New turn — only the current turn's terminal state matters.
			lastReason = ""
			sawAssistant = false
			continue
		}
		if entry.Type == "assistant" && entry.Message != nil {
			sawAssistant = true
			lastReason = entry.Message.StopReason
		}
	}
	if !sawAssistant {
		return false, nil
	}
	return terminalStopReasons[lastReason], nil
}

// titleEntry captures the title fields from transcript JSONL entries. Claude
// Code writes an auto-generated name as an "ai-title" entry (aiTitle) and, when
// the user runs /rename, a "custom-title" entry (customTitle) that overrides it.
type titleEntry struct {
	Type        string `json:"type"`
	CustomTitle string `json:"customTitle"`
	AITitle     string `json:"aiTitle"`
}

// ReadSessionTitle reads the transcript file and returns the session name,
// preferring the most recent user-set custom title (/rename) and falling back
// to the most recent auto-generated title. Returns empty string if neither is
// found. This mirrors the precedence Claude Code uses in the UI (/status).
func ReadSessionTitle(transcriptPath string) string {
	f, err := os.Open(transcriptPath)
	if err != nil {
		return ""
	}
	defer func() { _ = f.Close() }()

	dec := json.NewDecoder(f)
	var customTitle, aiTitle string
	for dec.More() {
		var entry titleEntry
		if err := dec.Decode(&entry); err != nil {
			continue
		}
		switch entry.Type {
		case "custom-title":
			if entry.CustomTitle != "" {
				customTitle = entry.CustomTitle
			}
		case "ai-title":
			if entry.AITitle != "" {
				aiTitle = entry.AITitle
			}
		}
	}
	if customTitle != "" {
		return customTitle
	}
	return aiTitle
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
// a tool_result relay and NOT an injected meta message. Typed prompts carry
// content as a plain string; tool-result relays carry an array with
// content[0].type == "tool_result", and meta messages (isMeta, e.g. the
// skill-loading relay injected mid-turn) both should not reset the turn
// boundary — otherwise usage accumulated earlier in the turn is discarded.
func isRealUserMessage(entry transcriptEntry) bool {
	if entry.Type != "user" {
		return false
	}
	if entry.IsMeta {
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
