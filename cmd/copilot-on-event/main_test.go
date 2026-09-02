// SPDX-FileCopyrightText: Copyright 2026 Dash0 Inc.
// SPDX-License-Identifier: Apache-2.0

// These cover run()'s wiring: which of Copilot's session mechanisms fire on which
// event, and what the entrypoint does with the answer.
//
// The mechanisms themselves are unit-tested in internal/source/copilot. What none
// of those can see is the decision tree here: a sweep never called, a marker read
// from a directory something else deleted, a RemoveAll reached with an id nothing
// vetted.
//
// In-process, calling run() directly, because spawning the built binary and
// waiting for its export costs a build and ~1.8s per case for the same code.
package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/dash0hq/dash0-agent-plugin/internal/harness"
	"github.com/dash0hq/dash0-agent-plugin/internal/otlp"
)

// convID is the conversation id every test drives, and the id ReadTurn looks up
// in the staged native-OTel file.
const convID = "aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee"

// session is one hermetic Copilot runtime: a capture server for the collector, a
// plugin data directory, a native-OTel directory, and the environment a hook
// process sees.
//
// The home moves too, under both names, because os.UserHomeDir reads HOME on POSIX
// and USERPROFILE on Windows. The prefixed options below outrank the config file
// for the URL and the token, but Enabled() reads it directly, so a developer whose
// own ~/.copilot/dash0-agent-plugin.local.md says `enabled: false` would see all
// five of these pass having run nothing.
type session struct {
	t       *testing.T
	mu      sync.Mutex
	bodies  [][]byte
	dataDir string
	otelDir string
	cwd     string
}

func newSession(t *testing.T) *session {
	t.Helper()

	s := &session{t: t, dataDir: t.TempDir(), otelDir: t.TempDir()}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		s.mu.Lock()
		s.bodies = append(s.bodies, body)
		s.mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)

	// The prefixed option form outranks both the config file and the DASH0_
	// fallbacks, so nothing on the developer's machine can redirect this.
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("COPILOT_PLUGIN_OPTION_OTLP_URL", srv.URL)
	t.Setenv("COPILOT_PLUGIN_OPTION_AUTH_TOKEN", "copilot-entrypoint-token")
	t.Setenv("COPILOT_PLUGIN_DATA", s.dataDir)
	t.Setenv("DASH0_COPILOT_OTEL_DIR", s.otelDir)

	cwd, err := os.Getwd()
	require.NoError(t, err)
	s.cwd = cwd
	// run() chdirs into the payload's cwd. In a subprocess that died with it;
	// here it follows the test binary into the next case. run restores it after
	// each event; t.Chdir restores it once the test is over.
	t.Chdir(cwd)

	return s
}

// run drives one hook event the way a hook process does. The entrypoint is
// fail-open, so an error from it fails the test.
func (s *session) run(eventName, payload string) {
	s.t.Helper()

	// One hook process resolves its configuration once.
	harness.ResetConfig()

	oldArgs := os.Args
	oldStdin := os.Stdin
	defer func() {
		os.Args = oldArgs
		os.Stdin = oldStdin
		_ = os.Chdir(s.cwd)
	}()

	os.Args = []string{"copilot-on-event", eventName}

	r, w, err := os.Pipe()
	require.NoError(s.t, err)
	os.Stdin = r
	go func() {
		_, _ = w.WriteString(payload)
		_ = w.Close()
	}()

	require.NoError(s.t, run(), "%s must not return an error", eventName)
}

// spans decodes every span the capture holds.
func (s *session) spans() []otlp.Span {
	s.t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()

	var spans []otlp.Span
	for _, b := range s.bodies {
		var req otlp.ExportTracesRequest
		if err := json.Unmarshal(b, &req); err != nil {
			continue
		}
		for _, rs := range req.ResourceSpans {
			for _, ss := range rs.ScopeSpans {
				spans = append(spans, ss.Spans...)
			}
		}
	}
	return spans
}

// tmpJSON is a temp directory quoted as a JSON string. A raw Windows path is full
// of backslashes and every one starts a JSON escape, so an unquoted path makes
// these pass for the wrong reason: the payload never decodes.
func tmpJSON(t *testing.T) string {
	t.Helper()
	return strconv.Quote(t.TempDir())
}

