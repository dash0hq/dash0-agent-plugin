// SPDX-FileCopyrightText: Copyright 2026 Dash0 Inc.
// SPDX-License-Identifier: Apache-2.0

//go:build e2e

package e2e

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/dash0hq/dash0-agent-plugin/internal/otlp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The three gaps below were all found by a live QA run and were all invisible in
// the totals: no count moved and no error surfaced. These are the wire-format
// half of that verification, which a live run cannot see at all — the managed
// install cannot be reconfigured to log its payloads, so QA reads spans back out
// of the product and never the bytes. Hermetic: no Claude CLI, no API key.

// A sub-agent's tool call arrives after the spawning turn's Stop has cleared the
// session trace context, because the Task tool returns as soon as the agent is
// launched. Every such span used to be dropped.
func TestE2ESubAgentToolSpanSurvivesStop(t *testing.T) {
	h := newHookHarness(t)
	const sessionID = "e2e-subagent-after-stop"

	h.feed(fmt.Sprintf(`{"hook_event_name":"UserPromptSubmit","session_id":%q,"prompt":"delegate it"}`, sessionID))
	h.feed(fmt.Sprintf(`{"hook_event_name":"SubagentStart","session_id":%q,"agent_id":"agent1","agent_type":"general-purpose"}`, sessionID))
	h.feed(fmt.Sprintf(`{"hook_event_name":"Stop","session_id":%q}`, sessionID))
	h.feed(fmt.Sprintf(`{"hook_event_name":"PostToolUse","session_id":%q,"agent_id":"agent1","agent_type":"general-purpose","tool_name":"Bash","tool_use_id":"tu1","duration_ms":1000}`, sessionID))

	spans := h.spans()
	chat := findSpanPrefix(t, spans, "chat")
	tool := findSpanPrefix(t, spans, "execute_tool Bash")

	assert.Equal(t, chat.TraceID, tool.TraceID, "the tool span joins the spawning turn's trace")
	assert.Equal(t, otlp.SpanIDFromAgentID("agent1"), tool.ParentSpanID, "parented under the Agent tool span")
}

// A skill invoked by its slash command runs no tool, so it produces no
// execute_tool span. The invocation is reported on the turn's chat span instead,
// under the same key the Skill tool uses.
func TestE2ESlashInvokedSkillIsOnTheChatSpan(t *testing.T) {
	h := newHookHarness(t)
	const sessionID = "e2e-slash-skill"

	transcript := filepath.Join(t.TempDir(), "transcript.jsonl")
	lines := strings.Join([]string{
		`{"type":"user","message":{"role":"user","content":"<command-message>writing:unslop</command-message>\n<command-name>/writing:unslop</command-name>\n<command-args>text</command-args>"}}`,
		`{"type":"user","isMeta":true,"message":{"role":"user","content":[{"type":"text","text":"Base directory for this skill: /home/me/.claude/plugins/cache/mp/writing/0.3.0/skills/unslop\n\n# Unslop\n"}]}}`,
		`{"type":"assistant","requestId":"req_1","message":{"role":"assistant","model":"claude-opus-4-8","stop_reason":"end_turn","content":[{"type":"text","text":"hi"}],"usage":{"input_tokens":10,"output_tokens":20,"cache_read_input_tokens":0,"cache_creation_input_tokens":0}}}`,
	}, "\n") + "\n"
	require.NoError(t, os.WriteFile(transcript, []byte(lines), 0o644))

	h.feed(fmt.Sprintf(`{"hook_event_name":"UserPromptSubmit","session_id":%q,"prompt":"/writing:unslop text"}`, sessionID))
	h.feed(fmt.Sprintf(`{"hook_event_name":"Stop","session_id":%q,"transcript_path":%q}`, sessionID, transcript))

	chat := findSpanPrefix(t, h.spans(), "chat")
	assert.Equal(t, "writing:unslop", spanStringAttr(t, *chat, "dash0.gen_ai.tool.skill.name"))
	assert.Equal(t, "command", spanStringAttr(t, *chat, "dash0.gen_ai.tool.skill.source"))
}

// A built-in slash command writes the same command tag and loads no skill.
// Counting it would put /compact and /plugin into skill usage.
func TestE2EBuiltinSlashCommandIsNotASkill(t *testing.T) {
	h := newHookHarness(t)
	const sessionID = "e2e-builtin-command"

	transcript := filepath.Join(t.TempDir(), "transcript.jsonl")
	lines := strings.Join([]string{
		`{"type":"user","message":{"role":"user","content":"<command-name>/compact</command-name>\n<command-args></command-args>"}}`,
		`{"type":"assistant","requestId":"req_1","message":{"role":"assistant","model":"claude-opus-4-8","stop_reason":"end_turn","content":[{"type":"text","text":"hi"}],"usage":{"input_tokens":10,"output_tokens":20}}}`,
	}, "\n") + "\n"
	require.NoError(t, os.WriteFile(transcript, []byte(lines), 0o644))

	h.feed(fmt.Sprintf(`{"hook_event_name":"UserPromptSubmit","session_id":%q,"prompt":"/compact"}`, sessionID))
	h.feed(fmt.Sprintf(`{"hook_event_name":"Stop","session_id":%q,"transcript_path":%q}`, sessionID, transcript))

	chat := findSpanPrefix(t, h.spans(), "chat")
	for _, a := range chat.Attributes {
		assert.NotEqual(t, "dash0.gen_ai.tool.skill.name", a.Key)
		assert.NotEqual(t, "dash0.gen_ai.tool.skill.source", a.Key)
	}
}

