// SPDX-FileCopyrightText: Copyright 2026 Dash0 Inc.
// SPDX-License-Identifier: Apache-2.0

package opencodev2

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/dash0hq/dash0-agent-plugin/internal/otlp"
)

// Exercise the real HTTP exporter, not a mock implementation of the mapping.
func exporter(t *testing.T) (otlp.Config, chan otlp.Span) {
	t.Helper()
	spans := make(chan otlp.Span, 30)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/v1/traces", r.URL.Path)
		assert.Equal(t, "Bearer test-token", r.Header.Get("Authorization"))
		assert.Equal(t, "test-dataset", r.Header.Get("Dash0-Dataset"))
		var req otlp.ExportTracesRequest
		if !assert.NoError(t, json.NewDecoder(r.Body).Decode(&req)) {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		for _, resource := range req.ResourceSpans {
			assert.Equal(t, "opencode-v2", attr(resource.Resource.Attributes, "service.name"))
			for _, scope := range resource.ScopeSpans {
				for _, span := range scope.Spans {
					spans <- span
				}
			}
		}
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(server.Close)
	return otlp.Config{OTLPUrl: server.URL, AuthToken: "test-token", Dataset: "test-dataset", AgentName: "opencode-v2", HarnessName: "opencode-v2", TeamName: "darkplane", OmitUserInfo: true}, spans
}

func attr(attrs []otlp.Attribute, key string) string {
	for _, a := range attrs {
		if a.Key == key {
			if a.Value.StringValue != nil {
				return *a.Value.StringValue
			}
			if a.Value.IntValue != nil {
				return *a.Value.IntValue
			}
		}
	}
	return ""
}

// Every source event has its own durable sequence, starting at zero. Progress
// is ephemeral and deliberately doesn't move that cursor.
func feed(t *testing.T, p *Processor, sid, kind, fields string) Event {
	t.Helper()
	seq := int64(0)
	if s := p.Sessions[sid]; s != nil {
		seq = s.LastSeq + 1
	}
	if fields != "" {
		fields = "," + fields
	}
	var e Event
	require.NoError(t, json.Unmarshal([]byte(`{"type":"`+kind+`","created":1000,"location":{"directory":"/tmp/project"},"data":{"sessionID":"`+sid+`"`+fields+`}}`), &e))
	if kind != "session.tool.progress" {
		e.Durable = &struct {
			Seq int64 `json:"seq"`
		}{seq}
	}
	require.NoError(t, p.Handle(e))
	return e
}

func step(t *testing.T, p *Processor, sid, message string) {
	t.Helper()
	feed(t, p, sid, "session.step.started", `"assistantMessageID":"`+message+`","agent":"general","model":{"id":"custom-model","providerID":"test-provider"}`)
}

func TestUsageReloadAndTurnBoundaries(t *testing.T) {
	cfg, spans := exporter(t)
	dir := t.TempDir()
	p, err := Load(dir, cfg)
	require.NoError(t, err)
	feed(t, p, "parent", "session.created", "")
	feed(t, p, "parent", "session.skill.activated", `"name":"review"`)
	feed(t, p, "parent", "session.execution.started", "")
	step(t, p, "parent", "m1")
	e := feed(t, p, "parent", "session.step.ended", `"tokens":{"input":7,"output":11,"reasoning":3,"cache":{"read":5,"write":2}}`)
	require.NoError(t, p.Save())
	p, err = Load(dir, cfg)
	require.NoError(t, err)
	require.NoError(t, p.Handle(e)) // Replay must not double the counters.
	firstTrace := p.Sessions["parent"].Turn.TraceID
	feed(t, p, "parent", "session.execution.interrupted", `"reason":"shutdown"`)
	feed(t, p, "parent", "session.execution.started", "")
	assert.Equal(t, firstTrace, p.Sessions["parent"].Turn.TraceID)
	step(t, p, "parent", "m2")
	feed(t, p, "parent", "session.step.ended", `"tokens":{"input":13,"output":17,"reasoning":19,"cache":{"read":23,"write":29}}`)
	feed(t, p, "parent", "session.text.ended", `"assistantMessageID":"m2","text":"first "`)
	feed(t, p, "parent", "session.text.ended", `"assistantMessageID":"m2","text":"second"`)
	feed(t, p, "parent", "session.execution.succeeded", "")
	span := <-spans
	assert.Equal(t, "79", attr(span.Attributes, "gen_ai.usage.input_tokens"))
	assert.Equal(t, "50", attr(span.Attributes, "gen_ai.usage.output_tokens"))
	assert.Equal(t, "28", attr(span.Attributes, "gen_ai.usage.cache_read.input_tokens"))
	assert.Equal(t, "31", attr(span.Attributes, "gen_ai.usage.cache_creation.input_tokens"))
	assert.Equal(t, "22", attr(span.Attributes, "gen_ai.usage.reasoning.output_tokens"))
	assert.Contains(t, attr(span.Attributes, "gen_ai.output.messages"), "first second")
	assert.Equal(t, "review", attr(span.Attributes, "dash0.gen_ai.tool.skill.name"))
	assert.Equal(t, "test-provider", attr(span.Attributes, "gen_ai.provider.name"))
	feed(t, p, "parent", "session.execution.started", "")
	assert.NotEqual(t, firstTrace, p.Sessions["parent"].Turn.TraceID)
	assert.Empty(t, p.Sessions["parent"].Turn.Usage)
	feed(t, p, "parent", "session.execution.failed", `"error":{"type":"ApiError","message":"failed request","response":{"body":"PRIVATE_RAW_BODY"}}`)
	span = <-spans
	assert.Equal(t, otlp.StatusCodeError, span.Status.Code)
	assert.Equal(t, "failed request", span.Status.Message)
	payload, err := json.Marshal(span)
	require.NoError(t, err)
	assert.NotContains(t, string(payload), "PRIVATE_RAW_BODY")
	assert.Empty(t, attr(span.Attributes, "gen_ai.usage.input_tokens"))
	assert.Empty(t, attr(span.Attributes, "dash0.gen_ai.tool.skill.name"))
	assert.Empty(t, spans)
}

func TestRepeatedToolIDsAndSubagentJoin(t *testing.T) {
	cfg, spans := exporter(t)
	p, err := Load(t.TempDir(), cfg)
	require.NoError(t, err)
	feed(t, p, "parent", "session.execution.started", "")
	step(t, p, "parent", "m1")
	step(t, p, "parent", "m2")
	for _, m := range []string{"m1", "m2"} {
		feed(t, p, "parent", "session.tool.input.started", `"assistantMessageID":"`+m+`","id":"call0","name":"subagent"`)
		feed(t, p, "parent", "session.tool.called", `"assistantMessageID":"`+m+`","id":"call0","input":{"prompt":"child prompt"}`)
	}
	feed(t, p, "child", "session.created", `"parentID":"parent"`)
	assert.Equal(t, "parent", p.Sessions["child"].ParentID) // created seq 0
	feed(t, p, "parent", "session.tool.progress", `"assistantMessageID":"m2","id":"call0","metadata":{"sessionID":"child"}`)
	feed(t, p, "child", "session.execution.started", "")
	step(t, p, "child", "c1")
	feed(t, p, "child", "session.tool.input.started", `"assistantMessageID":"c1","id":"read0","name":"read"`)
	feed(t, p, "child", "session.tool.success", `"assistantMessageID":"c1","id":"read0","content":[{"type":"text","text":"ok"}]`)
	childTool := <-spans
	feed(t, p, "child", "session.step.ended", `"tokens":{"input":41,"output":43,"cache":{"read":0,"write":0}}`)
	feed(t, p, "child", "session.execution.succeeded", "")
	child := <-spans
	// A continued child has no creation event in this consumer's lifetime.
	feed(t, p, "parent", "session.tool.progress", `"assistantMessageID":"m1","id":"call0","metadata":{"sessionID":"continued"}`)
	feed(t, p, "continued", "session.execution.started", "")
	step(t, p, "continued", "c2")
	feed(t, p, "continued", "session.execution.succeeded", "")
	continued := <-spans
	for _, m := range []string{"m2", "m1"} { // completion order differs
		feed(t, p, "parent", "session.tool.success", `"assistantMessageID":"`+m+`","id":"call0","content":[{"type":"text","text":"done"}]`)
	}
	tool2, tool1 := <-spans, <-spans
	feed(t, p, "parent", "session.execution.succeeded", "")
	parent := <-spans
	assert.NotEqual(t, tool1.SpanID, tool2.SpanID)
	assert.Equal(t, tool2.SpanID, child.ParentSpanID)
	assert.Equal(t, parent.SpanID, tool1.ParentSpanID)
	assert.Equal(t, parent.TraceID, child.TraceID)
	assert.Equal(t, "invoke_agent general", child.Name)
	assert.Equal(t, "invoke_agent general", continued.Name)
	assert.Equal(t, tool1.SpanID, continued.ParentSpanID)
	assert.Equal(t, parent.TraceID, continued.TraceID)
	assert.Equal(t, child.SpanID, childTool.ParentSpanID)
	// The sub-agent's own tool calls name it, like its invoke_agent span does.
	assert.Equal(t, "general", attr(childTool.Attributes, "gen_ai.agent.name"))
	assert.Equal(t, "child", attr(childTool.Attributes, "gen_ai.agent.id"))
	assert.NotEqual(t, "general", attr(tool1.Attributes, "gen_ai.agent.name"))
	assert.Equal(t, "parent", attr(child.Attributes, "gen_ai.conversation.id"))
	assert.Equal(t, "41", attr(child.Attributes, "gen_ai.usage.input_tokens"))
	assert.Empty(t, attr(parent.Attributes, "gen_ai.usage.input_tokens"))
	assert.Empty(t, spans)
}

func TestLateSubagentLinkKeepsTheAgentName(t *testing.T) {
	cfg, spans := exporter(t)
	p, err := Load(t.TempDir(), cfg)
	require.NoError(t, err)
	feed(t, p, "parent", "session.execution.started", "")
	step(t, p, "parent", "m1")
	feed(t, p, "parent", "session.tool.input.started", `"assistantMessageID":"m1","id":"call0","name":"subagent"`)
	// The child's first step lands before the parent's progress links it.
	feed(t, p, "late", "session.execution.started", "")
	step(t, p, "late", "c1")
	feed(t, p, "parent", "session.tool.progress", `"assistantMessageID":"m1","id":"call0","metadata":{"sessionID":"late"}`)
	feed(t, p, "late", "session.execution.succeeded", "")
	child := <-spans
	assert.Equal(t, "invoke_agent general", child.Name)
	assert.Equal(t, "general", attr(child.Attributes, "gen_ai.agent.name"))
}

func TestPrivacyAndToolFailure(t *testing.T) {
	cfg, spans := exporter(t)
	cfg.OmitIO = true
	p, err := Load(t.TempDir(), cfg)
	require.NoError(t, err)
	feed(t, p, "parent", "session.execution.started", "")
	step(t, p, "parent", "m1")
	e := feed(t, p, "parent", "session.inbox.delivered", "")
	e.Durable = nil
	e.Prompt = &struct {
		Text string `json:"text"`
		Role string `json:"role"`
	}{"PRIVATE_PROMPT", "user"}
	require.NoError(t, p.Handle(e))
	feed(t, p, "parent", "session.text.ended", `"assistantMessageID":"m1","text":"PRIVATE_RESPONSE"`)
	feed(t, p, "parent", "session.tool.input.started", `"assistantMessageID":"m1","id":"call0","name":"shell"`)
	feed(t, p, "parent", "session.tool.called", `"assistantMessageID":"m1","id":"call0","input":{"command":"printf PRIVATE_COMMAND"}`)
	require.NoError(t, p.Save())
	state, err := os.ReadFile(p.path)
	require.NoError(t, err)
	assert.NotContains(t, string(state), "PRIVATE_")
	assert.NotContains(t, string(state), "test-token")
	feed(t, p, "parent", "session.tool.failed", `"assistantMessageID":"m1","id":"call0","error":{"type":"ToolError","message":"tool unavailable"},"content":[{"type":"text","text":"PRIVATE_RESULT"}]`)
	tool := <-spans
	assert.Equal(t, otlp.StatusCodeError, tool.Status.Code)
	assert.Equal(t, "ToolError", tool.Status.Message)
	assert.Equal(t, "<REDACTED>", attr(tool.Attributes, "gen_ai.tool.call.arguments"))
	assert.Equal(t, "<REDACTED>", attr(tool.Attributes, "gen_ai.tool.call.result"))
	assert.Equal(t, "printf", attr(tool.Attributes, "dash0.gen_ai.tool.bash.command_family"))
	feed(t, p, "parent", "session.execution.interrupted", `"reason":"user"`)
	chat := <-spans
	assert.Equal(t, otlp.StatusCodeError, chat.Status.Code)
	assert.JSONEq(t, `[{"parts":[{"content":"<REDACTED>","type":"text"}],"role":"user"}]`, attr(chat.Attributes, "gen_ai.input.messages"))
	assert.JSONEq(t, `[{"parts":[{"content":"<REDACTED>","type":"text"}],"role":"assistant"}]`, attr(chat.Attributes, "gen_ai.output.messages"))
	// The identity comes from the machine, and CI runners may have none.
	if name := attr(chat.Attributes, "user.name"); name != "" {
		assert.Regexp(t, "^[0-9a-f]{16}$", name)
	}
	assert.Empty(t, attr(chat.Attributes, "user.email"))
	assert.Equal(t, "opencode-v2", attr(chat.Attributes, "gen_ai.harness.name"))
	assert.Equal(t, "darkplane", attr(chat.Attributes, "dash0.team.name"))
}

func TestReloadTightensPrivacy(t *testing.T) {
	cfg, _ := exporter(t)
	dir := t.TempDir()
	p, err := Load(dir, cfg)
	require.NoError(t, err)
	feed(t, p, "parent", "session.execution.started", "")
	p.Sessions["parent"].Turn.Prompt = "PRIVATE_PROMPT"
	p.Sessions["parent"].Turn.Response = "PRIVATE_RESPONSE"
	p.Sessions["parent"].Prompt = "PRIVATE_PENDING_PROMPT" // delivered before its turn
	feed(t, p, "parent", "session.tool.input.started", `"assistantMessageID":"m1","id":"call0","name":"shell"`)
	feed(t, p, "parent", "session.tool.called", `"assistantMessageID":"m1","id":"call0","input":{"command":"printf PRIVATE_COMMAND"}`)
	require.NoError(t, p.Save())
	cfg.OmitIO = true
	p, err = Load(dir, cfg)
	require.NoError(t, err)
	// Persisted by Load itself, not left in plaintext until the next event.
	state, err := os.ReadFile(p.path)
	require.NoError(t, err)
	assert.NotContains(t, string(state), "PRIVATE_")
}

func TestPromptBeforeExecutionJoinsTheTurn(t *testing.T) {
	cfg, spans := exporter(t)
	p, err := Load(t.TempDir(), cfg)
	require.NoError(t, err)
	e := feed(t, p, "parent", "session.inbox.delivered", "")
	e.Durable = nil
	e.Prompt = &struct {
		Text string `json:"text"`
		Role string `json:"role"`
	}{"early prompt", "user"}
	require.NoError(t, p.Handle(e))
	feed(t, p, "parent", "session.execution.started", "")
	step(t, p, "parent", "m1")
	feed(t, p, "parent", "session.execution.succeeded", "")
	chat := <-spans
	assert.JSONEq(t, `[{"parts":[{"content":"early prompt","type":"text"}],"role":"user"}]`, attr(chat.Attributes, "gen_ai.input.messages"))
}

func TestErrorsRespectIOPrivacy(t *testing.T) {
	for _, omitIO := range []bool{true, false} {
		for _, terminal := range []string{"session.tool.failed", "session.execution.failed"} {
			t.Run(terminal+map[bool]string{true: "-private", false: "-io"}[omitIO], func(t *testing.T) {
				cfg, spans := exporter(t)
				cfg.OmitIO = omitIO
				p, err := Load(t.TempDir(), cfg)
				require.NoError(t, err)
				feed(t, p, "parent", "session.execution.started", "")
				step(t, p, "parent", "m1")
				feed(t, p, "parent", "session.tool.input.started", `"assistantMessageID":"m1","id":"call0","name":"remote_tool"`)
				feed(t, p, "parent", terminal, `"assistantMessageID":"m1","id":"call0","error":{"type":"ToolError","message":"Arguments provided: PRIVATE_SENTINEL","response":{"body":"PRIVATE_RAW_BODY"}}`)
				span := <-spans
				assert.Equal(t, otlp.StatusCodeError, span.Status.Code)
				payload, err := json.Marshal(span)
				require.NoError(t, err)
				assert.NotContains(t, string(payload), "PRIVATE_RAW_BODY")
				if omitIO {
					assert.NotContains(t, string(payload), "PRIVATE_SENTINEL")
					assert.Equal(t, "ToolError", span.Status.Message)
				} else {
					assert.Contains(t, string(payload), "PRIVATE_SENTINEL")
				}
			})
		}
	}
}

func TestArbitraryV2MetadataDoesNotInvalidateEvents(t *testing.T) {
	cfg, spans := exporter(t)
	p, err := Load(t.TempDir(), cfg)
	require.NoError(t, err)
	feed(t, p, "parent", "session.created", `"metadata":{"name":42,"sessionID":{"arbitrary":true}}`)
	feed(t, p, "parent", "session.execution.started", "")
	step(t, p, "parent", "m1")
	feed(t, p, "parent", "session.tool.input.started", `"assistantMessageID":"m1","id":"call0","name":"skill"`)
	feed(t, p, "parent", "session.tool.progress", `"assistantMessageID":"m1","id":"call0","metadata":{"sessionID":123}`)
	feed(t, p, "parent", "session.tool.success", `"assistantMessageID":"m1","id":"call0","metadata":{"name":{"arbitrary":true},"sessionID":123},"content":[{"type":"text","text":"result"}]`)
	span := <-spans
	assert.Equal(t, "execute_tool skill", span.Name)
	assert.Equal(t, "result", attr(span.Attributes, "gen_ai.tool.call.result"))
	assert.Empty(t, attr(span.Attributes, "dash0.gen_ai.tool.skill.name"))
	assert.Empty(t, p.Anchors)
	feed(t, p, "parent", "session.execution.succeeded", "")
	assert.Equal(t, "chat custom-model", (<-spans).Name)
}

func TestGuardsDeletionAndPromptJoin(t *testing.T) {
	cfg, _ := exporter(t)
	p, err := Load(t.TempDir(), cfg)
	require.NoError(t, err)
	for _, raw := range []string{
		`{"type":"session.created","created":1000,"data":{"sessionID":"../escape"}}`,
		`{"type":"session.created","created":0,"data":{"sessionID":"zero"}}`,
	} {
		var e Event
		require.NoError(t, json.Unmarshal([]byte(raw), &e))
		require.NoError(t, p.Handle(e))
	}
	assert.Empty(t, p.Sessions)

	feed(t, p, "parent", "session.execution.started", "")
	for _, prompt := range []string{`{"text":"first","role":"user"}`, `{"text":"second","role":"assistant"}`} {
		var e Event
		require.NoError(t, json.Unmarshal([]byte(`{"type":"session.inbox.delivered","created":1000,"prompt":`+prompt+`,"data":{"sessionID":"parent"}}`), &e))
		require.NoError(t, p.Handle(e))
	}
	assert.Equal(t, "first\nsecond", p.Sessions["parent"].Turn.Prompt)
	assert.Equal(t, "user", p.Sessions["parent"].Turn.PromptRole)

	p.Anchors["parent"] = anchor{TraceID: "trace"}
	feed(t, p, "parent", "session.deleted", "")
	assert.NotContains(t, p.Sessions, "parent")
	assert.NotContains(t, p.Anchors, "parent")
}

func TestLostTerminalEventStartsNewTurn(t *testing.T) {
	cfg, _ := exporter(t)
	p, err := Load(t.TempDir(), cfg)
	require.NoError(t, err)
	feed(t, p, "parent", "session.execution.started", "")
	first := p.Sessions["parent"].Turn.TraceID
	feed(t, p, "parent", "session.execution.started", "") // execution A's end was dropped
	assert.NotEqual(t, first, p.Sessions["parent"].Turn.TraceID)
}

func TestUnreadableStateDoesNotStopExporter(t *testing.T) {
	cfg, _ := exporter(t)
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(dir+"/opencode-v2.json", []byte("{not json"), 0o600))
	p, err := Load(dir, cfg)
	require.NoError(t, err)
	assert.Empty(t, p.Sessions)
	feed(t, p, "parent", "session.execution.started", "")
	assert.NotNil(t, p.Sessions["parent"].Turn)
}

