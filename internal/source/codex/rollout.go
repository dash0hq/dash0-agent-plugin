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

// Limits holds the account-level allowance state Codex reports alongside token
// usage. Unlike Usage this is NOT per-turn: it describes the plan the session
// runs under and how much of the current window it has consumed, so the reader
// keeps the most recent value seen rather than resetting at turn boundaries.
type Limits struct {
	PlanType    string  // free / go / plus / pro / business; empty when absent
	Primary     *Window // nil when unreported
	Secondary   *Window // nil when the plan reports only one window
	ReachedType string  // which window blocked, once a limit is hit; empty until then
	Credits     *Credits
}

// Window is an allowance window and how much of it is consumed. Nil rather than
// zero-valued when unreported: "0% consumed" is a measurement, not an absence.
//
// Codex models both slots as the same RateLimitWindow type, and which duration
// occupies which slot is not fixed — read WindowMinutes to tell a short rolling
// window from a monthly one rather than assuming an ordering.
type Window struct {
	UsedPercent   float64 // consumption against the allowance, 0-100
	WindowMinutes int64   // length of the window (43200 = 30 days, 300 = 5 hours)
	ResetsAt      int64   // unix seconds at which the window resets
}

// Credits is the prepaid balance a user can hold for usage beyond the included
// allowance — the one place per-token spend re-enters a subscription. Nil when
// the CLI predates the field (rollouts before ~14 Jul 2026 report it as null).
type Credits struct {
	HasCredits bool
	Unlimited  bool
	Balance    *float64 // nil when unreported; distinct from a balance of zero
}

// Billing modes. See DEVELOPMENT.md for why "api" is never among them.
const (
	BillingSubscription = "subscription"
	BillingUnknown      = "unknown"
)

// BillingMode reports whether the session is billed per token at all — the only
// thing the cost label depends on. The finer detail travels as PlanType. Safe on
// a nil receiver, which is the pre-July-CLI case.
func (l *Limits) BillingMode() string {
	if l == nil || l.PlanType == "" {
		return BillingUnknown
	}
	return BillingSubscription
}

// Rollout is everything one pass over a rollout file yields.
type Rollout struct {
	Usage  *Usage  // the most recent turn's token counts; nil when the file has none
	Limits *Limits // account allowance state; nil when the CLI predates the field
}

// rolloutLine is the subset of a Codex rollout JSONL record we read. A rollout
// interleaves several record types (session_meta, turn_context, response_item,
// event_msg); token usage lives on event_msg records whose payload type is
// "token_count", and turn boundaries are event_msg records of type "user_message".
//
// Note RateLimits is a SIBLING of Info, not a child of it — a token_count
// payload carries {type, info, rate_limits} side by side.
type rolloutLine struct {
	Type    string `json:"type"`
	Payload struct {
		Type string `json:"type"`
		Info struct {
			LastTokenUsage codexTokenUsage `json:"last_token_usage"`
		} `json:"info"`
		RateLimits *codexRateLimits `json:"rate_limits"`
	} `json:"payload"`
}

// codexRateLimits mirrors the rate_limits block on a token_count payload
// (RateLimitSnapshot in the Codex source). Pointer-typed sub-objects so an absent
// block stays distinguishable from a present-but-zero one.
type codexRateLimits struct {
	PlanType    string       `json:"plan_type"`
	Primary     *codexWindow `json:"primary"`
	Secondary   *codexWindow `json:"secondary"`
	ReachedType string       `json:"rate_limit_reached_type"`
	Credits     *struct {
		HasCredits bool     `json:"has_credits"`
		Unlimited  bool     `json:"unlimited"`
		Balance    *float64 `json:"balance"`
	} `json:"credits"`
}

// codexWindow is Codex's RateLimitWindow — the shape both the primary and
// secondary slots carry.
type codexWindow struct {
	UsedPercent   float64 `json:"used_percent"`
	WindowMinutes int64   `json:"window_minutes"`
	ResetsAt      int64   `json:"resets_at"`
}

// newWindow converts a wire window, preserving absence: a null slot stays nil
// rather than becoming a zero-valued window.
func newWindow(w *codexWindow) *Window {
	if w == nil {
		return nil
	}
	return &Window{
		UsedPercent:   w.UsedPercent,
		WindowMinutes: w.WindowMinutes,
		ResetsAt:      w.ResetsAt,
	}
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
	r, err := ReadRollout(rolloutPath)
	if err != nil || r == nil {
		return nil, err
	}
	return r.Usage, nil
}

// ReadRollout walks a Codex rollout file once and returns both the most recent
// turn's token usage and the account's allowance state. The two have different
// lifetimes: usage is per-turn and resets at each user_message, while limits
// describe the account and simply carry the last value seen.
//
// Returns (nil, nil) for a compressed rollout, matching ReadTurnUsage.
func ReadRollout(rolloutPath string) (*Rollout, error) {
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
	var limits *Limits
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
			// the most recent turn's counts survive. Limits deliberately survive:
			// they describe the account, not the turn.
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

			if rl := line.Payload.RateLimits; rl != nil {
				l := Limits{
					PlanType:    rl.PlanType,
					ReachedType: rl.ReachedType,
					Primary:     newWindow(rl.Primary),
					Secondary:   newWindow(rl.Secondary),
				}
				if c := rl.Credits; c != nil {
					l.Credits = &Credits{
						HasCredits: c.HasCredits,
						Unlimited:  c.Unlimited,
						Balance:    c.Balance,
					}
				}
				limits = &l
			}
		}
	}

	out := &Rollout{Limits: limits}
	if hasUsage {
		out.Usage = &turn
	}
	return out, nil
}
