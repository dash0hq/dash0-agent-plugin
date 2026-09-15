// SPDX-FileCopyrightText: Copyright 2026 Dash0 Inc.
// SPDX-License-Identifier: Apache-2.0

package amp

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/dash0hq/dash0-agent-plugin/internal/otlp"
)

func TestCompletedTurnHasChatRootAndTimedTools(t *testing.T) {
	start := time.Date(2026, 9, 14, 12, 0, 0, 0, time.UTC)
	turn := Turn{ThreadID: "T-test", ID: json.RawMessage(`"M-prompt"`), Start: start, End: start.Add(10 * time.Second), Status: "done", Executor: "remote", Tools: []Tool{{ID: "call-1", Name: "shell_command", Start: start.Add(time.Second), End: start.Add(3 * time.Second), Status: "error"}}}
	req, err := BuildTrace(turn, nil, "disabled", otlp.Config{AgentName: "amp", HarnessName: "amp", OmitIO: true, OmitUserInfo: true, OmitIdentityFallback: true})
	require.NoError(t, err)
	spans := req.ResourceSpans[0].ScopeSpans[0].Spans
	require.Len(t, spans, 2)
	// With usage off the root still has to be the turn's chat span, so Dash0
	// counts it as a turn of this session rather than a sub-agent invocation.
	require.Equal(t, "chat", spans[0].Name)
	require.Equal(t, spans[0].TraceID, spans[1].TraceID)
	require.Equal(t, spans[0].SpanID, spans[1].ParentSpanID)
	require.Equal(t, "execute_tool shell_command", spans[1].Name)
	require.Equal(t, otlp.StatusCodeError, spans[1].Status.Code)
	require.Equal(t, start.Add(time.Second).UnixNano(), mustNano(t, spans[1].StartTimeUnixNano))
	encoded, err := json.Marshal(req)
	require.NoError(t, err)
	require.NotContains(t, string(encoded), "gen_ai.request.model")
	require.NotContains(t, string(encoded), "gen_ai.usage.")
}

func mustNano(t *testing.T, s string) int64 {
	t.Helper()
	var n int64
	require.NoError(t, json.Unmarshal([]byte(s), &n))
	return n
}

func TestMCPToolUsesSharedNameAndServerConventions(t *testing.T) {
	for _, omit := range []bool{false, true} {
		for _, tc := range []struct{ name, tool, server string }{
			{"mcp__github__search", "search", "github"},
			{"mcp__12345678-1234-1234-1234-123456789abc__search", "search", ""},
			{"shell_command", "shell_command", ""},
			{"mcp__", "mcp__", ""},
		} {
			start := time.Now()
			turn := Turn{ThreadID: "T-test", ID: json.RawMessage(`1`), Start: start, End: start.Add(time.Second), Status: "done", Executor: "local",
				Tools: []Tool{{ID: "call", Name: tc.name, Start: start, End: start.Add(time.Second), Status: "done"}}}
			req, err := BuildTrace(turn, nil, "disabled", otlp.Config{AgentName: "amp", HarnessName: "amp", OmitIO: omit})
			require.NoError(t, err)
			span := req.ResourceSpans[0].ScopeSpans[0].Spans[1]
			require.Equal(t, "execute_tool "+tc.tool, span.Name)
			attrs := map[string]otlp.AttrValue{}
			for _, attr := range span.Attributes {
				attrs[attr.Key] = attr.Value
			}
			require.Equal(t, otlp.StringVal(tc.tool), attrs["gen_ai.tool.name"])
			require.Equal(t, otlp.StringVal("call"), attrs["gen_ai.tool.call.id"])
			if tc.server == "" {
				require.NotContains(t, attrs, "dash0.gen_ai.tool.mcp_server")
			} else {
				require.Equal(t, otlp.StringVal(tc.server), attrs["dash0.gen_ai.tool.mcp_server"])
			}
		}
	}
}

func TestWorkingDirectoryUsesSharedPrivacyRules(t *testing.T) {
	home := t.TempDir()
	workspace := filepath.Join(home, "workspace")
	require.NoError(t, os.Mkdir(workspace, 0700))
	t.Chdir(workspace)
	cwd, err := os.Getwd()
	require.NoError(t, err)
	t.Setenv("HOME", filepath.Dir(cwd))
	t.Setenv("USERPROFILE", filepath.Dir(cwd))
	start := time.Now()
	turn := Turn{
		ThreadID: "T-test", ID: json.RawMessage(`1`), Start: start,
		End: start.Add(time.Second), Status: "done", Executor: "local",
		Tools: []Tool{{ID: "call", Name: "shell_command", Start: start, End: start.Add(time.Second), Status: "done"}},
	}
	for _, omit := range []bool{false, true} {
		req, err := BuildTrace(turn, []Usage{{Model: "gpt-6-astra", Attributes: map[string]any{"gen_ai.usage.output_tokens": int64(3)}}}, "matched", otlp.Config{AgentName: "amp", OmitUserInfo: omit, OmitIdentityFallback: true})
		require.NoError(t, err)
		want := cwd
		if omit {
			want = "~" + string(filepath.Separator) + "workspace"
		}
		spans := req.ResourceSpans[0].ScopeSpans[0].Spans
		// One model call, so it lands on the turn root: chat span plus its tool.
		require.Len(t, spans, 2)
		for _, span := range spans {
			attrs := map[string]otlp.AttrValue{}
			for _, attr := range span.Attributes {
				attrs[attr.Key] = attr.Value
			}
			require.Equal(t, otlp.StringVal(want), attrs["process.working_directory"])
		}
	}
}

