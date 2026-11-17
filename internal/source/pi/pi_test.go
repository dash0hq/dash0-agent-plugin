// SPDX-FileCopyrightText: Copyright 2026 Dash0 Inc.
// SPDX-License-Identifier: Apache-2.0

package pi

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/dash0hq/dash0-agent-plugin/internal/otlp"
)

var start = time.Date(2026, 11, 13, 12, 0, 0, 0, time.UTC)

func at(s int) time.Time { return start.Add(time.Duration(s) * time.Second) }
func ptr[T any](v T) *T  { return &v }

func cfg(omitIO bool) otlp.Config {
	return otlp.Config{AgentName: "pi", HarnessName: "pi", OmitIO: omitIO, OmitUserInfo: true, OmitIdentityFallback: true}
}

func attrs(span otlp.Span) map[string]string {
	out := map[string]string{}
	for _, a := range span.Attributes {
		switch {
		case a.Value.StringValue != nil:
			out[a.Key] = *a.Value.StringValue
		case a.Value.IntValue != nil:
			out[a.Key] = *a.Value.IntValue
		}
	}
	return out
}

func build(t *testing.T, turn Turn, omitIO bool) []otlp.Span {
	t.Helper()
	req, err := BuildTrace(turn, cfg(omitIO))
	require.NoError(t, err)
	return req.ResourceSpans[0].ScopeSpans[0].Spans
}

// One session can switch providers between model calls, so every call names
// its own provider and model, and no span takes them from the session.
func TestEveryModelCallCarriesItsOwnProviderAndTokens(t *testing.T) {
	turn := Turn{
		SessionID: "019a-session", Start: at(0), End: at(10), Status: "done",
		Calls: []Call{
			{Provider: "openai", Model: "gpt-5.5", Start: at(1), End: at(2), StopReason: "toolUse",
				Usage: Usage{Input: 100, Output: 20, CacheRead: 30, Reasoning: ptr[int64](5)}},
			{Provider: "anthropic", Model: "claude-sonnet-5", Start: at(4), End: at(9), StopReason: "stop", ThinkingLevel: "high",
				Usage: Usage{Input: 2, Output: 40, CacheRead: 300, CacheWrite: 50, CacheWrite1h: ptr[int64](50)}},
		},
		Tools: []Tool{{ID: "call_1", Name: "bash", Start: at(2), End: at(3), Call: ptr(0), Input: `{"command":"git status"}`}},
	}
	spans := build(t, turn, true)
	require.Len(t, spans, 3)

	root, earlier, tool := spans[0], spans[1], spans[2]
	require.Equal(t, "chat claude-sonnet-5", root.Name)
	require.Empty(t, root.ParentSpanID)
	require.Equal(t, "chat gpt-5.5", earlier.Name)
	require.Equal(t, "execute_tool bash", tool.Name)
	for _, child := range spans[1:] {
		require.Equal(t, root.TraceID, child.TraceID)
		require.Equal(t, root.SpanID, child.ParentSpanID)
	}

	r := attrs(root)
	require.Equal(t, "anthropic", r["gen_ai.provider.name"])
	require.Equal(t, "anthropic", r["dash0.pi.provider"])
	require.Equal(t, "pi", r["gen_ai.harness.name"])
	require.Equal(t, "019a-session", r["gen_ai.conversation.id"])
	require.Equal(t, "high", r["gen_ai.request.reasoning.level"])
	// Inclusive of cache, as the conventions and Dash0's cost processor expect.
	require.Equal(t, "352", r["gen_ai.usage.input_tokens"])
	require.Equal(t, "40", r["gen_ai.usage.output_tokens"])
	require.Equal(t, "300", r["gen_ai.usage.cache_read.input_tokens"])
	require.Equal(t, "50", r["gen_ai.usage.cache_creation.input_tokens"])
	require.Equal(t, "50", r["dash0.gen_ai.usage.cache_creation.ephemeral_1h.input_tokens"])
	require.Equal(t, start.UnixNano(), mustNano(t, root.StartTimeUnixNano))

	e := attrs(earlier)
	require.Equal(t, "openai", e["gen_ai.provider.name"])
	require.Equal(t, "gpt-5.5", e["gen_ai.request.model"])
	require.Equal(t, "130", e["gen_ai.usage.input_tokens"])
	require.Equal(t, "5", e["gen_ai.usage.reasoning.output_tokens"])
	require.Equal(t, at(1).UnixNano(), mustNano(t, earlier.StartTimeUnixNano))
	require.Equal(t, at(2).UnixNano(), mustNano(t, earlier.EndTimeUnixNano))

	// The tool names the model and provider of the call that requested it,
	// and carries no tokens of its own.
	tl := attrs(tool)
	require.Equal(t, "openai", tl["gen_ai.provider.name"])
	require.Equal(t, "gpt-5.5", tl["gen_ai.request.model"])
	require.Equal(t, "git", tl["dash0.gen_ai.tool.bash.command_family"])
	require.Equal(t, "<REDACTED>", tl["gen_ai.tool.call.arguments"])
	for k := range tl {
		require.NotContains(t, k, "gen_ai.usage.")
	}
}