func spanAttr(s otlp.Span, key string) (string, bool) {
	for _, a := range s.Attributes {
		if a.Key != key {
			continue
		}
		if a.Value.StringValue != nil {
			return *a.Value.StringValue, true
		}
		if a.Value.IntValue != nil {
			return *a.Value.IntValue, true
		}
	}
	return "", false
}

func hasPositiveTokenUsage(s otlp.Span) bool {
	v, ok := spanAttr(s, "gen_ai.usage.input_tokens")
	return ok && v != "" && v != "0"
}

// stageOtelTurn writes a native-OTel file for one turn, mirroring a real capture:
// an invoke_agent root, a chat span with usage and response, a top-level bash tool,
// and a task spawn whose sub-agent runs its own bash.
func stageOtelTurn(t *testing.T, dir, conv string) {
	t.Helper()
	const trace = "11111111111111111111111111111111"
	lines := []string{
		fmt.Sprintf(`{"type":"span","traceId":%q,"spanId":"aaaaaaaaaaaaaa01","parentSpanId":"aaaaaaaaaaaaaa00","name":"chat gpt-5.3-codex","startTime":[1000,0],"endTime":[1001,0],"status":{"code":0},"attributes":{"gen_ai.conversation.id":%q,"gen_ai.request.model":"gpt-5.3-codex","gen_ai.usage.input_tokens":14613,"gen_ai.usage.output_tokens":68,"gen_ai.usage.cache_read.input_tokens":14592,"gen_ai.output.messages":"[{\"role\":\"assistant\",\"parts\":[{\"type\":\"text\",\"content\":\"Echo complete.\"}]}]"}}`, trace, conv),
		fmt.Sprintf(`{"type":"span","traceId":%q,"spanId":"aaaaaaaaaaaaaa02","parentSpanId":"aaaaaaaaaaaaaa00","name":"execute_tool bash","startTime":[1001,0],"endTime":[1001,500000000],"status":{"code":0},"attributes":{"gen_ai.tool.name":"bash","gen_ai.tool.call.id":"call_top","gen_ai.tool.call.arguments":"{\"command\":\"echo hi\"}","gen_ai.tool.call.result":"hi"}}`, trace),
		fmt.Sprintf(`{"type":"span","traceId":%q,"spanId":"aaaaaaaaaaaaaa05","parentSpanId":"aaaaaaaaaaaaaa04","name":"execute_tool bash","startTime":[1002,0],"endTime":[1002,250000000],"status":{"code":0},"attributes":{"gen_ai.tool.name":"bash","gen_ai.tool.call.id":"call_sub","gen_ai.tool.call.arguments":"{\"command\":\"echo hello\"}","gen_ai.tool.call.result":"hello"}}`, trace),
		fmt.Sprintf(`{"type":"span","traceId":%q,"spanId":"aaaaaaaaaaaaaa04","parentSpanId":"aaaaaaaaaaaaaa03","name":"invoke_agent task","startTime":[1001,600000000],"endTime":[1003,0],"status":{"code":0},"attributes":{"gen_ai.conversation.id":%q,"gen_ai.agent.name":"task","gen_ai.agent.id":"builtin:task"}}`, trace, conv),
		fmt.Sprintf(`{"type":"span","traceId":%q,"spanId":"aaaaaaaaaaaaaa03","parentSpanId":"aaaaaaaaaaaaaa00","name":"execute_tool task","startTime":[1001,600000000],"endTime":[1003,100000000],"status":{"code":0},"attributes":{"gen_ai.tool.name":"task","gen_ai.tool.call.id":"call_spawn","gen_ai.tool.call.arguments":"{\"agent_type\":\"task\",\"name\":\"echo-runner\"}","gen_ai.tool.call.result":"done"}}`, trace),
		fmt.Sprintf(`{"type":"span","traceId":%q,"spanId":"aaaaaaaaaaaaaa00","parentSpanId":"","name":"invoke_agent","startTime":[1000,0],"endTime":[1004,0],"status":{"code":0},"attributes":{"gen_ai.conversation.id":%q}}`, trace, conv),
	}
	require.NoError(t, os.WriteFile(filepath.Join(dir, "otel.jsonl"),
		[]byte(strings.Join(lines, "\n")+"\n"), 0o644))
}