// Thinking tokens are inside output_tokens and billed at the output rate, so the
// split changes no total and no cost. Without it a long deliberation is
// indistinguishable from a long answer.
func TestE2EReasoningTokensOnTheWire(t *testing.T) {
	h := newHookHarness(t)
	const sessionID = "e2e-reasoning-tokens"

	transcript := filepath.Join(t.TempDir(), "transcript.jsonl")
	lines := strings.Join([]string{
		`{"type":"user","message":{"role":"user","content":"think about it"}}`,
		`{"type":"assistant","requestId":"req_1","message":{"role":"assistant","model":"claude-opus-4-8","stop_reason":"end_turn","content":[{"type":"text","text":"hi"}],"usage":{"input_tokens":10,"output_tokens":116,"cache_read_input_tokens":0,"cache_creation_input_tokens":0,"output_tokens_details":{"thinking_tokens":104}}}}`,
	}, "\n") + "\n"
	require.NoError(t, os.WriteFile(transcript, []byte(lines), 0o644))

	h.feed(fmt.Sprintf(`{"hook_event_name":"UserPromptSubmit","session_id":%q,"prompt":"think about it"}`, sessionID))
	h.feed(fmt.Sprintf(`{"hook_event_name":"Stop","session_id":%q,"transcript_path":%q}`, sessionID, transcript))

	chat := findSpanPrefix(t, h.spans(), "chat")
	assert.Equal(t, int64(116), spanIntAttr(t, *chat, "gen_ai.usage.output_tokens"))
	assert.Equal(t, int64(104), spanIntAttr(t, *chat, "gen_ai.usage.reasoning.output_tokens"))
}

// hookHarness builds the claude-on-event binary once and feeds it hook payloads
// against a mock OTLP endpoint that keeps every traces request.
type hookHarness struct {
	t       *testing.T
	binary  string
	dataDir string
	url     string
	mu      *sync.Mutex
	bodies  *[][]byte
}

func newHookHarness(t *testing.T) *hookHarness {
	t.Helper()
	pluginDir := findPluginDir(t)

	binary := filepath.Join(t.TempDir(), "on-event")
	build := exec.Command("go", "build", "-o", binary, "./cmd/claude-on-event")
	build.Dir = pluginDir
	out, err := build.CombinedOutput()
	require.NoError(t, err, "build failed: %s", string(out))

	var (
		mu     sync.Mutex
		bodies [][]byte
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if r.URL.Path == "/v1/traces" {
			mu.Lock()
			bodies = append(bodies, body)
			mu.Unlock()
		}
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)

	return &hookHarness{t: t, binary: binary, dataDir: t.TempDir(), url: srv.URL, mu: &mu, bodies: &bodies}
}

func (h *hookHarness) feed(event string) {
	h.t.Helper()
	runBinary(h.t, h.binary, event, h.dataDir, h.url)
}

func (h *hookHarness) spans() []otlp.Span {
	h.t.Helper()
	time.Sleep(500 * time.Millisecond)
	h.mu.Lock()
	defer h.mu.Unlock()
	spans := collectSpans(h.t, *h.bodies)
	require.NotEmpty(h.t, spans, "expected exported spans")
	return spans
}

// findSpanPrefix returns the first span whose name starts with prefix, failing
// the test when none does.
func findSpanPrefix(t *testing.T, spans []otlp.Span, prefix string) *otlp.Span {
	t.Helper()
	for i := range spans {
		if strings.HasPrefix(spans[i].Name, prefix) {
			return &spans[i]
		}
	}
	names := make([]string, 0, len(spans))
	for _, s := range spans {
		names = append(names, s.Name)
	}
	t.Fatalf("no span named %q* among %v", prefix, names)
	return nil
}

// spanStringAttr returns the string value of an attribute on a span, failing the
// test if it is missing or not a string.
func spanStringAttr(t *testing.T, s otlp.Span, key string) string {
	t.Helper()
	for _, a := range s.Attributes {
		if a.Key != key {
			continue
		}
		require.NotNil(t, a.Value.StringValue, "attribute %s is not a string", key)
		return *a.Value.StringValue
	}
	t.Fatalf("attribute %s not found on span %q", key, s.Name)
	return ""
}
