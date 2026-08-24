// SPDX-FileCopyrightText: Copyright 2026 Dash0 Inc.
// SPDX-License-Identifier: Apache-2.0

package transcript

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
)

// Usage holds aggregated token usage for a turn.
type Usage struct {
	InputTokens              int64
	OutputTokens             int64
	CacheCreationInputTokens int64
	CacheReadInputTokens     int64
	// CacheCreation5mInputTokens and CacheCreation1hInputTokens decompose
	// CacheCreationInputTokens by cache TTL. Anthropic prices 1h cache writes
	// higher than 5m writes, so the split is needed for accurate cost; there is
	// no OpenTelemetry semantic-convention attribute for it (the semconv
	// standardizes only input/output tokens), so it is emitted under dash0.gen_ai.usage.
	// A source that omits the breakdown leaves both at 0, which costs nothing.
	CacheCreation5mInputTokens int64
	CacheCreation1hInputTokens int64
	// ReasoningOutputTokens is the share of OutputTokens spent on extended
	// thinking. It is a subset, not an addition: thinking tokens are billed at
	// the output rate and are already inside OutputTokens, so cost does not
	// change — but without the split a long deliberation is indistinguishable
	// from a long answer. Named for the OTel key it is exported under
	// (gen_ai.usage.reasoning.output_tokens), which Copilot already reports, so
	// one query covers both runtimes. Zero when the turn did no thinking.
	ReasoningOutputTokens int64
}