// Claude Code reached through Pi's claude-code provider is Anthropic for
// pricing, and Pi has already made the input count cache-exclusive.
func TestClaudeCodeProviderIsAnthropic(t *testing.T) {
	turn := Turn{SessionID: "s1", Start: at(0), End: at(2), Status: "done",
		Calls: []Call{{Provider: "claude-code", Model: "claude-opus-5-5", Start: at(0), End: at(2), StopReason: "stop",
			Usage: Usage{Input: 2, Output: 97, CacheWrite: 18689}}}}
	r := attrs(build(t, turn, true)[0])
	require.Equal(t, "anthropic", r["gen_ai.provider.name"])
	require.Equal(t, "claude-code", r["dash0.pi.provider"])
	require.Equal(t, "18691", r["gen_ai.usage.input_tokens"])
}

// The mode says what a cost figure means, so it sits only beside tokens.
func TestBillingModeIsPerCallAndOnlyBesideTokens(t *testing.T) {
	turn := Turn{SessionID: "s1", Start: at(0), End: at(9), Status: "done",
		Calls: []Call{
			{Provider: "openai", Model: "gpt-5.5", Start: at(0), End: at(1), StopReason: "toolUse", BillingMode: "unknown", Usage: Usage{Input: 1, Output: 1}},
			{Provider: "openai", Model: "gpt-5.5", Start: at(1), End: at(2), StopReason: "aborted", BillingMode: "subscription"},
			{Provider: "claude-code", Model: "claude-sonnet-5", Start: at(2), End: at(9), StopReason: "stop", BillingMode: "subscription", Usage: Usage{Input: 1, Output: 1}},
		}}
	spans := build(t, turn, true)
	require.Equal(t, "subscription", attrs(spans[0])["dash0.gen_ai.billing_mode"])
	require.Equal(t, "unknown", attrs(spans[1])["dash0.gen_ai.billing_mode"])
	require.NotContains(t, attrs(spans[2]), "dash0.gen_ai.billing_mode")

}

