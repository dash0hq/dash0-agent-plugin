// SPDX-FileCopyrightText: Copyright 2026 Dash0 Inc.
// SPDX-License-Identifier: Apache-2.0

package codex

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"regexp"
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
	InputTokens              int64 // total prompt tokens, INCLUDING the cached portion
	CacheReadInputTokens     int64 // prompt tokens served from cache (a subset of InputTokens)
	CacheCreationInputTokens int64 // prompt tokens written to the cache (also a subset of InputTokens)
	OutputTokens             int64 // completion tokens (includes reasoning tokens)
	ReasoningOutputTokens    int64 // reasoning tokens (a subset of OutputTokens, not an addition)
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

// ReadSpawnedAgentID returns the thread id of the agent a spawn call created,
// as Codex itself recorded it, or "" when the rollout does not (yet) say.
//
// Codex writes an item_completed event carrying a SubAgentActivity item at the
// moment a spawn call starts an agent:
//
//	{"type":"event_msg","payload":{"type":"item_completed", ...,
//	  "item":{"type":"SubAgentActivity","id":"<spawn call id>",
//	          "kind":"started","agent_thread_id":"<the new agent>"}}}
//
// The item's id is the spawn call's id, which arrives on the hook payload as
// tool_use_id, so this is an explicit mapping rather than an inference. It is
// written into the rollout of the thread that MADE the call — the main session's
// for a top-level spawn, the parent agent's for a nested one — which is exactly
// the file the hook payload's transcript_path points at, at any depth.
//
// Only "started" is read. Codex also writes "interacted" for later exchanges with
// an agent already running, under a different call id; treating one as a spawn
// would anchor a second span onto the same agent.
func ReadSpawnedAgentID(rolloutPath, spawnCallID string) (string, error) {
	if rolloutPath == "" || spawnCallID == "" || strings.HasSuffix(rolloutPath, ".zst") {
		return "", nil
	}
	f, err := os.Open(rolloutPath)
	if err != nil {
		return "", fmt.Errorf("opening rollout: %w", err)
	}
	defer func() { _ = f.Close() }()

	var agentID string
	forEachRecord(f, func(line subAgentActivityLine) bool {
		item := line.Payload.Item
		if line.Type != "event_msg" || item.Type != "SubAgentActivity" {
			return true
		}
		if item.Kind == "started" && item.ID == spawnCallID {
			agentID = item.AgentThreadID
			return false
		}
		return true
	})
	return agentID, nil
}

// forEachRecord decodes a rollout's JSON records in order and passes each to fn,
// stopping early when fn returns false.
//
// The error handling is the load-bearing part, and it is the rule
// transcript.forEachEntry already follows for the same reason. A decoder recovers
// from a type mismatch: it consumed the value, so the stream is intact and the
// record can be skipped. It cannot recover from a syntax error. It does not
// advance, and More() answers from the buffered bytes rather than the decoder's
// stuck state, so it stays true while every Decode returns the same error
// immediately. A loop that treats the two alike spins on a core until the hook
// times out.
//
// A half-written final record is the normal case here rather than corruption:
// these hooks read a rollout Codex is still appending to, and
// waitForSpawnedAgentID reads it during the flush on purpose. So a type error
// skips one record and a syntax error ends the read, which is the most a decoder
// can honestly do.
func forEachRecord[T any](f *os.File, fn func(record T) bool) {
	dec := json.NewDecoder(f)
	for dec.More() {
		var record T
		if err := dec.Decode(&record); err != nil {
			var typeErr *json.UnmarshalTypeError
			if errors.As(err, &typeErr) {
				continue
			}
			return
		}
		if !fn(record) {
			return
		}
	}
}

// subAgentActivityLine is the subset of a rollout record that carries the
// spawn-call-to-agent mapping.
type subAgentActivityLine struct {
	Type    string `json:"type"`
	Payload struct {
		Type string `json:"type"`
		Item struct {
			Type          string `json:"type"`
			ID            string `json:"id"`
			Kind          string `json:"kind"`
			AgentThreadID string `json:"agent_thread_id"`
		} `json:"item"`
	} `json:"payload"`
}

