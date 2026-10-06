// SPDX-FileCopyrightText: Copyright 2026 Dash0 Inc.
// SPDX-License-Identifier: Apache-2.0

package opencode

import (
	"bufio"
	"encoding/json"
	"flag"
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

	"github.com/dash0hq/dash0-agent-plugin/internal/otlp"
	"github.com/dash0hq/dash0-agent-plugin/internal/pipeline"
)

var update = flag.Bool("update", false, "regenerate the golden span snapshot")

// TestGoldenSpanTree drives the full OpenCode producer (Normalize →
// pipeline.Process → OTLP export) over the recorded session and snapshots the
// resulting span tree. Volatile fields (random span/trace IDs, absolute
// timestamps, machine/VCS context) are canonicalized so the snapshot is stable
// across machines and runs.
//
// The input is testdata/forwarded_envelopes.jsonl: captured_events.jsonl as the
// real TypeScript translator forwards it, envelope context and all. That file is
// generated and kept honest by opencode/test/envelopes.test.ts, so the plugin's
// filtering is not reimplemented here in a second, drifting copy.
//
// Regenerate with: go test ./internal/source/opencode -run Golden -update
func TestGoldenSpanTree(t *testing.T) {
	var mu sync.Mutex
	var bodies [][]byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		bodies = append(bodies, b)
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	dataDir := t.TempDir()
	cfg := otlp.Config{
		OTLPUrl:      srv.URL,
		AuthToken:    "test-token",
		Dataset:      "test",
		AgentName:    "opencode",
		HarnessName:  "opencode",
		OmitUserInfo: true,
		OmitIO:       false,
	}
	require.True(t, cfg.ValidateURL())

	f, err := os.Open(filepath.Join("testdata", "forwarded_envelopes.jsonl"))
	require.NoError(t, err)
	defer f.Close()

	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	cwds := map[string]bool{}

	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1024*1024), 4*1024*1024)
	i := 0
	for sc.Scan() {
		line := sc.Bytes()
		if len(line) == 0 {
			continue
		}
		var envelope map[string]any
		require.NoError(t, json.Unmarshal(line, &envelope))
		if c := str(envelope["cwd"]); c != "" {
			cwds[c] = true
		}

		ts := base.Add(time.Duration(i) * time.Second)
		i++

		event := Normalize(envelope)
		if event == nil {
			continue
		}
		// Export failures are logged, not returned; the connectivity POST on
		// SessionStart is harmless (empty resourceSpans, filtered below).
		_, _ = pipeline.Process(event, cfg, dataDir, ts)
	}
	require.NoError(t, sc.Err())

	mu.Lock()
	defer mu.Unlock()

	var spans []otlp.Span
	for _, b := range bodies {
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
	require.NotEmpty(t, spans, "expected spans from the replayed session")

	canon := canonicalize(spans, cwds)

	traces := map[string]bool{}
	var sawMCP, sawSubagent bool
	for _, s := range canon {
		traces[s.Trace] = true

		// Structural invariant, independent of the byte snapshot: every span must
		// parent to a span that was actually emitted (or be a root). Guards the
		// sub-agent anchoring, which only holds because the normalizer presents
		// OpenCode's delegation as a tool named "Agent" carrying agentId.
		assert.False(t, strings.HasPrefix(s.Parent, "MISSING"),
			"span %q (%s) has a dangling parent %s", s.Name, s.Span, s.Parent)

		// Cost-contract invariant: Dash0 derives the uncached prompt portion by
		// subtracting cache_read from gen_ai.usage.input_tokens, so the emitted
		// input must stay inclusive of it. Fails even if the golden is blindly
		// re-blessed with a pre-subtracted value.
		if in, ok := goldenIntAttr(s, "gen_ai.usage.input_tokens"); ok {
			cacheRead, _ := goldenIntAttr(s, "gen_ai.usage.cache_read.input_tokens")
			assert.GreaterOrEqualf(t, in, cacheRead,
				"span %q: input_tokens (%d) must be inclusive of cache_read (%d), not pre-subtracted",
				s.Name, in, cacheRead)
		}

		// OpenCode names an MCP-provided tool `<server>_<tool>`; the normalizer
		// rewrites it to mcp__<server>__<tool> so the pipeline's shared extractor
		// attributes the server and strips the prefix with no OpenCode-specific code.
		if s.Attributes["dash0.gen_ai.tool.mcp_server"] == "capture" {
			sawMCP = true
			assert.Equal(t, "echo", s.Attributes["gen_ai.tool.name"],
				"MCP tool name must be normalized (server prefix stripped)")
			assert.Equal(t, "execute_tool", s.Attributes["gen_ai.operation.name"])
		}
		if strings.HasPrefix(s.Name, "invoke_agent") {
			sawSubagent = true
			assert.NotEmpty(t, s.Attributes["gen_ai.agent.id"], "a sub-agent span must identify its agent")
		}
	}
	assert.True(t, sawMCP, "expected an MCP tool span with mcp_server attribution")
	assert.True(t, sawSubagent, "expected the delegated sub-agent to produce an invoke_agent span")
	assert.Len(t, traces, 1, "one turn is one trace, sub-agent included")

	got := marshalGolden(t, canon)
	goldenPath := filepath.Join("testdata", "golden_spans.json")

	if *update {
		require.NoError(t, os.WriteFile(goldenPath, got, 0o644))
		t.Logf("wrote %s (%d spans)", goldenPath, len(spans))
		return
	}

	want, err := os.ReadFile(goldenPath)
	require.NoError(t, err, "missing golden file; run with -update")
	assert.Equal(t, string(want), string(got))
}