// A cloud provider such as Azure or Bedrock serves the same model names as
// the vendor, but it bills differently. Pi's provider ID, not the model name,
// sets gen_ai.provider.name on every span that names the call.
func TestCloudProviderWinsOverTheModelName(t *testing.T) {
	turn := Turn{SessionID: "s1", Start: at(0), End: at(9), Status: "done",
		Calls: []Call{
			{Provider: "azure", Model: "gpt-5.5", Start: at(0), End: at(1), StopReason: "toolUse", Usage: Usage{Input: 1, Output: 1}},
			{Provider: "amazon-bedrock", Model: "claude-sonnet-5", Start: at(2), End: at(9), StopReason: "stop", Usage: Usage{Input: 1, Output: 1}},
		},
		Tools: []Tool{{ID: "call_sub", Name: "subagent", Start: at(1), End: at(2), Call: ptr(0),
			Subagents: []Subagent{{Agent: "scout", Model: "google-vertex/gemini-3-pro", Turns: 1, Usage: Usage{Input: 1, Output: 1}}}}}}
	spans := build(t, turn, true)
	require.Len(t, spans, 5)
	root, call, tool, agent, usage := spans[0], spans[1], spans[2], spans[3], spans[4]
	require.Equal(t, "aws.bedrock", attrs(root)["gen_ai.provider.name"])
	require.Equal(t, "azure.ai.openai", attrs(call)["gen_ai.provider.name"])
	require.Equal(t, "azure.ai.openai", attrs(tool)["gen_ai.provider.name"])
	require.Equal(t, "gcp.vertex_ai", attrs(agent)["gen_ai.provider.name"])
	require.Equal(t, "gcp.vertex_ai", attrs(usage)["gen_ai.provider.name"])
}

// A router such as OpenRouter or Copilot does not say who serves the model,
// so the vendor of a known model name wins over the router's raw ID.
func TestUnmappedProviderDoesNotWinOverAKnownModel(t *testing.T) {
	turn := Turn{SessionID: "s1", Start: at(0), End: at(9), Status: "done",
		Calls: []Call{
			{Provider: "github-copilot", Model: "gpt-5.5", Start: at(0), End: at(1), StopReason: "toolUse", Usage: Usage{Input: 1, Output: 1}},
			{Provider: "openrouter", Model: "claude-sonnet-5", Start: at(2), End: at(9), StopReason: "stop", Usage: Usage{Input: 1, Output: 1}},
		},
		Tools: []Tool{{ID: "call_sub", Name: "subagent", Start: at(1), End: at(2), Call: ptr(0),
			Subagents: []Subagent{{Agent: "scout", Model: "vercel-ai-gateway/gemini-3-pro", Turns: 1, Usage: Usage{Input: 1, Output: 1}}}}}}
	spans := build(t, turn, true)
	require.Len(t, spans, 5)
	require.Equal(t, "anthropic", attrs(spans[0])["gen_ai.provider.name"])
	require.Equal(t, "openrouter", attrs(spans[0])["dash0.pi.provider"])
	require.Equal(t, "openai", attrs(spans[1])["gen_ai.provider.name"])
	require.Equal(t, "openai", attrs(spans[2])["gen_ai.provider.name"])
	require.Equal(t, "gcp.gemini", attrs(spans[3])["gen_ai.provider.name"])
	require.Equal(t, "gcp.gemini", attrs(spans[4])["gen_ai.provider.name"])
}

func TestUnknownProviderIsExportedAsItIs(t *testing.T) {
	turn := Turn{SessionID: "s1", Start: at(0), End: at(2), Status: "done",
		Calls: []Call{{Provider: "my-proxy", Model: "local-llama", Start: at(0), End: at(2), StopReason: "stop", Usage: Usage{Input: 1, Output: 1}}}}
	require.Equal(t, "my-proxy", attrs(build(t, turn, true)[0])["gen_ai.provider.name"])
}