// Rollout is everything one pass over a rollout file yields.
type Rollout struct {
	Usage  *Usage    // the most recent turn's token counts; nil when the file has none
	Limits *Limits   // account allowance state; nil when the CLI predates the field
	Skill  *SkillUse // the skill the most recent turn loaded; nil when it loaded none
}

// SkillUse is the skill a turn used, and who chose it.
//
// Codex does not call a skill as a tool the way Claude Code does. It loads one
// by INJECTING it into the conversation — "progressive disclosure": the model
// sees every skill's name and description, and the full SKILL.md arrives only
// once it picks one. So there is no PostToolUse to enrich, and the only record
// that a skill was used at all is in the rollout.
type SkillUse struct {
	Name string
	// Source is skillSourceCommand when the person named the skill themselves,
	// with Codex's $name mention, and skillSourceModel when the model chose it
	// from the catalogue. The same two routes Claude Code has, reached
	// differently: there the person types a slash command, here a $ mention.
	Source string
}

// Codex's own markup for a loaded skill, injected as a user message:
//
//	<skill>
//	<name>qa-echo</name>
//	<path>/…/.agents/skills/qa-echo/SKILL.md</path>
//
// Not to be confused with the <skills_instructions> block, which is a developer
// message listing every skill AVAILABLE. That one says nothing about what was
// used, and treating it as a signal would attribute a skill to every turn.
var skillNamePattern = regexp.MustCompile(`(?s)^\s*<skill>.*?<name>([^<]+)</name>`)

// Source values, matching the pipeline's own constants. Kept as literals rather
// than imported because internal/pipeline imports this package.
const (
	skillSourceCommand = "command"
	skillSourceModel   = "model"
)

// codexInjectedTags open the user messages Codex writes itself. Only these are
// held out of the person's words when looking for a $mention.
//
// An earlier version held out every message starting with "<", on the reasoning
// that Codex's injections are XML-ish and a person's prompt is not. That loses a
// real prompt that happens to begin with an angle bracket — asking about a
// generic type, quoting HTML — and reports `model` for a skill the person named
// themselves. A short list of known tags is narrower and fails the safer way:
// an unrecognised injection is scanned for a mention it almost never contains.
var codexInjectedTags = []string{
	"<skill>",
	"<skills_instructions>",
	"<recommended_plugins>",
	"<task-notification>",
}

func isCodexInjection(body string) bool {
	trimmed := strings.TrimSpace(body)
	for _, tag := range codexInjectedTags {
		if strings.HasPrefix(trimmed, tag) {
			return true
		}
	}
	return false
}

// messageText joins a message's text parts. Content is raw JSON because its
// shape varies, so a shape this does not recognise yields no text rather than
// failing the line it arrived on.
func messageText(content json.RawMessage) string {
	if len(content) == 0 {
		return ""
	}
	var parts []struct {
		Text string `json:"text"`
	}
	if err := json.Unmarshal(content, &parts); err == nil {
		var out strings.Builder
		for _, part := range parts {
			out.WriteString(part.Text)
		}
		return out.String()
	}
	// Some records carry the body as a plain string.
	var plain string
	if err := json.Unmarshal(content, &plain); err == nil {
		return plain
	}
	return ""
}

// mentions reports whether the person's words name this skill with Codex's $
// mention. The boundary check matters: a plain Contains of "$"+name also matches
// a longer name, so a prompt naming $qa-echo-v2 would be read as choosing
// qa-echo when that is the skill the turn happened to load.
func mentions(prompt, skill string) bool {
	needle := "$" + skill
	for i := 0; ; {
		at := strings.Index(prompt[i:], needle)
		if at < 0 {
			return false
		}
		end := i + at + len(needle)
		if end == len(prompt) || !isSkillNameByte(prompt[end]) {
			return true
		}
		i = end
	}
}