func TestTurnAttributesAndUsageAreAttributedOncePerModel(t *testing.T) {
	workspace := t.TempDir()
	t.Chdir(workspace)
	t.Setenv("HOME", workspace)
	t.Setenv("XDG_CONFIG_HOME", workspace)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	for _, args := range [][]string{
		{"init", "-b", "main"},
		{"config", "user.name", "Test Developer"},
		{"config", "user.email", "developer@example.invalid"},
		{"remote", "add", "origin", "https://github.com/example/project.git"},
		{"-c", "commit.gpgsign=false", "commit", "--allow-empty", "-m", "fixture"},
	} {
		out, err := exec.Command("git", args...).CombinedOutput()
		require.NoError(t, err, string(out))
	}
	data := []byte(`{"id":"T-test","messages":[
		{"role":"user","messageId":0,"content":[{"type":"image","source":{"type":"url","url":"https://example.invalid/PRIVATE-IMAGE"}}]},
		{"role":"assistant","messageId":1,"protocolMessageID":"M-one","state":{"type":"complete"},"usage":{"model":"gpt-6-astra","totalInputTokens":11,"inputTokens":2,"cacheReadInputTokens":6,"cacheCreationInputTokens":3,"outputTokens":2}},
		{"role":"assistant","messageId":2,"state":{"type":"complete"},"usage":{"model":"claude-sonnet-4-6","totalInputTokens":31,"inputTokens":7,"cacheReadInputTokens":13,"cacheCreationInputTokens":11,"outputTokens":17}},
		{"role":"assistant","messageId":3,"state":{"type":"complete"},"content":[{"type":"image","source":{"type":"base64","data":"PRIVATE-BASE64"}}],"usage":{"model":"gpt-6-astra","totalInputTokens":7,"outputTokens":5}},
		{"role":"assistant","messageId":4,"state":{"type":"complete"},"usage":{"model":"wrong-turn","totalInputTokens":999,"outputTokens":999}}
	]}`)
	ids := []json.RawMessage{json.RawMessage(`1`), json.RawMessage(`"M-one"`), json.RawMessage(`2`), json.RawMessage(`3`), json.RawMessage(`3`)}
	usage, status := ReadUsage(data, "T-test", ids)
	require.Equal(t, "matched", status)
	start := time.Now()
	turn := Turn{ThreadID: "T-test", ID: json.RawMessage(`0`), Start: start, End: start.Add(time.Second), Status: "done", Executor: "remote",
		Tools: []Tool{{ID: "call", Name: "shell_command", Start: start, End: start.Add(time.Second), Status: "done"}}}
	req, err := BuildTrace(turn, usage, status, otlp.Config{AgentName: "amp", HarnessName: "amp", TeamName: "platform", OmitIO: false})
	require.NoError(t, err)
	spans := req.ResourceSpans[0].ScopeSpans[0].Spans
	require.Len(t, spans, 4)
	// Index 0 is the turn root, which the answering message (the last selected
	// record) names and supplies usage for; index 1 is the tool; the rest are
	// the turn's earlier model calls, still one span each.
	type call struct {
		model, provider       string
		input, output         int64
		cacheRead, cacheWrite int64
		hasCache              bool
	}
	calls := map[int]call{
		0: {"gpt-6-astra", "openai", 7, 5, 0, 0, false},
		2: {"gpt-6-astra", "openai", 11, 2, 6, 3, true},
		3: {"claude-sonnet-4-6", "anthropic", 31, 17, 13, 11, true},
	}
	spanIDs := map[string]bool{}
	for i, span := range spans {
		require.False(t, spanIDs[span.SpanID], "duplicate span ID")
		spanIDs[span.SpanID] = true
		attrs := map[string]otlp.AttrValue{}
		for _, attr := range span.Attributes {
			attrs[attr.Key] = attr.Value
		}
		for key, value := range map[string]string{
			"gen_ai.agent.name": "amp", "gen_ai.harness.name": "amp", "gen_ai.conversation.id": "T-test",
			"dash0.team.name": "platform", "user.name": "Test Developer", "user.email": "developer@example.invalid",
			"dash0.gen_ai.user.identity.source": "git", "dash0.gen_ai.vcs.repository.url.full": "https://github.com/example/project",
			"dash0.gen_ai.vcs.repository.name": "project", "dash0.gen_ai.vcs.owner.name": "example",
			"dash0.gen_ai.vcs.provider.name": "github", "dash0.gen_ai.vcs.ref.head.name": "main", "dash0.gen_ai.vcs.ref.head.type": "branch",
			"dash0.amp.turn.id": "n:0", "dash0.amp.executor.kind": "remote",
		} {
			require.Equal(t, otlp.StringVal(value), attrs[key], "%s on %s", key, span.Name)
		}
		require.Contains(t, attrs, "process.working_directory")
		require.Contains(t, attrs, "dash0.gen_ai.vcs.ref.head.revision")
		require.Equal(t, spans[0].TraceID, span.TraceID)
		if i > 0 {
			require.Equal(t, spans[0].SpanID, span.ParentSpanID)
		}
		want, isCall := calls[i]
		if !isCall {
			require.NotContains(t, attrs, "gen_ai.usage.input_tokens")
			require.NotContains(t, attrs, "gen_ai.usage.output_tokens")
			require.NotContains(t, attrs, "gen_ai.usage.cache_read.input_tokens")
			require.NotContains(t, attrs, "gen_ai.usage.cache_creation.input_tokens")
			require.NotContains(t, attrs, "gen_ai.request.model")
			continue
		}
		require.Equal(t, otlp.StringVal("chat"), attrs["gen_ai.operation.name"])
		require.Equal(t, otlp.StringVal(want.model), attrs["gen_ai.request.model"])
		require.Equal(t, otlp.StringVal(want.provider), attrs["gen_ai.provider.name"])
		require.Equal(t, otlp.IntVal(want.input), attrs["gen_ai.usage.input_tokens"])
		require.Equal(t, otlp.IntVal(want.output), attrs["gen_ai.usage.output_tokens"])
		if want.hasCache {
			require.Equal(t, otlp.IntVal(want.cacheRead), attrs["gen_ai.usage.cache_read.input_tokens"])
			require.Equal(t, otlp.IntVal(want.cacheWrite), attrs["gen_ai.usage.cache_creation.input_tokens"])
		} else {
			require.NotContains(t, attrs, "gen_ai.usage.cache_read.input_tokens")
			require.NotContains(t, attrs, "gen_ai.usage.cache_creation.input_tokens")
		}
	}
	encoded, err := json.Marshal(req)
	require.NoError(t, err)
	require.NotContains(t, string(encoded), "PRIVATE-")
	require.NotContains(t, string(encoded), "wrong-turn")
}