func TestStateReadErrorDoesNotStopExporter(t *testing.T) {
	cfg, _ := exporter(t)
	dir := t.TempDir()
	require.NoError(t, os.Mkdir(dir+"/opencode-v2.json", 0o700)) // reading a directory fails
	p, err := Load(dir, cfg)
	require.NoError(t, err)
	assert.Empty(t, p.Sessions)
}

func TestSubagentLinkArrivingAfterChildStart(t *testing.T) {
	cfg, spans := exporter(t)
	p, err := Load(t.TempDir(), cfg)
	require.NoError(t, err)
	feed(t, p, "parent", "session.execution.started", "")
	step(t, p, "parent", "m1")
	feed(t, p, "parent", "session.tool.input.started", `"assistantMessageID":"m1","id":"call0","name":"subagent"`)
	feed(t, p, "child", "session.execution.started", "")
	feed(t, p, "parent", "session.tool.progress", `"assistantMessageID":"m1","id":"call0","metadata":{"sessionID":"child"}`)
	step(t, p, "child", "c1")
	feed(t, p, "child", "session.execution.succeeded", "")
	child := <-spans
	feed(t, p, "parent", "session.tool.success", `"assistantMessageID":"m1","id":"call0","content":[{"type":"text","text":"done"}]`)
	tool := <-spans
	assert.Equal(t, tool.TraceID, child.TraceID)
	assert.Equal(t, tool.SpanID, child.ParentSpanID)
	assert.Equal(t, "invoke_agent general", child.Name)
}

