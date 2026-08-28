// SPDX-FileCopyrightText: Copyright 2026 Dash0 Inc.
// SPDX-License-Identifier: Apache-2.0

package pipeline

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/dash0hq/dash0-agent-plugin/internal/incident"
	"github.com/dash0hq/dash0-agent-plugin/internal/spool"
)

// mockLogServer captures log records so tests can assert on what was reported.
func mockLogServer(t *testing.T) (url string, records *[]otlpLogRecord, mu *sync.Mutex) {
	t.Helper()
	var captured []otlpLogRecord
	var lock sync.Mutex
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/logs" {
			body, _ := io.ReadAll(r.Body)
			var req struct {
				ResourceLogs []struct {
					ScopeLogs []struct {
						LogRecords []otlpLogRecord `json:"logRecords"`
					} `json:"scopeLogs"`
				} `json:"resourceLogs"`
			}
			if err := json.Unmarshal(body, &req); err == nil {
				lock.Lock()
				for _, rl := range req.ResourceLogs {
					for _, sl := range rl.ScopeLogs {
						captured = append(captured, sl.LogRecords...)
					}
				}
				lock.Unlock()
			}
		}
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)
	return srv.URL, &captured, &lock
}

type otlpLogRecord struct {
	SeverityText string `json:"severityText"`
	Body         struct {
		StringValue *string `json:"stringValue"`
	} `json:"body"`
	Attributes []struct {
		Key   string `json:"key"`
		Value struct {
			StringValue *string `json:"stringValue"`
			IntValue    *string `json:"intValue"`
		} `json:"value"`
	} `json:"attributes"`
	TraceID string `json:"traceId"`
}

func (r otlpLogRecord) attr(key string) string {
	for _, a := range r.Attributes {
		if a.Key != key {
			continue
		}
		if a.Value.StringValue != nil {
			return *a.Value.StringValue
		}
		if a.Value.IntValue != nil {
			return *a.Value.IntValue
		}
	}
	return ""
}

// A span that cannot be sent is kept, and the next invocation that reaches the
// endpoint sends it. Before the spool this was simply lost, and nothing said so:
// the session's cost data was short and looked complete.
func TestBacklog_UnsentSpanIsKeptAndSentByALaterInvocation(t *testing.T) {
	dead := unreachableURL(t)
	s := newSetup(t, dead)

	s.feed(t, map[string]any{"hook_event_name": "SessionStart", "session_id": "sess-1", "model": "gpt-5.5"})
	s.feed(t, map[string]any{"hook_event_name": "UserPromptSubmit", "session_id": "sess-1", "prompt": "hi"})
	s.feed(t, map[string]any{
		"hook_event_name":            "Stop",
		"session_id":                 "sess-1",
		"gen_ai.usage.input_tokens":  int64(11),
		"gen_ai.usage.output_tokens": int64(22),
	})

	dir := spool.Dir(s.dataDir)
	require.NotZero(t, spool.Len(dir), "an undeliverable span must be kept, not dropped")

	// The network comes back and the user runs another prompt.
	url, spans, mu := mockOTLPServer(t)
	s.cfg.OTLPUrl = url
	s.feed(t, map[string]any{"hook_event_name": "UserPromptSubmit", "session_id": "sess-1", "prompt": "again"})

	mu.Lock()
	defer mu.Unlock()
	require.NotEmpty(t, *spans, "the spooled span should have been sent on the next invocation")
	assert.Zero(t, spool.Len(dir), "a sent payload must not stay queued")

	// The token counts survived the round trip through disk — the whole point is
	// that the cost data arrives late rather than never.
	require.Len(t, *spans, 1)
	assert.Equal(t, "11", intAttr(t, (*spans)[0], "gen_ai.usage.input_tokens"))
	assert.Equal(t, "22", intAttr(t, (*spans)[0], "gen_ai.usage.output_tokens"))
}

// writeBreadcrumbs lays down one session's breadcrumb file, the way the hook
// registration's shell fallback does when the bootstrap is gone.
func writeBreadcrumbs(t *testing.T, dataDir, session string, body []byte) string {
	t.Helper()
	require.NoError(t, os.MkdirAll(incident.Dir(dataDir), 0o755))
	path := filepath.Join(incident.Dir(dataDir), session+".log")
	require.NoError(t, os.WriteFile(path, body, 0o644))
	return path
}