// add folds one API call's effective usage into the aggregate. A nil
// CacheCreation (source gave no TTL split) leaves the breakdown totals at 0.
func (u *Usage) add(eff usageData) {
	u.InputTokens += eff.InputTokens
	u.OutputTokens += eff.OutputTokens
	u.CacheCreationInputTokens += eff.CacheCreationInputTokens
	u.CacheReadInputTokens += eff.CacheReadInputTokens
	if eff.CacheCreation != nil {
		u.CacheCreation5mInputTokens += eff.CacheCreation.Ephemeral5mInputTokens
		u.CacheCreation1hInputTokens += eff.CacheCreation.Ephemeral1hInputTokens
	}
	if eff.OutputTokensDetails != nil {
		u.ReasoningOutputTokens += eff.OutputTokensDetails.ThinkingTokens
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
	InputTokens              int64                `json:"input_tokens"`
	OutputTokens             int64                `json:"output_tokens"`
	CacheCreationInputTokens int64                `json:"cache_creation_input_tokens"`
	CacheReadInputTokens     int64                `json:"cache_read_input_tokens"`
	CacheCreation            *cacheCreation       `json:"cache_creation"`
	OutputTokensDetails      *outputTokensDetails `json:"output_tokens_details"`
	Iterations               []usageData          `json:"iterations"`
}

// cacheCreation splits cache-creation tokens by TTL.
type cacheCreation struct {
	Ephemeral5mInputTokens int64 `json:"ephemeral_5m_input_tokens"`
	Ephemeral1hInputTokens int64 `json:"ephemeral_1h_input_tokens"`
}

// outputTokensDetails breaks the output token count down by kind. Only thinking
// tokens are reported today.
type outputTokensDetails struct {
	ThinkingTokens int64 `json:"thinking_tokens"`
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
	var thinking int64
	for _, it := range u.Iterations {
		sum.InputTokens += it.InputTokens
		sum.OutputTokens += it.OutputTokens
		sum.CacheCreationInputTokens += it.CacheCreationInputTokens
		sum.CacheReadInputTokens += it.CacheReadInputTokens
		if it.OutputTokensDetails != nil {
			thinking += it.OutputTokensDetails.ThinkingTokens
		}
	}
	// Thinking tokens are a subset of the summed OutputTokens, so they are summed
	// from the iterations too. When no iteration reports the detail, fall back to
	// the top-level value, which mirrors the final attempt.
	switch {
	case thinking > 0:
		sum.OutputTokensDetails = &outputTokensDetails{ThinkingTokens: thinking}
	case u.OutputTokensDetails != nil:
		sum.OutputTokensDetails = u.OutputTokensDetails
	}
	// CacheCreation lives only on the top-level usage object, not on
	// individual iterations, so carry it through unchanged. On a fallback turn it
	// reflects the final iteration only, so the split may under-sum the
	// iteration-summed CacheCreationInputTokens total — the flat total stays
	// authoritative; the split is best-effort.
	sum.CacheCreation = u.CacheCreation
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

	err := forEachEntry(transcriptPath, func(entry transcriptEntry) bool {
		if isRealUserMessage(entry) {
			// New turn — the calls counted so far belong to the turn that just
			// ended, so a replay of them later in the file must not count again.
			for _, key := range callOrder {
				counted[key] = true
			}
			perCall = make(map[string]*usageData)
			callOrder = nil
			hasUsage = false
			return true
		}

		if entry.Type != "assistant" || entry.Message == nil || entry.Message.Usage == nil {
			return true
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
			return true
		}
		hasUsage = true
		if _, seen := perCall[key]; !seen {
			callOrder = append(callOrder, key)
		}
		perCall[key] = entry.Message.Usage
		return true
	})
	if err != nil {
		return nil, err
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
	var lastReason string
	var sawAssistant bool
	err := forEachEntry(transcriptPath, func(entry transcriptEntry) bool {
		if isRealUserMessage(entry) {
			// New turn — only the current turn's terminal state matters.
			lastReason = ""
			sawAssistant = false
			return true
		}
		if entry.Type == "assistant" && entry.Message != nil {
			sawAssistant = true
			lastReason = entry.Message.StopReason
		}
		return true
	})
	if err != nil {
		return false, err
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
	var customTitle, aiTitle string
	_ = forEachEntry(transcriptPath, func(entry titleEntry) bool {
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
		return true
	})
	if customTitle != "" {
		return customTitle
	}
	return aiTitle
}

// HasAssistantEntry reports whether the current turn holds an assistant entry.
// It separates "the model has not answered this turn yet" from "this transcript
// does not record models", which is the difference between a wait that will pay
// off and one that will time out. A missing or unreadable file counts as no entry.
func HasAssistantEntry(transcriptPath string) bool {
	_, found := ReadCurrentTurnModel(transcriptPath)
	return found
}

// commandNamePattern matches the command tag Claude Code writes into the user
// entry when it expands a slash command, e.g.
// <command-name>/writing:unslop</command-name>.
var commandNamePattern = regexp.MustCompile(`<command-name>\s*/([^<\s]+)\s*</command-name>`)

// skillBaseDirPattern matches the preamble of the skill-instructions relay that
// Claude Code injects as an isMeta user entry when a skill loads. The captured
// path ends in the skill's own directory, which names the skill.
//
// The match runs on the raw JSON bytes of the entry's content, so the path is
// terminated by the backslash of the "\n\n" that follows it, or by the quote
// closing the JSON string. Whitespace is deliberately not a terminator: a real
// newline cannot appear inside a JSON string, so the only whitespace this could
// stop at is a space that belongs to the path. Excluding it dropped every
// command-invoked skill for anyone whose skills live under a path containing a
// space, which a home directory alone is enough to produce.
var skillBaseDirPattern = regexp.MustCompile(`Base directory for this skill: ([^"\\]+)`)

// ReadTurnSkillCommand returns the skill invoked by a slash command in the most
// recent turn, as the plugin-qualified name the user typed (e.g.
// "writing:unslop"), or empty string when the turn began no such invocation.
//
// A slash command is expanded client-side, before any tool runs, so it fires no
// PreToolUse/PostToolUse pair and produces no tool span. The transcript is the
// only place the invocation is recorded, in two parts: the turn's user entry
// carries a <command-name> tag, and a skill load appends an isMeta entry naming
// the skill's base directory.
//
// Both parts are required, and the command's last colon-separated segment must
// match the skill directory's name. That conjunction is what keeps a built-in
// command out of the count: /compact writes a <command-name> tag too, but loads
// no skill. A prompt that merely mentions a slash command is likewise ignored,
// because the tag is written by the expansion, not by the user's text.
func ReadTurnSkillCommand(transcriptPath string) string {
	var command, skillDir string
	_ = forEachEntry(transcriptPath, func(entry transcriptEntry) bool {
		if isRealUserMessage(entry) {
			// New turn — only the current turn's invocation counts. The entry that
			// carries the command tag IS the turn's user message, so read it here.
			command, skillDir = "", ""
			if entry.Message != nil {
				if m := commandNamePattern.FindSubmatch(entry.Message.Content); len(m) > 1 {
					command = string(m[1])
				}
			}
			return true
		}
		if command == "" || skillDir != "" || entry.Type != "user" || !entry.IsMeta || entry.Message == nil {
			return true
		}
		if m := skillBaseDirPattern.FindSubmatch(entry.Message.Content); len(m) > 1 {
			// TrimSpace covers a trailing space between the path and the newline,
			// which the pattern now keeps rather than treating as the end.
			skillDir = path.Base(strings.TrimSpace(string(m[1])))
		}
		return true
	})

	if command == "" || skillDir == "" {
		return ""
	}
	segments := strings.Split(command, ":")
	if segments[len(segments)-1] != skillDir {
		return ""
	}
	return command
}

// SubagentPath returns the transcript Claude Code writes for one sub-agent,
// derived from the main session's transcript path, the session ID, and the
// agent ID:
//
//	<dir of the session transcript>/<session id>/subagents/agent-<agent id>.jsonl
//
// It has to be derived because the payload does not carry it. Only SubagentStop
// reports agent_transcript_path; a sub-agent's own PostToolUse carries
// transcript_path alone, which names the *main* session's file. A reader that
// takes that at face value answers questions about the sub-agent using the
// parent's transcript.
//
// claude/tools/claude-code-usage-audit.py builds the same path from the same
// three parts, so this layout is already relied on elsewhere in the repo.
//
// Returns empty string when any part is missing or when the derived file does
// not exist. Absent is the right answer for a caller that would otherwise read
// the wrong transcript.
func SubagentPath(sessionTranscriptPath, sessionID, agentID string) string {
	if sessionTranscriptPath == "" || sessionID == "" || agentID == "" {
		return ""
	}
	// Both are single path segments in the derived name, so a separator in
	// either would escape the session directory.
	if strings.ContainsAny(sessionID, `/\`) || strings.ContainsAny(agentID, `/\`) {
		return ""
	}
	candidate := filepath.Join(filepath.Dir(sessionTranscriptPath), sessionID,
		"subagents", "agent-"+agentID+".jsonl")
	if _, err := os.Stat(candidate); err != nil {
		return ""
	}
	return candidate
}

// ReadModel reads the transcript file and returns the model from the most
// recent assistant message in the current turn, or empty string if none is
// found. A new real user message resets the result so a tool hook that beats
// the current assistant flush waits instead of reusing the previous turn.
func ReadModel(transcriptPath string) string {
	model, _ := ReadCurrentTurnModel(transcriptPath)
	return model
}

// ReadCurrentTurnModel returns the latest model and assistant-presence state
// from one transcript snapshot. Keeping both answers in one scan prevents a
// transcript append between separate model and readiness checks from making a
// caller treat the wrong turn as ready.
func ReadCurrentTurnModel(transcriptPath string) (model string, hasAssistant bool) {
	_ = forEachEntry(transcriptPath, func(entry transcriptEntry) bool {
		if isRealUserMessage(entry) {
			model = ""
			hasAssistant = false
			return true
		}
		if entry.Type == "assistant" && entry.Message != nil {
			hasAssistant = true
			if entry.Message.Model != "" {
				model = entry.Message.Model
			}
		}
		return true
	})
	return model, hasAssistant
}

// forEachEntry decodes the transcript's JSON entries in order and passes each to
// fn, stopping early when fn returns false. It reports an error only when the
// file cannot be opened.
//
// Entries are decoded from the stream rather than read line by line, because
// Claude Code has written pretty-printed entries spanning several lines and a
// line-based reader silently drops those.
//
// The error handling is the load-bearing part. A decoder recovers from a type
// mismatch: it has consumed the value, so the stream stays intact and the entry
// can be skipped. It cannot recover from a syntax error — it does not advance,
// so More() stays true and the same broken bytes decode forever. Every reader
// here used to `continue` on either kind, which turned a half-written final entry
// into an infinite loop that pinned a core until the host's hook timeout killed
// the process. A half-written final entry is the normal case rather than
// corruption, because these hooks read a transcript Claude Code is still
// appending to. So a type error skips one entry and a syntax error ends the
// read, which is the most a decoder can honestly do.
func forEachEntry[T any](transcriptPath string, fn func(entry T) bool) error {
	f, err := os.Open(transcriptPath)
	if err != nil {
		return fmt.Errorf("opening transcript: %w", err)
	}
	defer func() { _ = f.Close() }()

	dec := json.NewDecoder(f)
	for dec.More() {
		var entry T
		if err := dec.Decode(&entry); err != nil {
			var typeErr *json.UnmarshalTypeError
			if errors.As(err, &typeErr) {
				continue
			}
			return nil
		}
		if !fn(entry) {
			return nil
		}
	}
	return nil
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
