// SPDX-FileCopyrightText: Copyright 2026 Dash0 Inc.
// SPDX-License-Identifier: Apache-2.0

package copilot

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Usage is the per-turn token/cost/model recovered from the native-OTel file.
type Usage struct {
	InputTokens           int64
	OutputTokens          int64
	CacheReadInputTokens  int64
	ReasoningOutputTokens int64
	Cost                  float64
	Model                 string
	ResponseText          string // final assistant text of the turn (from gen_ai.output.messages)
}

// chatSpan is one native-OTel `chat` span: its unique span id (top-level) plus
// its attribute map.
type chatSpan struct {
	spanID string
	attrs  map[string]any
}

// cursorFile persists the id of the last consumed chat span (see cursor).
const cursorFile = "otel_cursor.json"

// staleFileTTL bounds how long a native-OTel file (or an empty dir) left behind
// by an unclean prior exit — where the launcher's `rm` never ran — lingers
// before the sweep removes it.
const staleFileTTL = 24 * time.Hour

// cursor records the id of the last native span whose usage was consumed. A
// single id suffices because each launch's file is append-only, so "the spans
// after this id" is well defined; and because each launch writes disjoint span
// ids to its own file, a cursor not found in the current file simply means a
// new (resumed/rotated) file — all of whose spans are then fresh.
type cursor struct {
	LastSpanID string `json:"last_span_id"`
}

// OtelDir is the convention directory both the launch shell function (written
// by dash0-configure) and this reader agree on for native-OTel files. It is a
// fixed path — NOT derived from an env var — because Copilot does not pass the
// launch environment to hook processes, so the two sides cannot communicate a
// path at runtime and must share a baked-in convention. DASH0_COPILOT_OTEL_DIR
// overrides it (tests only; the bootstrap does not set it in production).
func OtelDir() string {
	if v := os.Getenv("DASH0_COPILOT_OTEL_DIR"); v != "" {
		return v
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return filepath.Join(os.TempDir(), "dash0-agent-plugin", "copilot", "otel")
	}
	return filepath.Join(home, ".local", "state", "dash0-agent-plugin", "copilot", "otel")
}

// ReadTurnUsage recovers usage for the turn that just ended and returns the
// cursor value the caller must persist (via SaveCursor) once the span is
// emitted. It reads the NEWEST native-OTel file carrying this conversation's
// spans (so a stale file left by an unclean prior exit is never preferred over
// the live one), then sums the chat spans after the last-consumed span id.
//
// Correct across --resume / rotation / re-runs: each launch writes an
// append-only file of disjoint span ids, so an unknown cursor means a fresh
// file (all spans new) and a known cursor bounds exactly this turn's new spans;
// re-running the same Stop finds the cursor at the end → nothing new. Sub-agent
// chat spans share the conversation.id and so fold into the turn total (flat
// attribution). Returns (nil, "") when there is no file or nothing new — the
// caller then emits the span without usage (graceful degradation).
func ReadTurnUsage(sessionID, sessionDir string) (*Usage, string) {
	if sessionID == "" {
		return nil, ""
	}
	spans := newestConversationChatSpans(OtelDir(), sessionID)
	if len(spans) == 0 {
		return nil, ""
	}
	fresh := spansAfterCursor(spans, loadCursor(sessionDir))
	if len(fresh) == 0 {
		return nil, ""
	}

	u := &Usage{}
	for _, s := range fresh {
		a := s.attrs
		u.InputTokens += attrInt(a, "gen_ai.usage.input_tokens")
		u.OutputTokens += attrInt(a, "gen_ai.usage.output_tokens")
		u.CacheReadInputTokens += attrInt(a, "gen_ai.usage.cache_read.input_tokens")
		u.ReasoningOutputTokens += attrInt(a, "gen_ai.usage.reasoning.output_tokens")
		u.Cost += attrFloat(a, "github.copilot.cost")
		if m := attrString(a, "gen_ai.request.model"); m != "" {
			u.Model = m // last non-empty model in the turn
		}
		if txt := assistantTextFromOutput(attrString(a, "gen_ai.output.messages")); txt != "" {
			u.ResponseText = txt // last non-empty assistant text in the turn = the final response
		}
	}
	return u, fresh[len(fresh)-1].spanID
}

// assistantTextFromOutput extracts the assistant's text from a chat span's
// gen_ai.output.messages value (a JSON array of GenAI messages). It returns the
// concatenated text parts of the LAST assistant-role message — i.e. the model's
// final textual reply for that round-trip (tool-call parts are ignored; those
// surface as their own tool spans). Returns "" if the value is absent or has no
// assistant text, so the caller degrades gracefully.
func assistantTextFromOutput(outputMessages string) string {
	if outputMessages == "" {
		return ""
	}
	var msgs []struct {
		Role  string `json:"role"`
		Parts []struct {
			Type    string `json:"type"`
			Content string `json:"content"`
		} `json:"parts"`
	}
	if err := json.Unmarshal([]byte(outputMessages), &msgs); err != nil {
		return ""
	}
	last := ""
	for _, m := range msgs {
		if m.Role != "assistant" {
			continue
		}
		var b strings.Builder
		for _, p := range m.Parts {
			if p.Type == "text" && p.Content != "" {
				if b.Len() > 0 {
					b.WriteString("\n")
				}
				b.WriteString(p.Content)
			}
		}
		if b.Len() > 0 {
			last = b.String()
		}
	}
	return last
}