// A real session whose sessionStart marker was never written still exports.
//
// That is the shape a failed bootstrap leaves: the first session after a version
// bump downloads the binary inside the sessionStart hook, under Copilot's 10s
// timeout. Reading a missing marker as proof of a sub-agent would silence those
// sessions permanently.
func TestUnmarkedRealSessionStillExports(t *testing.T) {
	s := newSession(t)
	stageOtelTurn(t, s.otelDir, convID)

	sid := `"sessionId":"` + convID + `"`
	// No sessionStart.
	s.run("userPromptSubmitted", `{`+sid+`,"cwd":`+tmpJSON(t)+`,"prompt":"still a real session"}`)
	s.run("agentStop", `{`+sid+`,"cwd":`+tmpJSON(t)+`,"stopReason":"end_turn"}`)

	require.NoFileExists(t, filepath.Join(s.dataDir, "started", convID),
		"the premise: this session really has no marker")

	spans := s.spans()
	require.NotEmpty(t, spans, "a real session whose marker was never written must still export")

	var chatWithUsage bool
	for _, sp := range spans {
		if strings.HasPrefix(sp.Name, "chat") && hasPositiveTokenUsage(sp) {
			chatWithUsage = true
		}
	}
	assert.True(t, chatWithUsage, "and it must carry the turn's usage, not a bare span")
}

// A sub-agent's own hook session mints no conversation of its own.
//
// Copilot gives a sub-agent its own session id. `copilot -p` names it
// call_<toolCallId>, which Normalize drops on the prefix; an interactive session
// gives it a plain UUID the prefix cannot catch. Against Copilot CLI 1.0.80 an
// unguarded delegating turn produces the expected conversation plus a second one
// holding one `chat` span with no model and no usage.
//
// What holds in both modes: a sub-agent session receives no sessionStart, and its
// native-OTel spans carry the PARENT's conversation id.
func TestSubAgentSessionMintsNoConversation(t *testing.T) {
	s := newSession(t)
	stageOtelTurn(t, s.otelDir, convID)

	const subAgentID = "5b65a91f-cbe0-4feb-9736-aadec8fe09b8"
	const trace = `"traceparent":"00-558ca38a5fd1be05a94cf7002271be76-36bfb9a97f18b534-01"`
	parent := `"sessionId":"` + convID + `"`
	sub := `"sessionId":"` + subAgentID + `"`
	cwd := tmpJSON(t)

	s.run("sessionStart", `{`+parent+`,"cwd":`+cwd+`,"source":"new",`+trace+`}`)
	s.run("userPromptSubmitted", `{`+parent+`,"cwd":`+cwd+`,"prompt":"delegate this"}`)

	// The sub-agent's whole lifecycle: a prompt and a stop, no sessionStart.
	s.run("userPromptSubmitted", `{`+sub+`,"cwd":`+cwd+`,"prompt":"echo sub",`+trace+`}`)
	s.run("agentStop", `{`+sub+`,"cwd":`+cwd+`,"stopReason":"end_turn",`+trace+`}`)

	s.run("agentStop", `{`+parent+`,"cwd":`+cwd+`,"stopReason":"end_turn"}`)

	spans := s.spans()
	require.NotEmpty(t, spans)

	conversations := map[string]int{}
	for _, sp := range spans {
		if v, ok := spanAttr(sp, "gen_ai.conversation.id"); ok {
			conversations[v]++
		}
	}
	assert.NotContains(t, conversations, subAgentID,
		"a sub-agent session must mint no conversation, whatever its id looks like")
	assert.Len(t, conversations, 1, "a delegating turn is one conversation; got %v", conversations)
	assert.Positive(t, conversations[convID], "the parent turn must still be exported")

	// The parent is unaffected: its turn still carries the usage and the tools.
	var chatWithUsage, tools int
	for _, sp := range spans {
		switch {
		case strings.HasPrefix(sp.Name, "chat") && hasPositiveTokenUsage(sp):
			chatWithUsage++
		case strings.HasPrefix(sp.Name, "execute_tool"):
			tools++
		}
	}
	assert.Equal(t, 1, chatWithUsage, "the parent turn's chat span must still carry usage")
	assert.Positive(t, tools, "the parent turn's tool spans must still be emitted")
}