func TestSubagentLinkIgnoredAfterChildExported(t *testing.T) {
	cfg, spans := exporter(t)
	p, err := Load(t.TempDir(), cfg)
	require.NoError(t, err)
	feed(t, p, "child", "session.execution.started", "")
	step(t, p, "child", "c1")
	feed(t, p, "child", "session.tool.input.started", `"assistantMessageID":"c1","id":"t1","name":"read"`)
	feed(t, p, "child", "session.tool.success", `"assistantMessageID":"c1","id":"t1","content":[{"type":"text","text":"ok"}]`)
	tool := <-spans
	p.Anchors["child"] = anchor{TraceID: "late-trace", SpanID: "late-span", ConversationID: "parent"}
	feed(t, p, "child", "session.execution.succeeded", "")
	chat := <-spans
	assert.Equal(t, tool.TraceID, chat.TraceID) // the child turn stays in one trace
	assert.Empty(t, chat.ParentSpanID)
}

func TestIdleSessionsArePrunedOnSessionCreated(t *testing.T) {
	cfg, _ := exporter(t)
	p, err := Load(t.TempDir(), cfg)
	require.NoError(t, err)
	feed(t, p, "old", "session.created", "") // created at 1000 ms
	p.Anchors["old-child"] = anchor{Created: 1000}
	day := staleAfter.Milliseconds()
	var e Event
	require.NoError(t, json.Unmarshal([]byte(`{"type":"session.created","created":`+strconv.FormatInt(1000+day+1, 10)+`,"data":{"sessionID":"new"}}`), &e))
	require.NoError(t, p.Handle(e))
	assert.NotContains(t, p.Sessions, "old")
	assert.NotContains(t, p.Anchors, "old-child")
	assert.Contains(t, p.Sessions, "new")
}

