// SPDX-FileCopyrightText: Copyright 2026 Dash0 Inc.
// SPDX-License-Identifier: Apache-2.0

package demo

import (
	"encoding/json"
	"fmt"
	"math/rand/v2"
	"strings"
	"time"

	"github.com/dash0hq/dash0-agent-plugin/internal/otlp"
)

// serviceVersions is the closed list of plugin versions a turn can report.
var serviceVersions = []string{"0.1.8", "0.1.9", "0.1.10"}

// toolKind enumerates the three tool-call variants a turn can contain.
type toolKind int

const (
	toolKindBash toolKind = iota
	toolKindMCP
	toolKindSkill
)

// turn holds the randomized selections for a single agent turn. Both the trace
// and the VCS metrics for the turn are built from this value, so they always
// share the same repository, branch, user, and session.
type turn struct {
	repo     repo
	user     user
	model    string
	effort   string
	msg      messagePair
	branch   string
	revision string
	session  string
}

// newTurn draws all randomized dimensions for one turn from the closed lists in
// data.go.
func newTurn() turn {
	return turn{
		repo:   pick(repos),
		user:   pick(users),
		model:  pick(models),
		effort: pick(effortLevels),
		msg:    pick(messagePairs),
		branch: fmt.Sprintf("ENG-%d-%s", rand.IntN(900)+100, pick(branchTitles)),
		// A fresh session per turn, used as the conversation identifier.
		revision: randomHex(40),
		session:  randomUUID(),
	}
}

// GenerateTurn builds a mock OTLP traces request representing exactly one agent
// turn: a root "chat" span with a single child tool span (a Bash command, an
// MCP tool call, or a Skill invocation, chosen at random). All randomized
// dimensions — repository, user, branch, model, token usage, and the
// prompt/response pair — are drawn from the closed lists in data.go.
//
// now is the wall-clock instant the turn ends; the span timestamps are derived
// from it so callers can produce deterministic output in tests.
func GenerateTurn(now time.Time) (otlp.ExportTracesRequest, error) {
	return newTurn().traces(now)
}

