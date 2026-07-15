// SPDX-FileCopyrightText: Copyright 2026 Dash0 Inc.
// SPDX-License-Identifier: Apache-2.0

package codex

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
)

// Usage holds aggregated token usage for a single Codex turn. Field semantics
// match the OTel GenAI convention the Dash0 cost processor expects: InputTokens
// is the TOTAL prompt count, inclusive of the cached portion, and
// CacheReadInputTokens is a subset of it. The processor derives the uncached
// input itself (input − cache_read − cache_creation), so we must not subtract
// here. Codex reports tokens the same way (input_tokens includes cached), so the
// mapping is a straight copy.
type Usage struct {
	InputTokens           int64 // total prompt tokens, INCLUDING the cached portion
	CacheReadInputTokens  int64 // prompt tokens served from cache (a subset of InputTokens)
	OutputTokens          int64 // completion tokens (includes reasoning tokens)
	ReasoningOutputTokens int64 // reasoning tokens (a subset of OutputTokens); parsed for future use, not yet emitted
}

// rolloutLine is the subset of a Codex rollout JSONL record we read. A rollout
// interleaves several record types (session_meta, turn_context, response_item,
// event_msg); token usage lives on event_msg records whose payload type is
// "token_count", and turn boundaries are event_msg records of type "user_message".
type rolloutLine struct {
	Type    string `json:"type"`
	Payload struct {
		Type string `json:"type"`
		Info struct {
			LastTokenUsage codexTokenUsage `json:"last_token_usage"`
		} `json:"info"`
	} `json:"payload"`
}

// codexTokenUsage mirrors the per-API-call token counts Codex records in
// info.last_token_usage. Note input_tokens is INCLUSIVE of cached_input_tokens
// (verified against Codex 0.142.5: total_tokens == input_tokens + output_tokens,
// and cached_input_tokens <= input_tokens).
type codexTokenUsage struct {
	InputTokens           int64 `json:"input_tokens"`
	CachedInputTokens     int64 `json:"cached_input_tokens"`
	OutputTokens          int64 `json:"output_tokens"`
	ReasoningOutputTokens int64 `json:"reasoning_output_tokens"`
}

// ReadTurnUsage reads a Codex rollout file and returns aggregated token usage for
// the most recent turn — the sum of every token_count event since the last
// user_message. A single turn drives several model round-trips (one token_count
// per call), so summing their last_token_usage deltas yields the turn total;
// resetting at each user_message scopes the result to the just-completed turn,
// mirroring the Claude transcript reader's per-turn semantics.
//
// Returns (nil, nil) when the file contains no token_count data (e.g. an
// interrupted turn) so the caller emits the span without token attributes.
//
// Compressed rollouts (.jsonl.zst, opt-in on newer Codex builds) are not yet
// supported: a .zst path yields (nil, nil) so the caller emits the span without
// token attributes. The caller (Normalize) detects the same suffix and marks the
// span dash0.codex.rollout.compressed so the gap is visible in telemetry. Adding
// zstd support is a localized change here — no runtime dependency exists in this
// module today and no compressed rollout has been observed to test against (Codex
// 0.142.5 writes plain .jsonl).
func ReadTurnUsage(rolloutPath string) (*Usage, error) {
	if strings.HasSuffix(rolloutPath, ".zst") {
		return nil, nil
	}

	f, err := os.Open(rolloutPath)
	if err != nil {
		return nil, fmt.Errorf("opening rollout: %w", err)
	}
	defer func() { _ = f.Close() }()

	dec := json.NewDecoder(f)

	var turn Usage
	var hasUsage bool
	for dec.More() {
		var line rolloutLine
		if err := dec.Decode(&line); err != nil {
			continue // skip malformed lines
		}
		if line.Type != "event_msg" {
			continue
		}
		switch line.Payload.Type {
		case "user_message":
			// New turn — discard usage accumulated for the previous turn so only
			// the most recent turn's counts survive.
			turn = Usage{}
			hasUsage = false
		case "token_count":
			u := line.Payload.Info.LastTokenUsage
			// Emit Codex's counts as-is. input_tokens is the total prompt count
			// inclusive of the cached portion — which is exactly what the cost
			// processor expects (it derives uncached = input − cache_read −
			// cache_creation itself). Subtracting cached here would double-count
			// the discount and under-price the turn.
			turn.InputTokens += u.InputTokens
			turn.CacheReadInputTokens += u.CachedInputTokens
			turn.OutputTokens += u.OutputTokens
			turn.ReasoningOutputTokens += u.ReasoningOutputTokens
			hasUsage = true
		}
	}

	if !hasUsage {
		return nil, nil
	}
	return &turn, nil
}