// The user stopped the turn. That is not an error, so an Esc press does not
// count toward the error rate. Attributes record the stop instead.
func TestAbortedTurnIsNotAnErrorAndKeepsItsTools(t *testing.T) {
	turn := Turn{SessionID: "s1", Start: at(0), End: at(5), Status: "aborted",
		Calls: []Call{
			{Provider: "anthropic", Model: "claude-sonnet-5", Start: at(0), End: at(1), StopReason: "aborted"},
			{Provider: "anthropic", Model: "claude-sonnet-5", Start: at(1), End: at(4), StopReason: "aborted"},
		},
		Tools: []Tool{{ID: "t1", Name: "bash", Start: at(1), End: at(4), Error: true, Call: ptr(1)}}}
	spans := build(t, turn, true)
	require.Len(t, spans, 3)
	root, call, tool := spans[0], spans[1], spans[2]
	require.Equal(t, otlp.StatusCodeUnset, root.Status.Code)
	require.Empty(t, root.Status.Message)
	require.Equal(t, "aborted", attrs(root)["dash0.pi.turn.status"])
	require.Equal(t, "aborted", attrs(root)["dash0.pi.call.stop_reason"])
	require.Equal(t, otlp.StatusCodeUnset, call.Status.Code)
	require.Equal(t, "aborted", attrs(call)["dash0.pi.call.stop_reason"])
	// A tool that the abort stopped still failed.
	require.Equal(t, otlp.StatusCodeError, tool.Status.Code)
	// An aborted call reported no usage; zeros would read as a measurement.
	for k := range attrs(root) {
		require.NotContains(t, k, "gen_ai.usage.")
	}
}

// A failed turn and a failed model call stay errors.
func TestFailedTurnAndCallAreErrors(t *testing.T) {
	turn := Turn{SessionID: "s1", Start: at(0), End: at(5), Status: "error",
		Calls: []Call{
			{Provider: "anthropic", Model: "claude-sonnet-5", Start: at(0), End: at(1), StopReason: "error"},
			{Provider: "anthropic", Model: "claude-sonnet-5", Start: at(1), End: at(4), StopReason: "error"},
		}}
	spans := build(t, turn, true)
	require.Len(t, spans, 2)
	require.Equal(t, otlp.StatusCodeError, spans[0].Status.Code)
	require.Equal(t, "Pi turn error", spans[0].Status.Message)
	require.Equal(t, "error", attrs(spans[0])["dash0.pi.turn.status"])
	require.Equal(t, otlp.StatusCodeError, spans[1].Status.Code)
	require.Equal(t, "Pi model call error", spans[1].Status.Message)
	require.NotContains(t, attrs(spans[1]), "dash0.pi.call.stop_reason")
}

// A completed turn names no status, so the attribute marks only the turns
// that did not complete.
func TestDoneTurnHasNoStatusAttribute(t *testing.T) {
	spans := build(t, Turn{SessionID: "s1", Start: at(0), End: at(1), Status: "done"}, true)
	require.Equal(t, otlp.StatusCodeUnset, spans[0].Status.Code)
	require.NotContains(t, attrs(spans[0]), "dash0.pi.turn.status")
}

func TestTurnWithoutModelCallHasAnUnnamedChatRoot(t *testing.T) {
	spans := build(t, Turn{SessionID: "s1", Start: at(0), End: at(1), Status: "aborted"}, true)
	require.Len(t, spans, 1)
	require.Equal(t, "chat", spans[0].Name)
	require.NotContains(t, attrs(spans[0]), "gen_ai.provider.name")
}

func TestNestedToolParentsToTheToolThatCalledIt(t *testing.T) {
	turn := Turn{SessionID: "s1", Start: at(0), End: at(9), Status: "done",
		Calls: []Call{{Provider: "anthropic", Model: "claude-sonnet-5", Start: at(0), End: at(1), StopReason: "toolUse"}},
		Tools: []Tool{
			// Pi emits tool_execution_end in completion order, so the child
			// arrives first.
			{ID: "code/1", ParentID: "code", Name: "read", Start: at(2), End: at(3), Call: ptr(0)},
			{ID: "code", Name: "codemode", Start: at(1), End: at(4), Call: ptr(0)},
			{ID: "orphan", ParentID: "missing", Name: "read", Start: at(2), End: at(3)},
		}}
	spans := build(t, turn, true)
	require.Len(t, spans, 4)
	byName := map[string]otlp.Span{}
	for _, s := range spans[1:] {
		byName[attrs(s)["gen_ai.tool.call.id"]] = s
	}
	require.Equal(t, byName["code"].SpanID, byName["code/1"].ParentSpanID)
	require.Equal(t, spans[0].SpanID, byName["code"].ParentSpanID)
	require.Equal(t, spans[0].SpanID, byName["orphan"].ParentSpanID)
}

