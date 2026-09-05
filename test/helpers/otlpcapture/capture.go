// SPDX-FileCopyrightText: Copyright 2026 Dash0 Inc.
// SPDX-License-Identifier: Apache-2.0

// Package otlpcapture stands in for the collector: an OTLP endpoint that records
// what the plugin exported, plus the selectors and assertions a test makes over
// the spans that arrive.
//
// The plugin exports asynchronously from a process that then exits, so a test
// asserts on what reached the endpoint rather than on a return value. The
// chat-span count doubles as the "turn finished" signal; see WaitForChatSpans.
package otlpcapture

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/dash0hq/dash0-agent-plugin/internal/otlp"
)

// pollInterval is how often WaitForChatSpans re-checks. A turn takes tens of
// seconds, so this only bounds how quickly a satisfied wait returns.
const pollInterval = 250 * time.Millisecond

// TracesPath is the only endpoint that carries spans.
const TracesPath = "/v1/traces"

// Request is one OTLP request the plugin sent.
type Request struct {
	Path   string
	Auth   string
	Method string
	Body   []byte
}

// Capture is an OTLP endpoint that records every request. The plugin exports
// asynchronously from a short-lived process, so a test asserts on what arrived
// rather than on a return value.
type Capture struct {
	mu       sync.Mutex
	requests []Request
}

// New starts a recording OTLP server. Close it through the returned
// server.
func New(t *testing.T) (*Capture, *httptest.Server) {
	t.Helper()
	c := &Capture{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		c.mu.Lock()
		c.requests = append(c.requests, Request{
			Path:   r.URL.Path,
			Auth:   r.Header.Get("Authorization"),
			Method: r.Method,
			Body:   body,
		})
		c.mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	return c, srv
}

// Requests returns a copy of everything recorded so far.
func (c *Capture) Requests() []Request {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]Request(nil), c.requests...)
}

// TraceBodies returns the bodies of the requests that carry spans.
func (c *Capture) TraceBodies() [][]byte {
	var bodies [][]byte
	for _, r := range c.Requests() {
		if r.Path == TracesPath {
			bodies = append(bodies, r.Body)
		}
	}
	return bodies
}

// AssertAuthToken checks that the spans arrived under the token the install
// configured.
//
// Spans arriving is not the same as spans arriving authenticated. This capture
// answers 200 to anything, so a plugin that lost the token, or read it from the
// developer's own configuration instead of the one under test, still turns every
// other assertion in the canary green. In production that is a 401 and no
// telemetry at all.
//
// Every trace request, not just the first: a hook process resolves the token
// once, and each turn is a new process, so one turn exporting unauthenticated is
// a state the first request cannot show.
//
// Reports each offender by position rather than stopping at one, because these
// canaries cost a live turn to run and the pattern is the diagnosis; the first
// request differing points at the install, a later one at config resolution
// under a hook that ran somewhere else.
func (c *Capture) AssertAuthToken(t *testing.T, token string) {
	t.Helper()

	want := "Bearer " + token

	var traces []Request
	for _, r := range c.Requests() {
		if r.Path == TracesPath {
			traces = append(traces, r)
		}
	}
	require.NotEmpty(t, traces, "no trace request to take an Authorization header from")

	// Numbered over the trace requests only. Counting positions in c.Requests()
	// would report "request 3 of 7" for the second of two exports, because the
	// total spans every path the capture serves.
	for i, r := range traces {
		assert.Equal(t, want, r.Auth,
			"trace request %d of %d exported spans without the configured token; in "+
				"production the collector answers 401 and the user gets no telemetry",
			i+1, len(traces))
	}
}

// Reset drops what has been recorded, so one server can serve several phases of
// a test without their requests running together.
func (c *Capture) Reset() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.requests = nil
}

// Spans decodes every span the capture received.
func (c *Capture) Spans(t *testing.T) []otlp.Span {
	t.Helper()
	return CollectSpans(t, c.TraceBodies())
}

// CollectSpans decodes spans out of OTLP request bodies. A body that does not
// parse is skipped rather than fatal: a test may point other traffic at the same
// endpoint.
func CollectSpans(t *testing.T, bodies [][]byte) []otlp.Span {
	t.Helper()
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
	return spans
}

// LogSpanTree renders the spans as a parent to child tree, visible with -v and
// on failure. A span whose parent was never emitted is shown at the root, so
// nothing is hidden.
func LogSpanTree(t *testing.T, spans []otlp.Span) {
	t.Helper()
	known := make(map[string]bool, len(spans))
	for _, s := range spans {
		known[s.SpanID] = true
	}
	children := map[string][]otlp.Span{}
	for _, s := range spans {
		parent := s.ParentSpanID
		if parent != "" && !known[parent] {
			parent = "" // dangling or external parent, treat as root for display
		}
		children[parent] = append(children[parent], s)
	}

	var b strings.Builder
	b.WriteString(fmt.Sprintf("received %d span(s):\n", len(spans)))
	var walk func(parent, indent string)
	walk = func(parent, indent string) {
		for _, s := range children[parent] {
			b.WriteString(fmt.Sprintf("%s- %s%s\n", indent, s.Name, spanTag(s)))
			// Guard against self-parenting: a span with an empty or self-equal
			// SpanID lands under the "" root and would recurse into itself,
			// growing the builder without bound.
			if s.SpanID != "" && s.SpanID != parent {
				walk(s.SpanID, indent+"    ")
			}
		}
	}
	walk("", "  ")
	t.Log(b.String())
}