func TestConversationFollowsSharedOmitIORules(t *testing.T) {
	start := time.Now()
	turn := Turn{
		ThreadID: "T-test", ID: json.RawMessage(`1`), Start: start, End: start.Add(time.Second),
		Status: "done", Executor: "local", Prompt: "the ask", Response: "the answer",
		Tools: []Tool{{ID: "call", Name: "shell_command", Start: start, End: start.Add(time.Second), Status: "done",
			Input: `{"command":"ls -la"}`, Output: "total 0"}},
	}
	for _, omit := range []bool{false, true} {
		req, err := BuildTrace(turn, nil, "disabled", otlp.Config{AgentName: "amp", HarnessName: "amp", OmitIO: omit})
		require.NoError(t, err)
		spans := req.ResourceSpans[0].ScopeSpans[0].Spans
		attrs := map[string]otlp.AttrValue{}
		for _, span := range spans {
			for _, attr := range span.Attributes {
				attrs[attr.Key] = attr.Value
			}
		}
		for _, key := range []string{"gen_ai.input.messages", "gen_ai.output.messages", "gen_ai.tool.call.arguments", "gen_ai.tool.call.result"} {
			require.Contains(t, attrs, key, "omit_io=%v", omit)
		}
		encoded, err := json.Marshal(req)
		require.NoError(t, err)
		// The shared exporter owns the decision, so the source keeps no second copy.
		for _, content := range []string{"the ask", "the answer", "ls -la", "total 0"} {
			if omit {
				require.NotContains(t, string(encoded), content)
			} else {
				require.Contains(t, string(encoded), content)
			}
		}
	}
}

func TestUsageSpanIDsDifferAcrossTurns(t *testing.T) {
	start := time.Now()
	usage := []Usage{{Model: "gpt-6-astra"}, {Model: "claude-sonnet-4-6"}}
	ids := make([]string, 0, 2)
	for _, id := range []json.RawMessage{json.RawMessage(`1`), json.RawMessage(`2`)} {
		turn := Turn{ThreadID: "T-test", ID: id, Start: start, End: start.Add(time.Second), Status: "done", Executor: "local"}
		req, err := BuildTrace(turn, usage, "matched", otlp.Config{AgentName: "amp", HarnessName: "amp", OmitIO: true})
		require.NoError(t, err)
		spans := req.ResourceSpans[0].ScopeSpans[0].Spans
		require.Len(t, spans, 2)
		ids = append(ids, spans[1].SpanID)
	}
	// "usage:0" alone is the same string in every turn, so a position-only key
	// made every trace reuse one span ID.
	require.NotEqual(t, ids[0], ids[1])
}
