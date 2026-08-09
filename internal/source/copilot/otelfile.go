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

// ToolCall is one tool execution of the turn, recovered from a native-OTel
// execute_tool span. Unlike Copilot's postToolUse hooks (zero duration, parent
// turn only), these carry real timing and cover sub-agent tool calls too.
type ToolCall struct {
	SpanID       string // native span id, reused verbatim (16-char hex, same format as ours)
	ParentSpanID string // nearest execute_tool ancestor emitted this turn, or "" (→ the turn's chat span)
	Name         string // gen_ai.tool.name (e.g. "bash", "task")
	Arguments    string // gen_ai.tool.call.arguments (JSON string)
	Result       string // gen_ai.tool.call.result
	CallID       string // gen_ai.tool.call.id (e.g. "call_…"; for `task` this is the sub-agent's hook session id)
	Start, End   time.Time
	Failed       bool // native span status code == ERROR
}

// Turn is everything recovered from the native-OTel file for the turn that just
// ended: aggregated usage (nil if no chat span flushed yet) and the turn's tool
// executions, parent and sub-agent alike.
type Turn struct {
	Usage *Usage
	Tools []ToolCall
}

// otelSpan is one native-OTel span record belonging to this conversation.
type otelSpan struct {
	spanID       string
	parentSpanID string
	name         string
	start, end   time.Time
	failed       bool
	attrs        map[string]any
}

// cursorFile persists the id of the last consumed native span (see cursor).
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

// ReadTurn recovers everything for the turn that just ended — aggregated usage
// AND the turn's tool executions — and returns the cursor value the caller must
// persist (via SaveCursor) once the spans are emitted. It reads the NEWEST
// native-OTel file carrying this conversation's spans (so a stale file left by
// an unclean prior exit is never preferred over the live one), then consumes
// the spans after the last-consumed span id.
//
// Correct across --resume / rotation / re-runs: each launch writes an
// append-only file of disjoint span ids, so an unknown cursor means a fresh
// file (all spans new) and a known cursor bounds exactly this turn's new spans;
// re-running the same Stop finds the cursor at the end → nothing new.
//
// Usage sums the window's `chat` spans; sub-agent chat spans share the
// conversation and fold into the turn total (flat attribution). Tools are the
// window's `execute_tool` spans — including those inside sub-agents — with the
// intermediate invoke_agent/chat layers collapsed: each tool's parent resolves
// to the nearest execute_tool ancestor emitted this turn (nesting a sub-agent's
// tools under its spawning `task` span), or "" for top-level tools (the caller
// parents those under the turn's chat span). A span that Copilot flushes late
// (after this read) folds into the next turn's window — graceful, slightly
// misattributed, rare. Returns (nil, "") when there is no file or nothing new.
func ReadTurn(sessionID, sessionDir string) (*Turn, string) {
	if sessionID == "" {
		return nil, ""
	}
	spans := newestConversationSpans(OtelDir(), sessionID)
	if len(spans) == 0 {
		return nil, ""
	}
	fresh := spansAfterCursor(spans, loadCursor(sessionDir))
	if len(fresh) == 0 {
		return nil, ""
	}

	turn := &Turn{}
	freshTools := make(map[string]bool)
	for _, s := range fresh {
		switch {
		case strings.HasPrefix(s.name, "chat "):
			if turn.Usage == nil {
				turn.Usage = &Usage{}
			}
			u, a := turn.Usage, s.attrs
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
		case strings.HasPrefix(s.name, "execute_tool"):
			freshTools[s.spanID] = true
			turn.Tools = append(turn.Tools, ToolCall{
				SpanID:    s.spanID,
				Name:      attrString(s.attrs, "gen_ai.tool.name"),
				Arguments: attrString(s.attrs, "gen_ai.tool.call.arguments"),
				Result:    attrString(s.attrs, "gen_ai.tool.call.result"),
				CallID:    attrString(s.attrs, "gen_ai.tool.call.id"),
				Start:     s.start,
				End:       s.end,
				Failed:    s.failed,
			})
		}
	}

	// Collapse the invoke_agent/chat layers: parent each tool to its nearest
	// execute_tool ancestor emitted this turn. Ancestry walks the FULL span list
	// (a parent record precedes only by id, not necessarily by window), but the
	// resolved parent must itself be emitted this turn to keep the link intact.
	byID := make(map[string]otelSpan, len(spans))
	for _, s := range spans {
		byID[s.spanID] = s
	}
	for i := range turn.Tools {
		turn.Tools[i].ParentSpanID = nearestFreshToolAncestor(byID, freshTools, turn.Tools[i].SpanID)
	}

	return turn, fresh[len(fresh)-1].spanID
}