// spanTag is a compact suffix of the most useful identity attributes.
func spanTag(s otlp.Span) string {
	var parts []string
	for _, a := range s.Attributes {
		if a.Value.StringValue == nil {
			continue
		}
		switch a.Key {
		case "gen_ai.harness.name", "gen_ai.provider.name", "gen_ai.tool.name", "gen_ai.agent.id":
			parts = append(parts, a.Key+"="+*a.Value.StringValue)
		}
	}
	if len(parts) == 0 {
		return ""
	}
	return "  [" + strings.Join(parts, " ") + "]"
}

// SpanHasPositiveTokenUsage reports whether the span carries a
// gen_ai.usage.*_tokens attribute with a positive value.
func SpanHasPositiveTokenUsage(s otlp.Span) bool {
	for _, a := range s.Attributes {
		if !strings.HasPrefix(a.Key, "gen_ai.usage.") || !strings.HasSuffix(a.Key, "_tokens") {
			continue
		}
		if a.Value.IntValue == nil {
			continue
		}
		if n, err := strconv.ParseInt(*a.Value.IntValue, 10, 64); err == nil && n > 0 {
			return true
		}
	}
	return false
}

// WaitForChatSpans blocks until the capture holds at least n chat spans, which is
// how a caller knows n turns have closed.
//
// The plugin emits one chat span per turn on the runtime's stop event, so this is
// the agent-agnostic "turn finished" signal and no test needs to know any agent's
// idle prompt. The cost is that a run producing no telemetry times out here rather
// than failing an assertion, so the message says so.
//
// onPoll runs on every poll. A CLI can raise a dialog in the MIDDLE of a turn
// (Codex offers a cheaper model near a rate limit) and an unanswered one stops the
// turn from ever closing, so handling it between turns is too late.
func (c *Capture) WaitForChatSpans(t *testing.T, n int, timeout time.Duration, onPoll ...func()) {
	t.Helper()

	deadline := time.Now().Add(timeout)
	for {
		for _, poll := range onPoll {
			poll()
		}
		got := len(ChatSpans(CollectSpans(t, c.TraceBodies())))
		if got >= n {
			return
		}
		if !time.Now().Before(deadline) {
			// A bare count cannot tell "the turn never ran" from "its telemetry was
			// dropped", and those need opposite investigations.
			all := CollectSpans(t, c.TraceBodies())
			LogSpanTree(t, all)
			t.Fatalf("timed out after %s waiting for %d chat span(s); %d arrived "+
				"(%d requests, %d spans total: %s).\n"+
				"Either the turn never completed, or it completed and the plugin exported "+
				"nothing for it; check the session transcript above for the turn's own output",
				timeout, n, got, len(c.Requests()), len(all), DescribeTurns(all))
		}
		time.Sleep(pollInterval)
	}
}

// WaitForSkillSpan blocks until a tool span carrying
// dash0.gen_ai.tool.skill.name=name arrives, and fails with the captured tree if
// it does not.
//
// Needed wherever a runtime exports a turn's tool spans AFTER the chat span that
// closes it, which is the order cmd/copilot-on-event uses: pipeline.Process puts
// the chat span on the wire, then copilot.EmitTurn follows with the tools. A test
// reading the capture the moment WaitForChatSpans releases can therefore see the
// turn as closed while its skill span is still in flight, and the slower the host
// the more often it does. Nothing is lost when that happens — otlp.sendOTLP is a
// blocking POST and the hook sends both spans before it exits — so waiting is all
// this needs to be.
//
// timeout covers one HTTP round trip from a process that has already run rather
// than a model call, so seconds rather than a turn's budget.
func (c *Capture) WaitForSkillSpan(t *testing.T, name string, timeout time.Duration) {
	t.Helper()

	deadline := time.Now().Add(timeout)
	for {
		spans := CollectSpans(t, c.TraceBodies())
		if slices.Contains(SkillNames(spans), name) {
			return
		}
		if !time.Now().Before(deadline) {
			LogSpanTree(t, spans)
			t.Fatalf("timed out after %s waiting for a tool span carrying "+
				"dash0.gen_ai.tool.skill.name=%s; got %s.\n"+
				"Either the agent never invoked the skill, or the plugin exported the "+
				"tool span without the skill attribute; check the session transcript above",
				timeout, name, DescribeTurns(spans))
		}
		time.Sleep(pollInterval)
	}
}