func TestSweepInstancesOnlyRemovesStaleOpenCodeState(t *testing.T) {
	base := t.TempDir()
	now := time.Now()
	old := now.Add(-staleAfter - time.Hour)
	for _, name := range []string{"stale", "fresh", "self"} {
		require.NoError(t, os.MkdirAll(filepath.Join(base, name), 0o700))
		require.NoError(t, os.WriteFile(filepath.Join(base, name, stateFile), []byte("{}"), 0o600))
	}
	require.NoError(t, os.Chtimes(filepath.Join(base, "stale", stateFile), old, old))
	require.NoError(t, os.Chtimes(filepath.Join(base, "self", stateFile), old, old))
	require.NoError(t, os.MkdirAll(filepath.Join(base, "other-agent-session"), 0o700)) // no OpenCode state
	require.NoError(t, os.Chtimes(filepath.Join(base, "other-agent-session"), old, old))
	SweepInstances(base, "self", now)
	for name, kept := range map[string]bool{"stale": false, "fresh": true, "self": true, "other-agent-session": true} {
		_, err := os.Stat(filepath.Join(base, name))
		assert.Equal(t, kept, err == nil, name)
	}
}

func TestSaveRecreatesSweptInstanceDirectory(t *testing.T) {
	cfg, _ := exporter(t)
	dir := filepath.Join(t.TempDir(), "instance")
	p, err := Load(dir, cfg)
	require.NoError(t, err)
	require.NoError(t, os.RemoveAll(dir))
	require.NoError(t, p.Save())
	assert.FileExists(t, filepath.Join(dir, stateFile))
}

