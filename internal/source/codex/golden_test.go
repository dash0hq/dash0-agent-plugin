package codex

import (
	"bufio"
	"encoding/json"
	"flag"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
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

// TestGoldenSpanTree drives the full Codex producer (Normalize → pipeline.Process
// → OTLP export) over the captured hook fixtures and snapshots the resulting span
// tree. Volatile fields (random span/trace IDs, absolute timestamps, machine/VCS
// context, workspace paths) are canonicalized so the snapshot is stable across
// machines and runs. Regenerate with: go test ./internal/source/codex -run Golden -update
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
		AgentName:    "codex",
		HarnessName:  "codex",
		Provider:     "openai",
		OmitUserInfo: true,
		OmitIO:       false,
	}
	require.True(t, pipeline.ValidateOTLPURL(&cfg))

	f, err := os.Open(filepath.Join("testdata", "captured_events.jsonl"))
	require.NoError(t, err)
	defer f.Close()

	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	cwds := map[string]bool{}

	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1024*1024), 1024*1024)
	i := 0
	for sc.Scan() {
		line := sc.Bytes()
		if len(line) == 0 {
			continue
		}
		var event map[string]any
		require.NoError(t, json.Unmarshal(line, &event))
		if c, ok := event["cwd"].(string); ok && c != "" {
			cwds[c] = true
		}

		ts := base.Add(time.Duration(i) * time.Second)
		i++
		sid, _ := event["session_id"].(string)

		ev := Normalize(event, pipeline.SessionDir(dataDir, sid), ts)
		if ev == nil {
			continue
		}
		// Export failures are logged, not returned; the connectivity POST on
		// SessionStart is harmless (empty resourceSpans, filtered below).
		_, _ = pipeline.Process(ev, cfg, dataDir, ts)
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

	// Structural invariant, independent of the byte snapshot: every span must
	// parent to a span that was actually emitted (or be a root). Guards sub-agent
	// anchoring so a regression fails here even if the golden is blindly re-blessed.
	for _, s := range canon {
		assert.False(t, strings.HasPrefix(s.Parent, "MISSING"),
			"span %q (%s) has a dangling parent %s", s.Name, s.Span, s.Parent)
	}

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
	"user.name":                 true,
	"user.email":                true,
	"process.working_directory": true,
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

var _ = sort.Strings // reserved for future stable ordering needs
