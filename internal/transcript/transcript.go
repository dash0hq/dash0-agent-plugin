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
	// ID is the API call's message id. Streaming writes one transcript entry per
	// content block, all sharing this id and each repeating the call's usage, so
	// it is the key that identifies a single billed call.
	ID         string     `json:"id"`
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
//
// A call already counted in an EARLIER turn of the same file is skipped. Claude
// Code re-appends conversation history it has re-materialized — byte-identical
// entries, original timestamps, original usage — after the current turn's
// prompt (observed on continuation/compaction, reported for revived and forked
// sessions). Since the replayed user entries are tool_result relays, they do
// not close the turn, so a purely position-based scan would bill that history a
// second time: in production this reported a session's cumulative usage on
// single turns, turning a ~$14 turn into a ~$700 one.
//
// Entries that predate the current turn but were never counted before are still
// attributed to it. They are the tail of a turn whose flush lost the race with
// the next prompt (see TurnComplete), and billing them one turn late keeps the
// session total whole — the lesser error for a cost view than dropping them.
func ReadTurnUsage(transcriptPath string) (*Usage, error) {
	f, err := os.Open(transcriptPath)
	if err != nil {
		return nil, fmt.Errorf("opening transcript: %w", err)
	}
	defer func() { _ = f.Close() }()

	dec := json.NewDecoder(f)

	// perCall tracks usage per API call, keeping only the last entry for each.
	// Streaming splits a single call into multiple transcript entries (thinking
	// block, then text block), each repeating that call's usage; the last entry
	// carries the final output_tokens count, and the input and cache counts are
	// identical across a call's entries.
	perCall := make(map[string]*usageData)
	// callOrder preserves first-seen order so the result does not depend on map
	// iteration order.
	var callOrder []string
	// counted holds the call keys of every earlier turn in this file, so
	// replayed history is not counted again.
	counted := make(map[string]bool)
	var hasUsage bool
	// entryCount seeds a synthetic key for entries carrying neither id, so
	// distinct calls are still counted separately rather than merged.
	entryCount := 0

	for dec.More() {
		var entry transcriptEntry
		if err := dec.Decode(&entry); err != nil {
			continue // skip malformed entries
		}

		if isRealUserMessage(entry) {
			// New turn — the calls counted so far belong to the turn that just
			// ended, so a replay of them later in the file must not count again.
			for _, key := range callOrder {
				counted[key] = true
			}
			perCall = make(map[string]*usageData)
			callOrder = nil
			hasUsage = false
			continue
		}

		if entry.Type != "assistant" || entry.Message == nil || entry.Message.Usage == nil {
			continue
		}

		entryCount++
		// Prefer the message id: some sessions write no requestId at all, and
		// without a per-call key each streamed entry's usage would be counted
		// again. Falling back to a per-entry key keeps distinct calls separate.
		key := entry.Message.ID
		if key == "" {
			key = entry.RequestID
		}
		if key == "" {
			key = fmt.Sprintf("entry-%d", entryCount)
		}
		if counted[key] {
			// Replayed history: this call was already reported on an earlier
			// turn's span.
			continue
		}
		hasUsage = true
		if _, seen := perCall[key]; !seen {
			callOrder = append(callOrder, key)
		}
		perCall[key] = entry.Message.Usage
	}

	// Sum final usage across all API calls in the turn.
	var usage Usage
	for _, key := range callOrder {
		eff := perCall[key].effective()
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