// Bridge data can name a loop of parents. No span may be its own ancestor,
// so each tool on the loop goes under the root, and a tool below the loop
// keeps its parent.
func TestToolParentCycleFallsBackToTheRoot(t *testing.T) {
	turn := Turn{SessionID: "s1", Start: at(0), End: at(9), Status: "done",
		Tools: []Tool{
			{ID: "self", ParentID: "self", Name: "read", Start: at(1), End: at(2)},
			{ID: "a", ParentID: "b", Name: "read", Start: at(1), End: at(2)},
			{ID: "b", ParentID: "c", Name: "read", Start: at(1), End: at(2)},
			{ID: "c", ParentID: "a", Name: "read", Start: at(1), End: at(2)},
			{ID: "leaf", ParentID: "a", Name: "read", Start: at(1), End: at(2)},
		}}
	spans := build(t, turn, true)
	require.Len(t, spans, 6)
	byName := map[string]otlp.Span{}
	for _, s := range spans[1:] {
		byName[attrs(s)["gen_ai.tool.call.id"]] = s
	}
	for _, id := range []string{"self", "a", "b", "c"} {
		require.Equal(t, spans[0].SpanID, byName[id].ParentSpanID, id)
	}
	require.Equal(t, byName["a"].SpanID, byName["leaf"].ParentSpanID)
}

// An optional count that contradicts its total is dropped alone. The call
// keeps its span and its other tokens, and so stays the turn's last call.
func TestInconsistentOptionalCountIsDroppedAlone(t *testing.T) {
	turn := Turn{SessionID: "s1", Start: at(0), End: at(9), Status: "done",
		Calls: []Call{
			{Provider: "openai", Model: "gpt-5.5", Start: at(0), End: at(1), StopReason: "toolUse", Usage: Usage{Input: 1, Output: 1}},
			{Provider: "anthropic", Model: "claude-sonnet-5", Start: at(2), End: at(9), StopReason: "stop",
				Usage: Usage{Input: 10, Output: 5, CacheWrite: 3, CacheWrite1h: ptr[int64](4), Reasoning: ptr[int64](6)}},
		}}
	spans := build(t, turn, true)
	require.Len(t, spans, 2)
	r := attrs(spans[0])
	require.Equal(t, "chat claude-sonnet-5", spans[0].Name)
	require.Equal(t, "anthropic", r["gen_ai.provider.name"])
	require.Equal(t, "13", r["gen_ai.usage.input_tokens"])
	require.Equal(t, "5", r["gen_ai.usage.output_tokens"])
	require.Equal(t, "3", r["gen_ai.usage.cache_creation.input_tokens"])
	require.NotContains(t, r, "dash0.gen_ai.usage.cache_creation.ephemeral_1h.input_tokens")
	require.NotContains(t, r, "gen_ai.usage.reasoning.output_tokens")
}

func TestContentFollowsOmitIO(t *testing.T) {
	turn := Turn{SessionID: "s1", Start: at(0), End: at(2), Status: "done",
		Prompt: "SECRET-PROMPT", Response: "SECRET-ANSWER",
		Calls: []Call{{Provider: "anthropic", Model: "claude-sonnet-5", Start: at(0), End: at(2), StopReason: "stop"}},
		Tools: []Tool{{ID: "t1", Name: "read", Start: at(0), End: at(1), Input: `{"path":"/x"}`, Output: "SECRET-FILE"}}}
	redacted, err := BuildTrace(turn, cfg(true))
	require.NoError(t, err)
	encoded, _ := json.Marshal(redacted)
	for _, secret := range []string{"SECRET-PROMPT", "SECRET-ANSWER", "SECRET-FILE"} {
		require.NotContains(t, string(encoded), secret)
	}
	require.Contains(t, string(encoded), "gen_ai.input.messages")

	spans := build(t, turn, false)
	require.Contains(t, attrs(spans[0])["gen_ai.input.messages"], "SECRET-PROMPT")
	require.Contains(t, attrs(spans[0])["gen_ai.output.messages"], "SECRET-ANSWER")
	require.Equal(t, "SECRET-FILE", attrs(spans[1])["gen_ai.tool.call.result"])
}

