// SPDX-FileCopyrightText: Copyright 2026 Dash0 Inc.
// SPDX-License-Identifier: Apache-2.0

package otlp

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
)

// TraceContext holds the active trace and root span IDs for a session,
// along with session-level metadata to carry forward to child spans.
type TraceContext struct {
	TraceID   string `json:"trace_id"`
	SpanID    string `json:"span_id"`
	SessionID string `json:"session_id"`
	Model     string `json:"model,omitempty"`
}

const traceContextFile = "trace_context.json"

// SaveTraceContext persists trace context to the data directory.
func SaveTraceContext(ctx TraceContext, dataDir string) error {
	data, err := json.Marshal(ctx)
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dataDir, traceContextFile), data, 0o644)
}

// ClearTraceContext removes the persisted trace context file.
func ClearTraceContext(dataDir string) {
	_ = os.Remove(filepath.Join(dataDir, traceContextFile))
}

// LoadTraceContext reads the persisted trace context from the data directory.
// Returns nil if the file does not exist.
func LoadTraceContext(dataDir string) (*TraceContext, error) {
	return loadContextFile(filepath.Join(dataDir, traceContextFile))
}

// agentIDPattern restricts agent IDs to filename-safe characters. Agent IDs
// come from hook input and are used in file names, so anything else (path
// separators, dots) is rejected rather than sanitized.
var agentIDPattern = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)

func agentTraceContextFile(dataDir, agentID string) (string, error) {
	if !agentIDPattern.MatchString(agentID) {
		return "", fmt.Errorf("invalid agent ID %q", agentID)
	}
	return filepath.Join(dataDir, "agent_trace_context_"+agentID+".json"), nil
}

// SaveAgentTraceContext persists a per-agent snapshot of the trace context.
// Taken at SubagentStart, it pins the subagent to the turn that spawned it so
// a SubagentStop arriving after the turn's Stop (which clears the session
// context) or after the next prompt (which replaces it) still attaches to the
// right trace.
func SaveAgentTraceContext(ctx TraceContext, dataDir, agentID string) error {
	path, err := agentTraceContextFile(dataDir, agentID)
	if err != nil {
		return err
	}
	data, err := json.Marshal(ctx)
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o644)
}

// LoadAgentTraceContext reads the per-agent trace context snapshot. Returns
// nil if no snapshot exists (e.g. the agent started before the plugin was
// installed) or the agent ID is not filename-safe.
func LoadAgentTraceContext(dataDir, agentID string) (*TraceContext, error) {
	path, err := agentTraceContextFile(dataDir, agentID)
	if err != nil {
		return nil, nil
	}
	return loadContextFile(path)
}

// ClearAgentTraceContext removes the per-agent trace context snapshot.
func ClearAgentTraceContext(dataDir, agentID string) {
	if path, err := agentTraceContextFile(dataDir, agentID); err == nil {
		_ = os.Remove(path)
	}
}

func loadContextFile(path string) (*TraceContext, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var ctx TraceContext
	if err := json.Unmarshal(data, &ctx); err != nil {
		return nil, err
	}
	return &ctx, nil
}
