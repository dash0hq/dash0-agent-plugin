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

// Usage is the per-turn token count, model and response recovered from the
// native-OTel file. Copilot's own cost figure is not among them: see the note in
// ReadTurn's chat branch.
type Usage struct {
	InputTokens           int64
	OutputTokens          int64
	CacheReadInputTokens  int64
	ReasoningOutputTokens int64
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
	SkillName    string // github.copilot.tool.parameters.skill_name
	Result       string // gen_ai.tool.call.result
	CallID       string // gen_ai.tool.call.id (e.g. "call_…"; for `task` this is the sub-agent's hook session id)
	Start, End   time.Time
	Failed       bool // native span status code == ERROR
}

// SubAgent is one sub-agent invocation of the turn, recovered from a native
// invoke_agent span nested under the tool that spawned it.
//
// Copilot's hooks cannot describe this: a sub-agent's hook session is a
// synthetic call_<toolCallId> with nothing linking it to the parent
// conversation. The native-OTel file can, and does — the layer used to be
// collapsed and thrown away, which left the sub-agent's identity with nowhere
// standard to go.
type SubAgent struct {
	SpanID       string // native span id, reused verbatim
	ParentSpanID string // the execute_tool span that spawned it
	// AgentType is the native gen_ai.agent.name, e.g. "task". It names the kind
	// of agent, not this invocation — two concurrent sub-agents of one kind share
	// it, exactly as Claude's subagent_type does.
	AgentType string
	// CallID is the spawning tool call's gen_ai.tool.call.id. It is the per
	// invocation identity. Under `copilot -p` it is also the sub-agent's own
	// hook session id (call_<toolCallId>), so it joins the two records there;
	// an interactive session names that hook session with a plain UUID instead,
	// and the join does not hold.
	CallID     string
	Start, End time.Time
	Failed     bool
}