// An unusable session id destroys nothing.
//
// The sub-agent suppression removes the session directory of a turn it drops, and
// it resolves that directory from the raw payload, before pipeline.Process
// substitutes a random id for a missing or unsafe one. Without a guard an
// agentStop with no sessionId resolves to the plugin's data root itself and
// deletes it, exit 0 and no diagnostic, and "../victim" walks out of it.
func TestUnsafeSessionIDDestroysNothing(t *testing.T) {
	s := newSession(t)

	root := t.TempDir()
	binCache := filepath.Join(s.dataDir, "bin")
	other := filepath.Join(s.dataDir, "another-live-session")
	sibling := filepath.Join(filepath.Dir(s.dataDir), "victim")
	markers := filepath.Join(s.dataDir, "started")
	otel := filepath.Join(s.dataDir, "otel")
	for _, d := range []string{binCache, other, sibling, markers, otel} {
		require.NoError(t, os.MkdirAll(d, 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(d, "keep"), []byte("x"), 0o644))
	}
	require.NoError(t, os.WriteFile(filepath.Join(markers, "live-session"), nil, 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(otel, "otel.jsonl"), []byte("{}\n"), 0o644))

	cwd := strconv.Quote(root)
	stop := func(payload string) { s.run("agentStop", payload) }

	stop(`{"cwd":` + cwd + `,"stopReason":"end_turn"}`)                         // no sessionId at all
	stop(`{"cwd":` + cwd + `,"sessionId":"../victim","stopReason":"end_turn"}`) // escapes the data dir
	stop(`{"cwd":` + cwd + `,"sessionId":"","stopReason":"end_turn"}`)          // empty is the data dir
	// A safe path segment is not enough: these three name directories the plugin
	// owns.
	for _, id := range []string{"started", "bin", "otel"} {
		stop(`{"cwd":` + cwd + `,"sessionId":"` + id + `","stopReason":"end_turn"}`)
	}

	// sessionEnd is pipeline.Process's RemoveAll rather than this entrypoint's:
	// Process joins the payload's id onto the data dir and removes what it joined,
	// so the id has to be renamed before Process sees it.
	for _, id := range []string{"started", "bin", "otel"} {
		s.run("sessionEnd", `{"cwd":`+cwd+`,"sessionId":"`+id+`"}`)
	}

	assert.DirExists(t, s.dataDir, "the plugin data root must survive an unusable session id")
	assert.FileExists(t, filepath.Join(binCache, "keep"), "the bootstrap's binary cache must survive")
	assert.FileExists(t, filepath.Join(other, "keep"), "a concurrent session's state must survive")
	assert.FileExists(t, filepath.Join(sibling, "keep"), "a sibling directory must be unreachable")
	assert.FileExists(t, filepath.Join(markers, "live-session"), "the marker directory must be unreachable")
	assert.FileExists(t, filepath.Join(otel, "otel.jsonl"),
		"the native-OTel directory must be unreachable: in the default layout it is a child of the data root")

	// The positive controls. Every assertion above holds on code that deletes
	// nothing at all, so both delete paths have to be shown to work and to be
	// reached from here.
	//
	// Two of them, because the cases above straddle two removals: the six agentStop
	// calls reach the sub-agent suppression's own RemoveAll in main.go, and the
	// three sessionEnd calls reach pipeline.Process's.
	agentStopped := filepath.Join(s.dataDir, "suppressed-subagent")
	require.NoError(t, os.MkdirAll(agentStopped, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(agentStopped, "keep"), []byte("x"), 0o644))
	// A well-formed id with no start marker is what the suppression is for: it
	// reads as a sub-agent, emits nothing, and drops its scratch directory.
	s.run("agentStop", `{"cwd":`+cwd+`,"sessionId":"suppressed-subagent","stopReason":"end_turn"}`)
	require.NoDirExists(t, agentStopped,
		"the suppression removed nothing for a well-formed id, so the agentStop cases "+
			"above prove only that this path deletes nothing under any input")

	live := filepath.Join(s.dataDir, "a-real-session")
	require.NoError(t, os.MkdirAll(live, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(live, "keep"), []byte("x"), 0o644))
	s.run("sessionEnd", `{"cwd":`+cwd+`,"sessionId":"a-real-session"}`)
	require.NoDirExists(t, live,
		"sessionEnd removed nothing for a well-formed id, so the sessionEnd cases "+
			"above prove only that this path deletes nothing under any input")

	// Nothing is asserted about spans. A bare Stop has no preceding prompt and so no
	// trace context, and Process declines to build a chat span without one. What
	// matters is that the event took the normal path, not the suppression's RemoveAll.
}

