// SPDX-FileCopyrightText: Copyright 2026 Dash0 Inc.
// SPDX-License-Identifier: Apache-2.0

package codex_test

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/dash0hq/dash0-agent-plugin/internal/otlp"
	"github.com/dash0hq/dash0-agent-plugin/internal/pipeline"
	"github.com/dash0hq/dash0-agent-plugin/internal/source/codex"
)

// Exercise the actual export path with privacy enabled, including repeated
// reads, an unrelated tool and a later turn. File attribution is event-local;
// it must neither leak into another span nor create extra chat/tool spans.
func TestSkillFilePipelineExportsOnlySuccessfulReads(t *testing.T) {
	var mu sync.Mutex
	var spans []otlp.Span
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/traces" {
			body, err := io.ReadAll(r.Body)
			assert.NoError(t, err)
			var request otlp.ExportTracesRequest
			assert.NoError(t, json.Unmarshal(body, &request))
			mu.Lock()
			for _, resource := range request.ResourceSpans {
				for _, scope := range resource.ScopeSpans {
					spans = append(spans, scope.Spans...)
				}
			}
			mu.Unlock()
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	dir := t.TempDir()
	cfg := otlp.Config{OTLPUrl: server.URL, AgentName: "codex", OmitIO: true, OmitUserInfo: true}
	now := time.Unix(123, 0)
	feed := func(event map[string]any) {
		t.Helper()
		event["session_id"] = "file-skills"
		event = codex.Normalize(event, filepath.Join(dir, "file-skills"), now)
		_, err := pipeline.Process(event, cfg, dir, now)
		require.NoError(t, err)
		now = now.Add(time.Second)
	}
	feed(map[string]any{"hook_event_name": "UserPromptSubmit", "prompt": "Private prompt"})
	for _, id := range []string{"read-1", "read-2", "unrelated"} {
		feed(map[string]any{"hook_event_name": "PreToolUse", "tool_name": "Bash", "tool_use_id": id})
		command := "cat /private/skills/collector/SKILL.md"
		if id == "unrelated" {
			command = "echo /private/skills/collector/SKILL.md"
		}
		feed(map[string]any{
			"hook_event_name": "PostToolUse", "tool_name": "Bash", "tool_use_id": id,
			"tool_input":    map[string]any{"command": command},
			"tool_response": "---\nname: collector\ndescription: Configure telemetry.\n---\nPrivate body",
		})
	}
	feed(map[string]any{"hook_event_name": "Stop"})
	feed(map[string]any{"hook_event_name": "UserPromptSubmit", "prompt": "A later turn"})
	feed(map[string]any{"hook_event_name": "Stop"})

	mu.Lock()
	defer mu.Unlock()
	require.Len(t, spans, 5, "three real tools and two turns; no fabricated spans")
	for i, span := range spans {
		attrs := map[string]otlp.AttrValue{}
		for _, a := range span.Attributes {
			attrs[a.Key] = a.Value
		}
		if i < 2 {
			assert.Equal(t, otlp.StringVal("collector"), attrs["dash0.gen_ai.tool.skill.name"])
			assert.Equal(t, otlp.StringVal("model"), attrs["dash0.gen_ai.tool.skill.source"])
			assert.Equal(t, "execute_tool Bash", span.Name)
			assert.Equal(t, spans[3].SpanID, span.ParentSpanID)
			assert.Equal(t, strconv.FormatInt(time.Unix(124+int64(i*2), 0).UnixNano(), 10), span.StartTimeUnixNano)
			assert.Equal(t, strconv.FormatInt(time.Unix(125+int64(i*2), 0).UnixNano(), 10), span.EndTimeUnixNano)
		} else {
			assert.NotContains(t, attrs, "dash0.gen_ai.tool.skill.name")
			assert.NotContains(t, attrs, "dash0.gen_ai.tool.skill.source")
		}
		if i < 3 {
			assert.Equal(t, otlp.StringVal("<REDACTED>"), attrs["gen_ai.tool.call.arguments"])
			assert.Equal(t, otlp.StringVal("<REDACTED>"), attrs["gen_ai.tool.call.result"])
		}
		assert.NotContains(t, attrs, "gen_ai.usage.input_tokens")
	}
	encoded, err := json.Marshal(spans)
	require.NoError(t, err)
	assert.NotContains(t, string(encoded), "Private")
	assert.NotContains(t, string(encoded), "/private/skills")
}