// Pi loads a skill two ways, and each lands where Claude and Codex put it: a
// person's /skill:name command on the turn root, the model's read of SKILL.md
// on that read's tool span.
func TestSkillIsNamedByRoute(t *testing.T) {
	turn := Turn{SessionID: "s1", Start: at(0), End: at(9), Status: "done", Skill: "qa-echo",
		Tools: []Tool{
			{ID: "r1", Name: "read", Start: at(1), End: at(2), Skill: "pdf-tools"},
			{ID: "r2", Name: "read", Start: at(2), End: at(3)},
			{ID: "r3", Name: "read", Start: at(3), End: at(4), Skill: "../bad name"},
		}}
	spans := build(t, turn, true)
	root := attrs(spans[0])
	require.Equal(t, "qa-echo", root["dash0.gen_ai.tool.skill.name"])
	require.Equal(t, "command", root["dash0.gen_ai.tool.skill.source"])
	read := attrs(spans[1])
	require.Equal(t, "pdf-tools", read["dash0.gen_ai.tool.skill.name"])
	require.Equal(t, "model", read["dash0.gen_ai.tool.skill.source"])
	for _, s := range spans[2:] {
		require.NotContains(t, attrs(s), "dash0.gen_ai.tool.skill.name")
	}

	bad := Turn{SessionID: "s1", Start: at(0), End: at(1), Status: "done", Skill: "a b"}
	require.Error(t, bad.Validate())
}

func TestForkedSessionNamesItsParent(t *testing.T) {
	turn := Turn{SessionID: "child", ParentSessionID: "parent", Start: at(0), End: at(1), Status: "done", Truncated: true}
	r := attrs(build(t, turn, true)[0])
	require.Equal(t, "parent", r["dash0.pi.session.parent.id"])
	require.Equal(t, "true", r["dash0.pi.truncated"])
	require.NotContains(t, attrs(build(t, Turn{SessionID: "s", Start: at(0), End: at(1), Status: "done"}, true)[0]), "dash0.pi.truncated")
}

func TestTraceIDsAreFreshPerTurn(t *testing.T) {
	turn := Turn{SessionID: "s1", Start: at(0), End: at(1), Status: "done",
		Tools: []Tool{{ID: "t1", Name: "read", Start: at(0), End: at(1)}}}
	a, b := build(t, turn, true), build(t, turn, true)
	require.NotEqual(t, a[0].TraceID, b[0].TraceID)
	require.NotEqual(t, a[1].SpanID, b[1].SpanID, "tool IDs repeat across turns, span IDs must not")
}

func TestValidateRejectsAmbiguousTurns(t *testing.T) {
	ok := Turn{SessionID: "s1", Start: at(0), End: at(1), Status: "done"}
	require.NoError(t, ok.Validate())
	for name, mutate := range map[string]func(*Turn){
		"path in session":  func(t *Turn) { t.SessionID = "../etc" },
		"empty session":    func(t *Turn) { t.SessionID = "" },
		"bad parent":       func(t *Turn) { t.ParentSessionID = "a/b" },
		"end before start": func(t *Turn) { t.End = at(-1) },
		"zero start":       func(t *Turn) { t.Start = time.Time{} },
		"unknown status":   func(t *Turn) { t.Status = "cancelled" },
		"too many tools":   func(t *Turn) { t.Tools = make([]Tool, maxItems+1) },
	} {
		t.Run(name, func(t *testing.T) {
			turn := ok
			mutate(&turn)
			require.Error(t, turn.Validate())
		})
	}
}