// Turn is everything recovered from the native-OTel file for the turn that just
// ended: aggregated usage (nil if no chat span flushed yet), the turn's tool
// executions parent and sub-agent alike, and its sub-agent invocations.
type Turn struct {
	Usage  *Usage
	Tools  []ToolCall
	Agents []SubAgent
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

// cursorPrefix names the per-session file that persists the id of the last
// consumed native span (see cursor).
//
// The cursor lives beside the native-OTel files, NOT in the session directory
// the rest of the session's state uses. pipeline.Process deletes that directory
// on SessionEnd, and a Copilot session id outlives its session: `copilot
// --resume` comes back under the same id, so the cursor is precisely the state
// that must survive the end of a launch. It used to be wiped with everything
// else, and a resumed session then re-read the whole file from the start —
// measured on qa/runs/probe-two-turns, where turn 2's chat span carried 59068
// input tokens for a turn of 29655, having counted turn 1 a second time, and
// turn 1's tool span was emitted again under turn 2's trace.
//
// Only reachable when both launches write to one native-OTel file. The launch
// function the dash0-configure skill installs gives each launch its own file
// and deletes it at exit, so a fresh file made the stale cursor harmless; the
// per-session path documented as the alternative to that function does not.
const cursorPrefix = "cursor-"

// cursorPath is where this conversation's cursor lives.
func cursorPath(sessionID string) string {
	return filepath.Join(OtelDir(), cursorPrefix+sessionID+".json")
}

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
// otelDirName is the leaf of the convention path. It is a constant because
// ReservedSessionID has to keep a session id from naming it: in the default
// layout this directory is a child of the plugin's own data root.
const otelDirName = "otel"

func OtelDir() string {
	if v := os.Getenv("DASH0_COPILOT_OTEL_DIR"); v != "" {
		return v
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return filepath.Join(os.TempDir(), "dash0-agent-plugin", "copilot", otelDirName)
	}
	return filepath.Join(home, ".local", "state", "dash0-agent-plugin", "copilot", otelDirName)
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
// re-running the same Stop finds the cursor at the end → nothing new. The
// cursor is keyed by conversation and kept outside the session directory, so it
// survives the SessionEnd that ends a launch — see cursorPrefix.
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
func ReadTurn(sessionID string) (*Turn, string) {
	if sessionID == "" {
		return nil, ""
	}
	spans := newestConversationSpans(OtelDir(), sessionID)
	if len(spans) == 0 {
		return nil, ""
	}
	fresh := spansAfterCursor(spans, loadCursor(sessionID))
	if len(fresh) == 0 {
		return nil, ""
	}

	// Every span of this conversation by native id, built before the loop below
	// because identifying the turn's own agent needs it.
	byID := make(map[string]otelSpan, len(spans))
	for _, s := range spans {
		byID[s.spanID] = s
	}

	turn := &Turn{}
	// Every span this turn will emit, by native id. Both tools and sub-agents go
	// in, because parenting resolves to the nearest ancestor that is *itself*
	// emitted — that is what nests a sub-agent's tools under their agent, and the
	// agent under the tool that spawned it.
	emitted := make(map[string]bool)
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
			if m := attrString(a, "gen_ai.request.model"); m != "" {
				u.Model = m // last non-empty model in the turn
			}
			if txt := assistantTextFromOutput(attrString(a, "gen_ai.output.messages")); txt != "" {
				u.ResponseText = txt // last non-empty assistant text in the turn = the final response
			}
		case strings.HasPrefix(s.name, "invoke_agent"):
			// The turn's own agent roots the trace. It IS the turn, which the
			// pipeline's chat span already represents, so emitting it too would
			// duplicate the turn as a second span with the whole tool tree
			// beneath it. A sub-agent is the nested case.
			//
			// "Roots the trace" is not the same as "has no parent id". Copilot
			// already injects a traceparent into an interactive session's hook
			// payloads, so a turn whose trace continues one from outside would
			// carry a parent id here that names a span this file does not hold,
			// and a bare emptiness check would read that root as a sub-agent.
			//
			// So the test is the positive one: a sub-agent hangs under the
			// execute_tool call that spawned it. Asking only whether the parent
			// is some span of this conversation is looser than that, and it
			// admits a turn whose root continues an earlier turn's trace within
			// the same file — which would duplicate that turn under its own chat
			// span, with the whole tool tree beneath.
			if s.parentSpanID == "" {
				continue
			}
			parent, known := byID[s.parentSpanID]
			if !known || !strings.HasPrefix(parent.name, "execute_tool") {
				// Also the window where a sub-agent's spawning execute_tool has
				// not flushed yet: a child span is written before its parent, so
				// the agent reaches the file first and a read landing between the
				// two sees a parent it cannot resolve. Indistinguishable from the
				// case above, so the agent span is dropped rather than guessed
				// at. Its tools still arrive, parented on the turn's chat span.
				// Rare, one-sided, and it costs a span rather than a wrong tree.
				continue
			}
			emitted[s.spanID] = true
			turn.Agents = append(turn.Agents, SubAgent{
				SpanID:    s.spanID,
				AgentType: attrString(s.attrs, "gen_ai.agent.name"),
				Start:     s.start,
				End:       s.end,
				Failed:    s.failed,
			})
		case strings.HasPrefix(s.name, "execute_tool"):
			emitted[s.spanID] = true
			turn.Tools = append(turn.Tools, ToolCall{
				SpanID:    s.spanID,
				Name:      attrString(s.attrs, "gen_ai.tool.name"),
				Arguments: attrString(s.attrs, "gen_ai.tool.call.arguments"),
				SkillName: attrString(s.attrs, "github.copilot.tool.parameters.skill_name"),
				Result:    attrString(s.attrs, "gen_ai.tool.call.result"),
				CallID:    attrString(s.attrs, "gen_ai.tool.call.id"),
				Start:     s.start,
				End:       s.end,
				Failed:    s.failed,
			})
		}
	}

	// Collapse only the layers nothing is emitted for — the native chat spans,
	// and the turn's own root invoke_agent. Every span this turn emits parents to
	// the nearest ancestor that is also emitted, which reproduces the native tree
	// minus those layers:
	//
	//   chat → execute_tool task → invoke_agent task → execute_tool bash
	//
	// Ancestry walks the FULL span list (a parent record precedes only by id, not
	// necessarily by window), but the resolved parent must itself be emitted this
	// turn or the link would point at a span nobody sent.
	for i := range turn.Tools {
		turn.Tools[i].ParentSpanID = nearestEmittedAncestor(byID, emitted, turn.Tools[i].SpanID)
	}
	for i := range turn.Agents {
		parent := nearestEmittedAncestor(byID, emitted, turn.Agents[i].SpanID)
		turn.Agents[i].ParentSpanID = parent
		// The sub-agent's per-invocation identity is the call id of the tool that
		// spawned it. The native span's own gen_ai.agent.id is "builtin:<type>",
		// which every sub-agent of that kind shares, so it would be a type filter
		// wearing an id's name.
		if p, ok := byID[parent]; ok {
			turn.Agents[i].CallID = attrString(p.attrs, "gen_ai.tool.call.id")
		}
	}

	return turn, fresh[len(fresh)-1].spanID
}

