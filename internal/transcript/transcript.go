// SPDX-FileCopyrightText: Copyright 2026 Dash0 Inc.
// SPDX-License-Identifier: Apache-2.0

package transcript

import (
	"encoding/json"
	"fmt"
	"os"
)

// TokenCounts holds the token counts of a set of API calls. The JSON field
// names mirror the source transcript's usage object, and are what a Breakdown
// entry serializes to.
type TokenCounts struct {
	InputTokens              int64 `json:"input_tokens"`
	OutputTokens             int64 `json:"output_tokens"`
	CacheCreationInputTokens int64 `json:"cache_creation_input_tokens"`
	CacheReadInputTokens     int64 `json:"cache_read_input_tokens"`
	// CacheCreation5mInputTokens and CacheCreation1hInputTokens decompose
	// CacheCreationInputTokens by cache TTL. Anthropic prices 1h cache writes
	// higher than 5m writes, so the split is needed for accurate cost; there is
	// no OpenTelemetry semantic-convention attribute for it (the semconv
	// standardizes only input/output tokens), so it is emitted under dash0.gen_ai.usage.
	// A source that omits the breakdown leaves both at 0, which costs nothing.
	CacheCreation5mInputTokens int64 `json:"cache_creation_5m_input_tokens"`
	CacheCreation1hInputTokens int64 `json:"cache_creation_1h_input_tokens"`
}

// Mode is the set of billing dimensions the API served a call under. A zero
// Mode means the call was priced at the default rate, so each field is omitted
// from JSON when it holds the default. Adding a dimension means adding a
// field, never a new attribute key.
type Mode struct {
	// Speed is Anthropic's fast mode ("fast"), which bills off a higher rate
	// table than standard speed.
	Speed string `json:"speed,omitempty"`
	// ServiceTier is the request's service tier ("priority", "batch", …), which
	// prices differently — batch at a discount, priority at a premium.
	ServiceTier string `json:"service_tier,omitempty"`
	// InferenceGeo is where inference ran ("us"), which Anthropic prices at a
	// premium over global routing. Claude Code never asks for it; a workspace
	// default or a migrated legacy data-residency opt-out applies it server-side,
	// so a turn can carry it with nothing set locally.
	InferenceGeo string `json:"inference_geo,omitempty"`
}

// ModeUsage is the share of a turn's tokens that one billing mode accounts for.
type ModeUsage struct {
	Mode
	TokenCounts
}

// Usage holds aggregated token usage for a turn.
type Usage struct {
	// TokenCounts totals every API call of the turn, whatever mode it ran in.
	TokenCounts
	// Breakdown partitions those totals by billing mode, and is nil when every
	// call ran at the default rate — the common turn, which then needs no
	// breakdown at all. It is populated as soon as one call did not: a turn can
	// mix modes (/fast toggles mid-session, and fast mode falls back to standard
	// while it is rate-limited), so pricing needs the split rather than one mode
	// label for the whole turn. Every call lands in exactly one entry, so the
	// entries always sum back to the totals above.
	Breakdown []ModeUsage
}

// add folds one API call's effective usage into the totals and into the entry
// for the mode that call was served under, appending that entry on first sight.
func (u *Usage) add(eff usageData) {
	u.TokenCounts.add(eff)

	mode := eff.mode()
	for i := range u.Breakdown {
		if u.Breakdown[i].Mode == mode {
			u.Breakdown[i].add(eff)
			return
		}
	}
	entry := ModeUsage{Mode: mode}
	entry.add(eff)
	u.Breakdown = append(u.Breakdown, entry)
}

// finalize drops a breakdown that says nothing: one entry covering the whole
// turn at the default rate, which the totals already express.
func (u *Usage) finalize() {
	if len(u.Breakdown) == 1 && u.Breakdown[0].Mode == (Mode{}) {
		u.Breakdown = nil
	}
}

// add folds one API call's effective usage into the counts. A nil
// CacheCreation (source gave no TTL split) leaves the breakdown totals at 0.
func (t *TokenCounts) add(eff usageData) {
	t.InputTokens += eff.InputTokens
	t.OutputTokens += eff.OutputTokens
	t.CacheCreationInputTokens += eff.CacheCreationInputTokens
	t.CacheReadInputTokens += eff.CacheReadInputTokens
	if eff.CacheCreation != nil {
		t.CacheCreation5mInputTokens += eff.CacheCreation.Ephemeral5mInputTokens
		t.CacheCreation1hInputTokens += eff.CacheCreation.Ephemeral1hInputTokens
	}
}