// The breadcrumb a dead hook leaves behind becomes a log record, which is the
// only way we learn that a session ran with the plugin mute.
func TestBacklog_BreadcrumbIsReportedAsALogRecord(t *testing.T) {
	url, records, mu := mockLogServer(t)
	s := newSetup(t, url)

	writeBreadcrumbs(t, s.dataDir, "sess-1", []byte(
		"2026-08-25T14:15:16Z\thook_script_missing\tcodex\t/plugins/0.1.24/codex/codex-on-event.sh\tsess-1\n"+
			"2026-08-25T14:15:20Z\thook_script_missing\tcodex\t/plugins/0.1.24/codex/codex-on-event.sh\tsess-1\n"))

	s.feed(t, map[string]any{"hook_event_name": "SessionStart", "session_id": "sess-2", "model": "gpt-5.5"})

	mu.Lock()
	defer mu.Unlock()

	var incidents []otlpLogRecord
	for _, r := range *records {
		if r.attr("dash0.plugin.incident.kind") != "" {
			incidents = append(incidents, r)
		}
	}
	require.Len(t, incidents, 1, "two identical breadcrumbs are one incident with a count")

	rec := incidents[0]
	assert.Equal(t, "WARN", rec.SeverityText)
	require.NotNil(t, rec.Body.StringValue)
	assert.Equal(t, "hook_script_missing", *rec.Body.StringValue)
	assert.Equal(t, "hook_script_missing", rec.attr("dash0.plugin.incident.kind"))
	assert.Equal(t, "2", rec.attr("dash0.plugin.incident.count"))
	assert.Equal(t, "codex", rec.attr("gen_ai.harness.name"))
	assert.Equal(t, "/plugins/0.1.24/codex/codex-on-event.sh", rec.attr("dash0.plugin.incident.detail"))
	assert.Equal(t, "2026-08-25T14:15:16Z", rec.attr("dash0.plugin.incident.first"))
	assert.Equal(t, "2026-08-25T14:15:20Z", rec.attr("dash0.plugin.incident.last"))
	assert.NotEmpty(t, rec.TraceID, "the incident correlates with the session it belongs to")

	left, err := os.ReadDir(incident.Dir(s.dataDir))
	require.NoError(t, err)
	assert.Empty(t, left, "a reported breadcrumb must not be reported again")
}

// Reporting an incident while the endpoint is still down must not lose it. The
// report is telemetry like any other, so it spools and goes out later.
func TestBacklog_BreadcrumbSurvivesAFailedReport(t *testing.T) {
	s := newSetup(t, unreachableURL(t))

	writeBreadcrumbs(t, s.dataDir, "sess-1", []byte(
		"2026-08-25T14:15:16Z\thook_script_missing\tcodex\t/gone.sh\tsess-1\n"))

	s.feed(t, map[string]any{"hook_event_name": "SessionStart", "session_id": "sess-1", "model": "gpt-5.5"})
	require.NotZero(t, spool.Len(spool.Dir(s.dataDir)), "the failed incident report must be kept")

	url, records, mu := mockLogServer(t)
	s.cfg.OTLPUrl = url
	s.feed(t, map[string]any{"hook_event_name": "UserPromptSubmit", "session_id": "sess-1", "prompt": "hi"})

	mu.Lock()
	defer mu.Unlock()
	var found bool
	for _, r := range *records {
		if r.attr("dash0.plugin.incident.kind") == "hook_script_missing" {
			found = true
		}
	}
	assert.True(t, found, "the incident should arrive once the endpoint is reachable")
}