// nearestEmittedAncestor walks up the native parent chain from spanID and
// returns the first ancestor this turn is emitting, or "" if the chain exits the
// known tree first (a top-level tool, which the caller parents to the turn's
// chat span).
func nearestEmittedAncestor(byID map[string]otelSpan, emitted map[string]bool, spanID string) string {
	seen := map[string]bool{spanID: true}
	cur := byID[spanID].parentSpanID
	for cur != "" && !seen[cur] {
		seen[cur] = true
		s, ok := byID[cur]
		if !ok {
			return ""
		}
		if emitted[s.spanID] {
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

func loadCursor(sessionID string) string {
	data, err := os.ReadFile(cursorPath(sessionID))
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
func SaveCursor(sessionID, lastSpanID string) {
	data, err := json.Marshal(cursor{LastSpanID: lastSpanID})
	if err != nil {
		return
	}
	if err := os.MkdirAll(OtelDir(), 0o755); err != nil {
		fmt.Fprintf(os.Stderr, "copilot-on-event: persisting usage cursor: %v\n", err)
		return
	}
	if err := os.WriteFile(cursorPath(sessionID), data, 0o644); err != nil {
		fmt.Fprintf(os.Stderr, "copilot-on-event: persisting usage cursor: %v\n", err)
	}
}

// SweepOldOtelFiles removes native-OTel files (and now-empty dirs) under
// OtelDir() older than staleFileTTL — leftovers from processes that exited
// uncleanly so the launcher's `rm` never ran. Best-effort; called on SessionStart.
//
// Cursors are left alone, however old. "Stale" here means "untouched for a
// day", which a conversation idle over a weekend also is, and its cursor ages
// while a shared native-OTel file stays fresh under other sessions' writes.
// Deleting it sends the next ReadTurn back to the top of that file and
// double-counts every token and tool it already reported, which is the defect
// the cursor exists to prevent. They are 35-byte files; the marker beside them
// is exempt for the same reason, in session.go.
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
		switch {
		case e.IsDir():
			_ = os.Remove(p) // removes only if empty
		case strings.HasSuffix(e.Name(), ".jsonl"):
			_ = os.Remove(p)
		}
	}
}

// attrInt/attrString read a native-OTel flat attribute (JSON numbers decode as
// float64).
func attrInt(a map[string]any, key string) int64 {
	switch v := a[key].(type) {
	case float64:
		return int64(v)
	case int64:
		return v
	}
	return 0
}

func attrString(a map[string]any, key string) string {
	s, _ := a[key].(string)
	return s
}