// modeDefault is the value speed and service tier carry when a call was priced
// at the default rate. An absent field means the same: transcripts written
// before Claude Code recorded these fields describe ordinary, default-rate calls.
const modeDefault = "standard"

// mode returns the billing dimensions of this call, normalizing the default
// values away so that a zero Mode means "nothing to price differently".
func (u *usageData) mode() Mode {
	var m Mode
	if u.Speed != "" && u.Speed != modeDefault {
		m.Speed = u.Speed
	}
	if u.ServiceTier != "" && u.ServiceTier != modeDefault {
		m.ServiceTier = u.ServiceTier
	}
	if !isDefaultInferenceGeo(u.InferenceGeo) {
		m.InferenceGeo = u.InferenceGeo
	}
	return m
}

// isDefaultInferenceGeo reports whether a geo carries no price premium.
// "global" is Anthropic's documented default routing, and "not_available" is
// what Claude Code records when the deployment reports no geo at all — the
// value on nearly every call today. Neither may look like a billing mode, or
// every ordinary turn would carry a breakdown describing nothing.
func isDefaultInferenceGeo(geo string) bool {
	switch geo {
	case "", "global", "not_available":
		return true
	default:
		return false
	}
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
	InputTokens              int64          `json:"input_tokens"`
	OutputTokens             int64          `json:"output_tokens"`
	CacheCreationInputTokens int64          `json:"cache_creation_input_tokens"`
	CacheReadInputTokens     int64          `json:"cache_read_input_tokens"`
	CacheCreation            *cacheCreation `json:"cache_creation"`
	Iterations               []usageData    `json:"iterations"`
	// Speed is the mode the API served this call in ("standard" or "fast"), as
	// reported by the response rather than by the local /fast toggle — so it
	// reflects what was actually billed. It sits on the top-level usage object
	// only, never on an iteration.
	Speed string `json:"speed"`
	// ServiceTier is the tier the call was served under ("standard",
	// "priority", "batch"), the other dimension Anthropic prices on. Top-level
	// only, like Speed.
	ServiceTier string `json:"service_tier"`
	// InferenceGeo is where inference actually ran ("us", "global", or
	// "not_available" when the deployment reports none). Top-level only, like
	// Speed, and likewise the response's account of what was billed rather than
	// what the request asked for.
	InferenceGeo string `json:"inference_geo"`
}

// cacheCreation splits cache-creation tokens by TTL.
type cacheCreation struct {
	Ephemeral5mInputTokens int64 `json:"ephemeral_5m_input_tokens"`
	Ephemeral1hInputTokens int64 `json:"ephemeral_1h_input_tokens"`
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
	// CacheCreation lives only on the top-level usage object, not on
	// individual iterations, so carry it through unchanged. On a fallback turn it
	// reflects the final iteration only, so the split may under-sum the
	// iteration-summed CacheCreationInputTokens total — the flat total stays
	// authoritative; the split is best-effort.
	sum.CacheCreation = u.CacheCreation
	// The billing dimensions are top-level only too, so the final iteration's
	// mode is attributed to every summed attempt. A fallback that crossed modes
	// therefore lands wholly in one breakdown entry — also best-effort.
	sum.Speed = u.Speed
	sum.ServiceTier = u.ServiceTier
	sum.InferenceGeo = u.InferenceGeo
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
// A call already counted in an EARLIER turn of the same file is skipped. On
// continuation/compaction, Claude Code re-appends conversation history it has
// re-materialized — byte-identical entries, original timestamps, original
// usage — after the current turn's prompt. The replayed user entries are
// tool_result relays, which do not close the turn, so a purely position-based
// scan counts that history a second time and reports a session's cumulative
// usage on a single turn.
//
// The rule only matches a replay whose original occurs earlier in the same
// file, which covers the layouts Claude Code writes: resuming appends to the
// same transcript without replaying anything, and forking copies the parent
// history AHEAD of the fork's first prompt, so the turn boundary already
// excludes it. A replay whose original never appeared earlier in the file is
// out of scope here.
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

	if !hasUsage {
		return nil, nil
	}

	// Sum final usage across all API calls in the turn.
	var usage Usage
	for _, key := range callOrder {
		eff := perCall[key].effective()
		usage.add(eff)
	}
	usage.finalize()
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