// Pi extensions register providers with free-text IDs and model names, so a
// call the helper cannot export loses its own span, as a bad tool does, and
// the rest of the turn is still sent. A tool keeps its span but loses the
// model of a dropped call.
func TestInvalidCallsAreSkippedNotFatal(t *testing.T) {
	good := Call{Provider: "anthropic", Model: "claude-sonnet-5", Start: at(0), End: at(1), StopReason: "toolUse", Usage: Usage{Input: 1, Output: 1}}
	for name, bad := range map[string]Call{
		"model with a space":  {Provider: "my-proxy", Model: "local llama", Start: at(1), End: at(2)},
		"empty model":         {Provider: "p", Start: at(1), End: at(2)},
		"negative tokens":     {Provider: "p", Model: "m", Start: at(1), End: at(2), Usage: Usage{Input: -1}},
		"call ends too early": {Provider: "p", Model: "m", Start: at(2), End: at(1)},
		"zero start":          {Provider: "p", Model: "m", End: at(2)},
		"zero start and end":  {Provider: "p", Model: "m"},
		"billing mode api":    {Provider: "p", Model: "m", Start: at(1), End: at(2), BillingMode: "api"},
		"bad response model":  {Provider: "p", Model: "m", Start: at(1), End: at(2), ResponseModel: "a b"},
	} {
		t.Run(name, func(t *testing.T) {
			turn := Turn{SessionID: "s1", Start: at(0), End: at(9), Status: "done",
				Calls: []Call{good, bad, good},
				Tools: []Tool{
					{ID: "t0", Name: "read", Start: at(1), End: at(2), Call: ptr(0)},
					{ID: "t1", Name: "bash", Start: at(2), End: at(3), Call: ptr(1)},
					{ID: "t2", Name: "bash", Start: at(3), End: at(4), Call: ptr(2)},
				}}
			require.NoError(t, turn.Validate())
			spans := build(t, turn, true)
			require.Len(t, spans, 5, "two calls and three tools")
			require.Equal(t, "chat claude-sonnet-5", spans[0].Name)
			require.Equal(t, "chat claude-sonnet-5", spans[1].Name)
			byID := map[string]map[string]string{}
			for _, s := range spans[2:] {
				byID[attrs(s)["gen_ai.tool.call.id"]] = attrs(s)
			}
			require.Equal(t, "claude-sonnet-5", byID["t0"]["gen_ai.request.model"])
			require.NotContains(t, byID["t1"], "gen_ai.request.model")
			require.Equal(t, "claude-sonnet-5", byID["t2"]["gen_ai.request.model"])
		})
	}
}

func TestInvalidToolsAreSkippedNotFatal(t *testing.T) {
	turn := Turn{SessionID: "s1", Start: at(0), End: at(9), Status: "done",
		Tools: []Tool{
			{ID: "dup", Name: "read", Start: at(1), End: at(2)},
			{ID: "dup", Name: "read", Start: at(1), End: at(2)},
			{ID: "", Name: "read", Start: at(1), End: at(2)},
			{ID: "back", Name: "read", Start: at(2), End: at(1)},
			// The child spans of a sub-agent take the times of their tool.
			{ID: "zero", Name: "subagent", End: at(1),
				Subagents: []Subagent{{Agent: "scout", Model: "anthropic/claude-sonnet-5", Turns: 1, Usage: Usage{Input: 1, Output: 1}}}},
			{ID: "zero-both", Name: "read"},
			{ID: "bad-call", Name: "read", Start: at(1), End: at(2), Call: ptr(7)},
		}}
	spans := build(t, turn, true)
	require.Len(t, spans, 3)
	require.NotContains(t, attrs(spans[2]), "gen_ai.request.model")
}

func mustNano(t *testing.T, s string) int64 {
	t.Helper()
	var n int64
	require.NoError(t, json.Unmarshal([]byte(s), &n))
	return n
}