// traces builds the OTLP traces request for the turn.
func (t turn) traces(now time.Time) (otlp.ExportTracesRequest, error) {
	traceID, err := otlp.GenerateTraceID()
	if err != nil {
		return otlp.ExportTracesRequest{}, err
	}
	chatSpanID, err := otlp.GenerateSpanID()
	if err != nil {
		return otlp.ExportTracesRequest{}, err
	}
	toolSpanID, err := otlp.GenerateSpanID()
	if err != nil {
		return otlp.ExportTracesRequest{}, err
	}

	r := t.repo
	u := t.user
	handle := userHandle(u.Name)
	model := t.model
	effort := t.effort
	msg := t.msg
	branch := t.branch
	revision := t.revision
	sessionID := t.session

	workingDir := fmt.Sprintf("/Users/%s/dev/%s", handle, r.Name)

	// Agent identity, VCS, user, and team attributes are shared by every span
	// in the turn.
	sharedAttrs := []otlp.Attribute{
		{Key: "gen_ai.harness.name", Value: otlp.StringVal("claude-code")},
		{Key: "gen_ai.provider.name", Value: otlp.StringVal("anthropic")},
		{Key: "gen_ai.agent.name", Value: otlp.StringVal("claude")},
		{Key: "dash0.gen_ai.vcs.owner.name", Value: otlp.StringVal(r.Owner)},
		{Key: "dash0.gen_ai.vcs.provider.name", Value: otlp.StringVal("github")},
		{Key: "dash0.gen_ai.vcs.ref.head.name", Value: otlp.StringVal(branch)},
		{Key: "dash0.gen_ai.vcs.ref.head.revision", Value: otlp.StringVal(revision)},
		{Key: "dash0.gen_ai.vcs.ref.head.type", Value: otlp.StringVal("branch")},
		{Key: "dash0.gen_ai.vcs.repository.name", Value: otlp.StringVal(r.Name)},
		{Key: "dash0.gen_ai.vcs.repository.url.full", Value: otlp.StringVal(r.URLFull)},
		{Key: "user.name", Value: otlp.StringVal(u.Name)},
		{Key: "dash0.team.name", Value: otlp.StringVal(u.Team)},
	}
	effortVal := otlp.StringVal(fmt.Sprintf(`{"level":%q}`, effort))

	// Timeline: the chat turn spans up to ~3 minutes; the tool call happens
	// somewhere in the middle and lasts a few seconds.
	chatEnd := now
	chatStart := chatEnd.Add(-time.Duration(rand.IntN(150)+30) * time.Second)
	turnSpan := chatEnd.Sub(chatStart)
	toolStart := chatStart.Add(time.Duration(rand.Int64N(int64(turnSpan / 2))))
	toolEnd := toolStart.Add(time.Duration(rand.IntN(9)+1) * time.Second)

	// Root chat span.
	chatAttrs := []otlp.Attribute{
		{Key: "gen_ai.operation.name", Value: otlp.StringVal("chat")},
		{Key: "gen_ai.request.model", Value: otlp.StringVal(model)},
		{Key: "gen_ai.conversation.id", Value: otlp.StringVal(sessionID)},
		{Key: "gen_ai.input.messages", Value: otlp.StringVal(messageJSON("user", msg.Input))},
		{Key: "gen_ai.output.messages", Value: otlp.StringVal(messageJSON("assistant", msg.Output))},
		{Key: "gen_ai.usage.input_tokens", Value: otlp.IntVal(int64(rand.IntN(40000) + 2000))},
		{Key: "gen_ai.usage.output_tokens", Value: otlp.IntVal(int64(rand.IntN(60000) + 500))},
		{Key: "gen_ai.usage.cache_creation.input_tokens", Value: otlp.IntVal(int64(rand.IntN(400000)))},
		{Key: "gen_ai.usage.cache_read.input_tokens", Value: otlp.IntVal(int64(rand.IntN(9000000)))},
		{Key: "effort", Value: effortVal},
	}
	chatAttrs = append(chatAttrs, sharedAttrs...)
	chatSpan := otlp.Span{
		TraceID:           traceID,
		SpanID:            chatSpanID,
		ParentSpanID:      "",
		Name:              "chat " + model,
		Kind:              otlp.SpanKindInternal,
		StartTimeUnixNano: nano(chatStart),
		EndTimeUnixNano:   nano(chatEnd),
		Attributes:        chatAttrs,
		Events:            []otlp.SpanEvent{},
		Links:             []otlp.SpanLink{},
		Status:            otlp.SpanStatus{Code: otlp.StatusCodeUnset},
	}

	// Child tool span.
	toolAttrs := []otlp.Attribute{
		{Key: "gen_ai.operation.name", Value: otlp.StringVal("execute_tool")},
		{Key: "gen_ai.request.model", Value: otlp.StringVal(model)},
		{Key: "gen_ai.conversation.id", Value: otlp.StringVal(sessionID)},
		{Key: "gen_ai.tool.type", Value: otlp.StringVal("function")},
		{Key: "gen_ai.tool.call.id", Value: otlp.StringVal(randomToolCallID())},
		{Key: "effort", Value: effortVal},
	}
	toolName := appendToolKindAttrs(&toolAttrs)
	toolAttrs = append(toolAttrs, sharedAttrs...)
	toolSpan := otlp.Span{
		TraceID:           traceID,
		SpanID:            toolSpanID,
		ParentSpanID:      chatSpanID,
		Name:              "execute_tool " + toolName,
		Kind:              otlp.SpanKindInternal,
		StartTimeUnixNano: nano(toolStart),
		EndTimeUnixNano:   nano(toolEnd),
		Attributes:        toolAttrs,
		Events:            []otlp.SpanEvent{},
		Links:             []otlp.SpanLink{},
		Status:            otlp.SpanStatus{Code: otlp.StatusCodeUnset},
	}

	resourceAttrs := []otlp.Attribute{
		{Key: "service.name", Value: otlp.StringVal("claude-code")},
		{Key: "service.version", Value: otlp.StringVal(pick(serviceVersions))},
		{Key: "process.working_directory", Value: otlp.StringVal(workingDir)},
	}

	req := otlp.ExportTracesRequest{
		ResourceSpans: []otlp.ResourceSpans{{
			Resource: otlp.Resource{Attributes: resourceAttrs},
			ScopeSpans: []otlp.ScopeSpans{{
				Scope: otlp.Scope{Name: "dash0-agent-plugin", Version: "demo"},
				Spans: []otlp.Span{chatSpan, toolSpan},
			}},
		}},
	}
	return req, nil
}