// isSkillNameByte reports whether a byte can continue a skill name, which is
// what decides where a $mention ends.
func isSkillNameByte(b byte) bool {
	switch {
	case b >= 'a' && b <= 'z', b >= 'A' && b <= 'Z', b >= '0' && b <= '9':
		return true
	// A colon carries a plugin-qualified name such as writing:unslop. A period
	// and a slash do not: they end far more sentences and paths than they
	// continue skill names, and treating them as part of the name loses a
	// mention written as "$qa-echo."
	case b == '-', b == '_', b == ':':
		return true
	}
	return false
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
		// A response_item/message carries the conversation itself, which is
		// where a loaded skill shows up. Codex injects several user messages of
		// its own alongside the person's — <recommended_plugins>, <skill> — so
		// the role alone does not say who wrote it.
		//
		// Content stays raw on purpose. Decoding it as a shape here would couple
		// message parsing to usage parsing: a record whose content is a string
		// rather than an array of parts fails to decode, the loop skips the whole
		// line, and that line's info.last_token_usage and rate_limits are lost in
		// silence. Only the message branch looks inside it, and a failure there
		// costs a skill attribute rather than a turn's tokens.
		Role    string          `json:"role"`
		Content json.RawMessage `json:"content"`
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
	InputTokens int64 `json:"input_tokens"`
	// The two cache halves. cached_input_tokens is the part served FROM cache;
	// cache_write_input_tokens is the part written TO it. Both are subsets of
	// input_tokens, so neither is added to it. The write half went unparsed
	// until 2026-08-26, which is why no Codex span could carry
	// gen_ai.usage.cache_creation.input_tokens: the field was on the wire the
	// whole time, and every value observed so far happens to be zero.
	CachedInputTokens     int64 `json:"cached_input_tokens"`
	CacheWriteInputTokens int64 `json:"cache_write_input_tokens"`
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

	var turn Usage
	var hasUsage bool
	var limits *Limits
	var turnSkill string
	var turnPrompt strings.Builder
	forEachRecord(f, func(line rolloutLine) bool {
		// The conversation, for the skill a turn loaded and who asked for it.
		if line.Type == "response_item" && line.Payload.Type == "message" && line.Payload.Role == "user" {
			body := messageText(line.Payload.Content)
			if match := skillNamePattern.FindStringSubmatch(body); match != nil {
				// Last one wins. A turn that loads two skills can only be
				// labelled with one, and the later choice is the more recent.
				turnSkill = match[1]
			} else if !isCodexInjection(body) {
				// Only the person's words can say they asked for a skill by
				// name, so Codex's own injected user messages are held out.
				turnPrompt.WriteString(body)
			}
			return true
		}

		if line.Type != "event_msg" {
			return true
		}
		switch line.Payload.Type {
		case "user_message", "task_started":
			// New turn — discard usage accumulated for the previous turn so only
			// the most recent turn's counts survive. Limits deliberately survive:
			// they describe the account, not the turn.
			//
			// Two event names because Codex changed which one it writes.
			// 0.142.5 wrote user_message, 0.149.1 writes
			// task_started and no user_message.
			//
			// Both are safe to reset on: each is written once, before its own
			// turn's token_count events.
			turn = Usage{}
			hasUsage = false
			turnSkill = ""
			turnPrompt.Reset()
		case "token_count":
			u := line.Payload.Info.LastTokenUsage
			// Emit Codex's counts as-is. input_tokens is the total prompt count
			// inclusive of the cached portion — which is exactly what the cost
			// processor expects (it derives uncached = input − cache_read −
			// cache_creation itself). Subtracting cached here would double-count
			// the discount and under-price the turn.
			turn.InputTokens += u.InputTokens
			turn.CacheReadInputTokens += u.CachedInputTokens
			turn.CacheCreationInputTokens += u.CacheWriteInputTokens
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
		return true
	})

	out := &Rollout{Limits: limits}
	if hasUsage {
		out.Usage = &turn
	}
	if turnSkill != "" {
		// The person asked for it if their own words carry Codex's $mention;
		// otherwise the model picked it out of the catalogue itself.
		source := skillSourceModel
		if mentions(turnPrompt.String(), turnSkill) {
			source = skillSourceCommand
		}
		out.Skill = &SkillUse{Name: turnSkill, Source: source}
	}
	return out, nil
}