// When the report can neither be sent nor spooled, the breadcrumbs stay. This is
// the last line of defence: they are the only evidence the plugin was mute, so
// they are not discarded until the report is somewhere durable.
func TestBacklog_BreadcrumbsSurviveWhenNothingCanBeSaved(t *testing.T) {
	s := newSetup(t, unreachableURL(t))

	body := []byte("2026-08-25T14:15:16Z\thook_script_missing\tcodex\t/gone.sh\tsess-1\n")
	path := writeBreadcrumbs(t, s.dataDir, "sess-1", body)

	// Occupy the spool's path with a file, so MkdirAll cannot create it and the
	// failed report has nowhere to go.
	require.NoError(t, os.WriteFile(spool.Dir(s.dataDir), []byte("not a directory"), 0o644))

	s.feed(t, map[string]any{"hook_event_name": "SessionStart", "session_id": "sess-1", "model": "gpt-5.5"})

	// The breadcrumbs are not at the original name — they are claimed — but they
	// are still on disk for a later invocation to adopt.
	claims, err := filepath.Glob(path + ".claimed.*")
	require.NoError(t, err)
	require.Len(t, claims, 1, "an unreportable incident must not be discarded")
	kept, err := os.ReadFile(claims[0])
	require.NoError(t, err)
	assert.Equal(t, body, kept)
}

// With no endpoint configured there is nothing to flush to, and the breadcrumbs
// must stay on disk until there is.
func TestBacklog_WithoutAnEndpointNothingIsConsumed(t *testing.T) {
	s := newSetup(t, "")

	body := []byte("2026-08-25T14:15:16Z\thook_script_missing\tcodex\t/gone.sh\tsess-1\n")
	path := writeBreadcrumbs(t, s.dataDir, "sess-1", body)

	s.feed(t, map[string]any{"hook_event_name": "SessionStart", "session_id": "sess-1", "model": "gpt-5.5"})

	got, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, body, got, "an unconfigured plugin must not discard the evidence")
}

// The drain is bounded so a large backlog cannot stall the user's hook.
func TestBacklog_DrainIsBoundedPerInvocation(t *testing.T) {
	url, _, _ := mockOTLPServer(t)
	s := newSetup(t, url)

	dir := spool.Dir(s.dataDir)
	for range backlogBatch + 10 {
		require.NoError(t, spool.Append(dir, "/v1/traces", []byte(`{"resourceSpans":[]}`)))
	}

	s.feed(t, map[string]any{"hook_event_name": "SessionStart", "session_id": "sess-1", "model": "gpt-5.5"})

	assert.Equal(t, 10, spool.Len(dir),
		"one invocation sends at most backlogBatch payloads; the rest wait for the next")
}

// A deadline that has already passed means the hook has no budget left.
func TestBacklog_SpentBudgetSendsNothing(t *testing.T) {
	url, _, _ := mockOTLPServer(t)
	s := newSetup(t, url)

	dir := spool.Dir(s.dataDir)
	require.NoError(t, spool.Append(dir, "/v1/traces", []byte(`{"resourceSpans":[]}`)))

	// now far enough in the past that now+backlogBudget is already behind us.
	_, err := Process(
		map[string]any{"hook_event_name": "SessionStart", "session_id": "sess-1", "model": "gpt-5.5"},
		s.cfg, s.dataDir, time.Now().Add(-time.Hour),
	)
	require.NoError(t, err)
	assert.Equal(t, 1, spool.Len(dir), "the payload waits for an invocation with budget")
}

// The incident report shares the backlog's budget. One report can spend the OTLP
// client's whole timeout while the endpoint is down, so an invocation with no
// budget left must hand the breadcrumbs on rather than hold the user's hook.
func TestBacklog_SpentBudgetLeavesIncidentsForLater(t *testing.T) {
	url, records, mu := mockLogServer(t)
	s := newSetup(t, url)

	path := writeBreadcrumbs(t, s.dataDir, "sess-1",
		[]byte("2026-08-25T14:15:16Z\thook_script_missing\tcodex\t/gone.sh\tsess-1\n"))

	// now far enough in the past that now+backlogBudget is already behind us.
	_, err := Process(
		map[string]any{"hook_event_name": "SessionStart", "session_id": "sess-1", "model": "gpt-5.5"},
		s.cfg, s.dataDir, time.Now().Add(-time.Hour),
	)
	require.NoError(t, err)

	mu.Lock()
	for _, r := range *records {
		assert.Empty(t, r.attr("dash0.plugin.incident.kind"), "no budget means no report")
	}
	mu.Unlock()

	claims, err := filepath.Glob(path + ".claimed.*")
	require.NoError(t, err)
	assert.Len(t, claims, 1, "the breadcrumbs wait for an invocation with budget")
}