// LatestModel returns the model of the most recent chat span for this
// conversation (from the newest native-OTel file), or "" if unavailable. Used
// to tag tool spans (PostToolUse), which otherwise carry no model — the file's
// chat spans are the only place Copilot's model surfaces.
func LatestModel(sessionID string) string {
	if sessionID == "" {
		return ""
	}
	spans := newestConversationChatSpans(OtelDir(), sessionID)
	for i := len(spans) - 1; i >= 0; i-- {
		if m := attrString(spans[i].attrs, "gen_ai.request.model"); m != "" {
			return m
		}
	}
	return ""
}

// spansAfterCursor returns the spans following the one whose id == last. If last
// is empty (first turn) or absent from this file (a new/rotated file after
// --resume), ALL spans are returned — correct because each launch's file holds
// only its own, disjoint spans.
func spansAfterCursor(spans []chatSpan, last string) []chatSpan {
	if last == "" {
		return spans
	}
	for i, s := range spans {
		if s.spanID == last {
			return spans[i+1:]
		}
	}
	return spans
}

// newestConversationChatSpans returns the chat spans (in file order) from the
// most-recently-modified *.jsonl file that contains this conversation's spans.
// Preferring the newest file avoids reading a frozen stale file that an unclean
// prior exit left behind with the same conversation.id.
func newestConversationChatSpans(dir, sessionID string) []chatSpan {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var best []chatSpan
	var bestMod time.Time
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".jsonl") {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		spans := chatSpansForConversation(filepath.Join(dir, e.Name()), sessionID)
		if len(spans) == 0 {
			continue
		}
		if best == nil || info.ModTime().After(bestMod) {
			best, bestMod = spans, info.ModTime()
		}
	}
	return best
}

func chatSpansForConversation(path, sessionID string) []chatSpan {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer func() { _ = f.Close() }()

	var spans []chatSpan
	sc := bufio.NewScanner(f)
	// 8MB per-line cap. Native-OTel content capture is off by default, so chat
	// spans are small and this is effectively never hit; a span exceeding it
	// would stop the scan, dropping that span and later ones. Accepted v1
	// limitation (code review #9) — revisit (skip-and-continue) if content
	// capture becomes common.
	sc.Buffer(make([]byte, 0, 1024*1024), 8*1024*1024)
	for sc.Scan() {
		line := sc.Bytes()
		if len(line) == 0 {
			continue
		}
		var rec map[string]any
		if err := json.Unmarshal(line, &rec); err != nil {
			continue // skip torn/partial lines (concurrent writers) — graceful
		}
		if t, _ := rec["type"].(string); t != "span" {
			continue
		}
		// Native chat spans are named "chat <model>"; the trailing space avoids
		// matching unrelated names like "chatbot".
		if name, _ := rec["name"].(string); !strings.HasPrefix(name, "chat ") {
			continue
		}
		attrs, _ := rec["attributes"].(map[string]any)
		if attrs == nil {
			continue
		}
		if conv, _ := attrs["gen_ai.conversation.id"].(string); conv != sessionID {
			continue
		}
		spanID, _ := rec["spanId"].(string)
		spans = append(spans, chatSpan{spanID: spanID, attrs: attrs})
	}
	// Tolerate scan errors (e.g. an oversized/torn line from a concurrent
	// writer): return whatever parsed cleanly — graceful degradation.
	_ = sc.Err()
	return spans
}

func loadCursor(sessionDir string) string {
	data, err := os.ReadFile(filepath.Join(sessionDir, cursorFile))
	if err != nil {
		return ""
	}
	var c cursor
	if err := json.Unmarshal(data, &c); err != nil {
		return ""
	}
	return c.LastSpanID
}

// SaveCursor persists the id of the last consumed chat span. A write failure is
// logged rather than silently swallowed: a lost cursor makes the next turn
// re-sum from the start and double-count.
func SaveCursor(sessionDir, lastSpanID string) {
	data, err := json.Marshal(cursor{LastSpanID: lastSpanID})
	if err != nil {
		return
	}
	if err := os.WriteFile(filepath.Join(sessionDir, cursorFile), data, 0o644); err != nil {
		fmt.Fprintf(os.Stderr, "copilot-on-event: persisting usage cursor: %v\n", err)
	}
}

// SweepOldOtelFiles removes native-OTel files (and now-empty dirs) under
// OtelDir() older than staleFileTTL — leftovers from processes that exited
// uncleanly so the launcher's `rm` never ran. Best-effort; called on SessionStart.
func SweepOldOtelFiles(now time.Time) {
	dir := OtelDir()
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		info, err := e.Info()
		if err != nil || now.Sub(info.ModTime()) < staleFileTTL {
			continue
		}
		p := filepath.Join(dir, e.Name())
		if e.IsDir() {
			_ = os.Remove(p) // removes only if empty
		} else if strings.HasSuffix(e.Name(), ".jsonl") {
			_ = os.Remove(p)
		}
	}
}

// attrInt/attrFloat/attrString read a native-OTel flat attribute (JSON numbers
// decode as float64).
func attrInt(a map[string]any, key string) int64 {
	switch v := a[key].(type) {
	case float64:
		return int64(v)
	case int64:
		return v
	}
	return 0
}

func attrFloat(a map[string]any, key string) float64 {
	if v, ok := a[key].(float64); ok {
		return v
	}
	return 0
}

func attrString(a map[string]any, key string) string {
	s, _ := a[key].(string)
	return s
}