// appendToolKindAttrs picks one of the three tool variants at random, appends
// the variant-specific attributes (and the tool call arguments/result), and
// returns the tool name used for the span name.
func appendToolKindAttrs(attrs *[]otlp.Attribute) string {
	switch toolKind(rand.IntN(3)) {
	case toolKindMCP:
		t := pick(mcpTools)
		*attrs = append(*attrs,
			otlp.Attribute{Key: "dash0.gen_ai.tool.mcp_server", Value: otlp.StringVal(t.Server)},
			otlp.Attribute{Key: "gen_ai.tool.name", Value: otlp.StringVal(t.ToolName)},
			otlp.Attribute{Key: "gen_ai.tool.call.arguments", Value: otlp.StringVal(t.Arguments)},
			otlp.Attribute{Key: "gen_ai.tool.call.result", Value: otlp.StringVal(t.Result)},
		)
		return t.ToolName
	case toolKindSkill:
		s := pick(skillCalls)
		*attrs = append(*attrs,
			otlp.Attribute{Key: "dash0.gen_ai.tool.skill.name", Value: otlp.StringVal(s.Name)},
			otlp.Attribute{Key: "gen_ai.tool.name", Value: otlp.StringVal("Skill")},
			otlp.Attribute{Key: "gen_ai.tool.call.arguments", Value: otlp.StringVal(s.Arguments)},
			otlp.Attribute{Key: "gen_ai.tool.call.result", Value: otlp.StringVal(s.Result)},
		)
		return "Skill"
	default: // toolKindBash
		c := pick(bashCommands)
		*attrs = append(*attrs,
			otlp.Attribute{Key: "dash0.gen_ai.tool.bash.command_family", Value: otlp.StringVal(c.Family)},
			otlp.Attribute{Key: "gen_ai.tool.name", Value: otlp.StringVal("Bash")},
			otlp.Attribute{Key: "gen_ai.tool.call.arguments", Value: otlp.StringVal(c.Arguments)},
			otlp.Attribute{Key: "gen_ai.tool.call.result", Value: otlp.StringVal(c.Result)},
		)
		return "Bash"
	}
}

// pick returns a uniformly random element of s. s must be non-empty.
func pick[T any](s []T) T {
	return s[rand.IntN(len(s))]
}

// userHandle derives a unix-style handle (e.g. "guymoses") from a display name.
func userHandle(name string) string {
	return strings.ToLower(strings.ReplaceAll(name, " ", ""))
}

// nano formats a time as a Unix-nanoseconds string, the OTLP wire format.
func nano(t time.Time) string {
	return fmt.Sprintf("%d", t.UnixNano())
}

// messageJSON renders a single-part text message in the gen_ai messages shape.
func messageJSON(role, content string) string {
	msg := []map[string]any{{
		"role":  role,
		"parts": []map[string]any{{"type": "text", "content": content}},
	}}
	b, err := json.Marshal(msg)
	if err != nil {
		return content
	}
	return string(b)
}

const hexChars = "0123456789abcdef"

// randomHex returns an n-character lowercase hex string.
func randomHex(n int) string {
	b := make([]byte, n)
	for i := range b {
		b[i] = hexChars[rand.IntN(16)]
	}
	return string(b)
}

const base62Chars = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789"

// randomToolCallID returns a mock Anthropic tool-call id (toolu_<24 base62>).
func randomToolCallID() string {
	b := make([]byte, 24)
	for i := range b {
		b[i] = base62Chars[rand.IntN(len(base62Chars))]
	}
	return "toolu_" + string(b)
}

// randomUUID returns a random RFC-4122 version-4 UUID string.
func randomUUID() string {
	var b [16]byte
	for i := range b {
		b[i] = byte(rand.IntN(256))
	}
	b[6] = (b[6] & 0x0f) | 0x40 // version 4
	b[8] = (b[8] & 0x3f) | 0x80 // variant 10
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}