// Replays the stream the plugin forwarded from a real `opencode run` (two shell
// calls, then a subagent that reads a file), so the mapping is checked against
// what V2 actually emits, not only against hand-written events.
func TestRecordedTurnWithSubagent(t *testing.T) {
	cfg, spans := exporter(t)
	cfg.OmitIO = false
	p, err := Load(t.TempDir(), cfg)
	require.NoError(t, err)
	data, err := os.ReadFile(filepath.Join("testdata", "turn_with_subagent.jsonl"))
	require.NoError(t, err)
	type usage struct{ input, output int64 }
	want := map[string]*usage{}
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		var e Event
		require.NoError(t, json.Unmarshal([]byte(line), &e))
		require.NoError(t, p.Handle(e))
		var raw struct {
			Data struct {
				SessionID string `json:"sessionID"`
				Tokens    *struct {
					Input, Output, Reasoning int64
					Cache                    struct{ Read, Write int64 }
				} `json:"tokens"`
			} `json:"data"`
		}
		require.NoError(t, json.Unmarshal([]byte(line), &raw))
		if tok := raw.Data.Tokens; tok != nil {
			if want[raw.Data.SessionID] == nil {
				want[raw.Data.SessionID] = &usage{}
			}
			want[raw.Data.SessionID].input += tok.Input + tok.Cache.Read + tok.Cache.Write
			want[raw.Data.SessionID].output += tok.Output + tok.Reasoning
		}
	}
	byName := map[string][]otlp.Span{}
	for len(spans) > 0 {
		s := <-spans
		byName[s.Name] = append(byName[s.Name], s)
	}
	require.Len(t, byName["execute_tool shell"], 2)
	require.Len(t, byName["execute_tool subagent"], 1)
	require.Len(t, byName["execute_tool read"], 1)
	require.Len(t, byName["invoke_agent general"], 1)
	var chat otlp.Span
	for name, s := range byName {
		if strings.HasPrefix(name, "chat ") {
			require.Len(t, s, 1)
			chat = s[0]
		}
	}
	require.NotEmpty(t, chat.SpanID, "no chat span among %v", byName)
	child, subagent, read := byName["invoke_agent general"][0], byName["execute_tool subagent"][0], byName["execute_tool read"][0]
	assert.Empty(t, chat.ParentSpanID)
	for _, s := range append(byName["execute_tool shell"], subagent) {
		assert.Equal(t, chat.SpanID, s.ParentSpanID)
	}
	assert.Equal(t, subagent.SpanID, child.ParentSpanID)
	assert.Equal(t, child.SpanID, read.ParentSpanID)
	for _, s := range []otlp.Span{child, subagent, read} {
		assert.Equal(t, chat.TraceID, s.TraceID)
	}
	parentID := attr(chat.Attributes, "gen_ai.conversation.id")
	childID := attr(read.Attributes, "gen_ai.agent.id")
	require.NotNil(t, want[parentID])
	require.NotNil(t, want[childID])
	assert.Equal(t, strconv.FormatInt(want[parentID].input, 10), attr(chat.Attributes, "gen_ai.usage.input_tokens"))
	assert.Equal(t, strconv.FormatInt(want[parentID].output, 10), attr(chat.Attributes, "gen_ai.usage.output_tokens"))
	assert.Equal(t, strconv.FormatInt(want[childID].input, 10), attr(child.Attributes, "gen_ai.usage.input_tokens"))
	assert.Equal(t, strconv.FormatInt(want[childID].output, 10), attr(child.Attributes, "gen_ai.usage.output_tokens"))
	assert.Equal(t, "general", attr(read.Attributes, "gen_ai.agent.name"))
	assert.Contains(t, attr(chat.Attributes, "gen_ai.input.messages"), "run the shell command 'ls'")
	assert.Contains(t, attr(chat.Attributes, "gen_ai.output.messages"), "first heading is Fixture")
}