// goldenIntAttr reads a canonicalized int attribute (stored as "int:<n>") off a
// golden span, returning the value and whether it was present as an int.
func goldenIntAttr(s goldenSpan, key string) (int64, bool) {
	v, ok := s.Attributes[key]
	if !ok || !strings.HasPrefix(v, "int:") {
		return 0, false
	}
	n, err := strconv.ParseInt(strings.TrimPrefix(v, "int:"), 10, 64)
	if err != nil {
		return 0, false
	}
	return n, true
}

// goldenSpan is the stable, snapshot-friendly projection of an OTLP span.
type goldenSpan struct {
	Name       string            `json:"name"`
	Kind       int               `json:"kind"`
	Trace      string            `json:"trace"`
	Span       string            `json:"span"`
	Parent     string            `json:"parent"`
	DurationMs int64             `json:"durationMs"`
	Status     string            `json:"status,omitempty"`
	Attributes map[string]string `json:"attributes"`
}

// volatileDrop lists span attributes that vary by machine/checkout and must not
// enter the snapshot (git/VCS context, user identity, absolute workspace path).
var volatileDrop = map[string]bool{
	"user.name":                         true,
	"user.email":                        true,
	"dash0.gen_ai.user.identity.source": true,
	"process.working_directory":         true,
}

func isVolatile(key string) bool {
	return volatileDrop[key] || strings.HasPrefix(key, "dash0.gen_ai.vcs.")
}

// canonicalize replaces non-deterministic identifiers and times with stable
// tokens. Span/trace IDs are tokenized in first-seen order; a parentSpanId that
// references no emitted span is tokenized as "MISSING-N" so dangling parents are
// visible in the snapshot rather than hidden.
func canonicalize(spans []otlp.Span, cwds map[string]bool) []goldenSpan {
	traceTok := newTokenizer("trace")
	spanTok := newTokenizer("span")
	// Pre-populate span tokens so parent references resolve regardless of order.
	for _, s := range spans {
		spanTok.get(s.SpanID)
	}
	missingTok := newTokenizer("MISSING")

	out := make([]goldenSpan, 0, len(spans))
	for _, s := range spans {
		parent := ""
		if s.ParentSpanID != "" {
			if tok, ok := spanTok.lookup(s.ParentSpanID); ok {
				parent = tok
			} else {
				parent = missingTok.get(s.ParentSpanID)
			}
		}

		status := ""
		if s.Status.Code != otlp.StatusCodeUnset {
			status = strconv.Itoa(s.Status.Code)
			if s.Status.Message != "" {
				status += ":" + scrubPaths(s.Status.Message, cwds)
			}
		}

		out = append(out, goldenSpan{
			Name:       s.Name,
			Kind:       s.Kind,
			Trace:      traceTok.get(s.TraceID),
			Span:       spanTok.get(s.SpanID),
			Parent:     parent,
			DurationMs: durationMs(s),
			Status:     status,
			Attributes: canonAttrs(s.Attributes, cwds),
		})
	}
	return out
}

func canonAttrs(attrs []otlp.Attribute, cwds map[string]bool) map[string]string {
	m := make(map[string]string, len(attrs))
	for _, a := range attrs {
		if isVolatile(a.Key) {
			continue
		}
		var v string
		switch {
		case a.Value.StringValue != nil:
			v = scrubPaths(*a.Value.StringValue, cwds)
		case a.Value.IntValue != nil:
			v = "int:" + *a.Value.IntValue
		}
		m[a.Key] = v
	}
	return m
}

func durationMs(s otlp.Span) int64 {
	start, err1 := strconv.ParseInt(s.StartTimeUnixNano, 10, 64)
	end, err2 := strconv.ParseInt(s.EndTimeUnixNano, 10, 64)
	if err1 != nil || err2 != nil {
		return -1
	}
	return (end - start) / 1_000_000
}

func scrubPaths(s string, cwds map[string]bool) string {
	for cwd := range cwds {
		s = strings.ReplaceAll(s, cwd, "<CWD>")
	}
	return s
}

func marshalGolden(t *testing.T, spans []goldenSpan) []byte {
	t.Helper()
	b, err := json.MarshalIndent(spans, "", "  ")
	require.NoError(t, err)
	return append(b, '\n')
}

// tokenizer assigns stable, prefixed tokens to opaque IDs in first-seen order.
type tokenizer struct {
	prefix string
	seen   map[string]string
	order  int
}

func newTokenizer(prefix string) *tokenizer {
	return &tokenizer{prefix: prefix, seen: map[string]string{}}
}

func (tk *tokenizer) get(id string) string {
	if id == "" {
		return ""
	}
	if tok, ok := tk.seen[id]; ok {
		return tok
	}
	tk.order++
	tok := tk.prefix + "-" + strconv.Itoa(tk.order)
	tk.seen[id] = tok
	return tok
}

func (tk *tokenizer) lookup(id string) (string, bool) {
	tok, ok := tk.seen[id]
	return tok, ok
}