// nearestFreshToolAncestor walks up the native parent chain from spanID and
// returns the first execute_tool ancestor that is being emitted this turn, or
// "" if the chain exits the known tree first (top-level tool → chat span).
func nearestFreshToolAncestor(byID map[string]otelSpan, freshTools map[string]bool, spanID string) string {
	seen := map[string]bool{spanID: true}
	cur := byID[spanID].parentSpanID
	for cur != "" && !seen[cur] {
		seen[cur] = true
		s, ok := byID[cur]
		if !ok {
			return ""
		}
		if strings.HasPrefix(s.name, "execute_tool") && freshTools[s.spanID] {
			return s.spanID
		}
		cur = s.parentSpanID
	}
	return ""
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

// spansAfterCursor returns the spans following the one whose id == last. If last
// is empty (first turn) or absent from this file (a new/rotated file after
// --resume), ALL spans are returned — correct because each launch's file holds
// only its own, disjoint spans.
func spansAfterCursor(spans []otelSpan, last string) []otelSpan {
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

// newestConversationSpans returns this conversation's spans (in file order)
// from the most-recently-modified *.jsonl file that contains them. Preferring
// the newest file avoids reading a frozen stale file that an unclean prior exit
// left behind with the same conversation.id.
func newestConversationSpans(dir, sessionID string) []otelSpan {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var best []otelSpan
	var bestMod time.Time
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".jsonl") {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		spans := conversationSpans(filepath.Join(dir, e.Name()), sessionID)
		if len(spans) == 0 {
			continue
		}
		if best == nil || info.ModTime().After(bestMod) {
			best, bestMod = spans, info.ModTime()
		}
	}
	return best
}

// rawSpan is one parsed native-OTel span record before conversation filtering.
type rawSpan struct {
	span    otelSpan
	traceID string
	conv    string
}

// conversationSpans returns the file's spans belonging to this conversation, in
// file order. Only `chat` and `invoke_agent` spans carry gen_ai.conversation.id;
// execute_tool spans carry none — but every span of a turn (sub-agents included,
// via context propagation) shares the turn's native traceId. Membership is
// therefore: carries the conversation.id directly, OR shares a traceId with a
// span that does.
func conversationSpans(path, sessionID string) []otelSpan {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer func() { _ = f.Close() }()

	var raws []rawSpan
	convTraces := make(map[string]bool)
	sc := bufio.NewScanner(f)
	// 8MB per-line cap. A span exceeding it would stop the scan, dropping that
	// span and later ones. Accepted v1 limitation (code review #9) — revisit
	// (skip-and-continue) if oversized spans show up in practice.
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
		attrs, _ := rec["attributes"].(map[string]any)
		if attrs == nil {
			continue
		}
		spanID, _ := rec["spanId"].(string)
		parentID, _ := rec["parentSpanId"].(string)
		name, _ := rec["name"].(string)
		traceID, _ := rec["traceId"].(string)
		conv := attrString(attrs, "gen_ai.conversation.id")
		if conv == sessionID && traceID != "" {
			convTraces[traceID] = true
		}
		raws = append(raws, rawSpan{
			span: otelSpan{
				spanID:       spanID,
				parentSpanID: parentID,
				name:         name,
				start:        otelTime(rec["startTime"]),
				end:          otelTime(rec["endTime"]),
				failed:       otelFailed(rec["status"]),
				attrs:        attrs,
			},
			traceID: traceID,
			conv:    conv,
		})
	}
	// Tolerate scan errors (e.g. an oversized/torn line from a concurrent
	// writer): keep whatever parsed cleanly — graceful degradation.
	_ = sc.Err()

	var spans []otelSpan
	for _, r := range raws {
		if r.conv == sessionID || (r.traceID != "" && convTraces[r.traceID]) {
			spans = append(spans, r.span)
		}
	}
	return spans
}

// otelTime converts a native-OTel timestamp — a [seconds, nanoseconds] JSON
// array — to a time.Time. Returns the zero time if the shape is unexpected.
func otelTime(v any) time.Time {
	arr, ok := v.([]any)
	if !ok || len(arr) != 2 {
		return time.Time{}
	}
	sec, ok1 := arr[0].(float64)
	nsec, ok2 := arr[1].(float64)
	if !ok1 || !ok2 {
		return time.Time{}
	}
	return time.Unix(int64(sec), int64(nsec)).UTC()
}

// otelFailed reports whether a native span's status marks it as failed
// (OTel status code 2 = ERROR).
func otelFailed(v any) bool {
	m, ok := v.(map[string]any)
	if !ok {
		return false
	}
	code, _ := m["code"].(float64)
	return code == 2
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