// A failed step was still billed, so its usage counts toward the turn.
func TestFailedStepUsageCounts(t *testing.T) {
	cfg, spans := exporter(t)
	p, err := Load(t.TempDir(), cfg)
	require.NoError(t, err)
	feed(t, p, "parent", "session.execution.started", "")
	step(t, p, "parent", "m1")
	feed(t, p, "parent", "session.step.failed", `"tokens":{"input":2,"output":3,"reasoning":5,"cache":{"read":7,"write":11}},"error":{"type":"ApiError","message":"overloaded"}`)
	step(t, p, "parent", "m2")
	feed(t, p, "parent", "session.step.ended", `"tokens":{"input":13,"output":17,"reasoning":19,"cache":{"read":23,"write":29}}`)
	feed(t, p, "parent", "session.execution.succeeded", "")
	span := <-spans
	assert.Equal(t, "85", attr(span.Attributes, "gen_ai.usage.input_tokens"))
	assert.Equal(t, "44", attr(span.Attributes, "gen_ai.usage.output_tokens"))
	assert.Equal(t, "30", attr(span.Attributes, "gen_ai.usage.cache_read.input_tokens"))
	assert.Equal(t, "40", attr(span.Attributes, "gen_ai.usage.cache_creation.input_tokens"))
	assert.Equal(t, "24", attr(span.Attributes, "gen_ai.usage.reasoning.output_tokens"))
}