// sessionStart collects what killed runs left behind. The sweep's own behaviour is
// covered at function level, and nothing else covers its wiring.
func TestSweepsOnSessionStart(t *testing.T) {
	s := newSession(t)

	// What a killed run leaves: a session directory nothing will end, and the
	// marker its launch wrote. The marker identifies the directory as this
	// runtime's, because under DASH0_PLUGIN_DATA the data root is shared with
	// Cursor and Codex.
	dead := filepath.Join(s.dataDir, "killed-session")
	require.NoError(t, os.MkdirAll(dead, 0o755))
	require.NoError(t, os.MkdirAll(filepath.Join(s.dataDir, "started"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(s.dataDir, "started", "killed-session"), nil, 0o644))
	events := filepath.Join(dead, "events.jsonl")
	require.NoError(t, os.WriteFile(events, []byte(`{"prompt":"private"}`+"\n"), 0o644))
	old := time.Now().Add(-4 * 7 * 24 * time.Hour)
	require.NoError(t, os.Chtimes(events, old, old))

	s.run("sessionStart", `{"sessionId":"`+convID+`","cwd":`+tmpJSON(t)+`,"source":"new"}`)

	assert.NoDirExists(t, dead,
		"sessionStart must collect what killed runs left, or their prompts sit on disk forever")
	assert.DirExists(t, filepath.Join(s.dataDir, convID),
		"the starting session's own directory must survive its own sweep")
}

// A turn arriving after SessionEnd still exports.
//
// Process removes the session directory on SessionEnd. A later turn recreates it,
// because its userPromptSubmitted mints a fresh trace context, but it cannot
// recreate the sessionStart marker. Keeping the marker inside that directory would
// make the turn read as a sub-agent's and drop it in silence.
//
// The reachable case is the end of every session: agentStop and sessionEnd fire as
// separate processes and agentStop is slower, since it scans the native-OTel file,
// so a sessionEnd that wins the race took the last turn with it.
func TestTurnAfterSessionEndStillExports(t *testing.T) {
	s := newSession(t)
	stageOtelTurn(t, s.otelDir, convID)

	sid := `"sessionId":"` + convID + `"`
	cwd := tmpJSON(t)
	s.run("sessionStart", `{`+sid+`,"cwd":`+cwd+`,"source":"new"}`)
	s.run("userPromptSubmitted", `{`+sid+`,"cwd":`+cwd+`,"prompt":"first"}`)
	s.run("agentStop", `{`+sid+`,"cwd":`+cwd+`,"stopReason":"end_turn"}`)
	s.run("sessionEnd", `{`+sid+`,"cwd":`+cwd+`,"reason":"exit"}`)

	require.NoDirExists(t, filepath.Join(s.dataDir, convID),
		"SessionEnd removes the session directory; that is the premise of this test")

	// The late turn, with no second sessionStart, as a racing agentStop has none.
	before := len(s.spans())
	s.run("userPromptSubmitted", `{`+sid+`,"cwd":`+cwd+`,"prompt":"second"}`)
	s.run("agentStop", `{`+sid+`,"cwd":`+cwd+`,"stopReason":"end_turn"}`)

	after := s.spans()
	require.Greater(t, len(after), before,
		"a turn arriving after SessionEnd must still export; the marker outlives the directory")

	var lateChat bool
	for _, sp := range after[before:] {
		if strings.HasPrefix(sp.Name, "chat") {
			lateChat = true
		}
	}
	assert.True(t, lateChat, "the late turn produced no chat span, so it was dropped")
}
