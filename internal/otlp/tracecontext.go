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
	// ToolModel is the model resolved from the transcript for THIS turn, cached so
	// the turn's later tool spans do not each race the transcript flush.
	//
	// It is deliberately separate from Model. Model comes from the SessionStart
	// payload and is copied forward across turns, so writing a transcript-derived
	// value there would pin the first model a session resolved onto every later
	// turn, and a mid-session model switch would be reported as the old model.
	// ToolModel is not copied forward, so it expires with the turn that wrote it.
	// Only tool spans read it; an LLM span reads its own turn's transcript.
	ToolModel string `json:"tool_model,omitempty"`
	// StartTime is set only in per-agent snapshots (written at SubagentStart)
	// and records the RFC3339Nano timestamp of the hook fire. It anchors the
	// subagent span's start so a late-arriving SubagentStop does not inherit
	// the next turn's UserPromptSubmit timestamp.
	StartTime string `json:"start_time,omitempty"`
}

const traceContextFile = "trace_context.json"

// SaveTraceContext persists trace context to the data directory.
func SaveTraceContext(ctx TraceContext, dataDir string) error {
	return writeContextFile(filepath.Join(dataDir, traceContextFile), ctx)
}

// writeContextFile serializes ctx to path atomically, by writing a temporary
// file in the same directory and renaming it over the target.
//
// A plain os.WriteFile truncates first and writes second, so a concurrent reader
// can observe an empty or half-written file. That is not theoretical here: one
// hook process runs per hook invocation, the recorded QA runs show tool-call
// hooks overlapping in time, and a reader that gets a torn file fails to parse
// its trace context and drops the span. os.Rename is atomic on POSIX, so a
// reader sees either the whole previous file or the whole new one.
func writeContextFile(path string, ctx TraceContext) error {
	data, err := json.Marshal(ctx)
	if err != nil {
		return err
	}
	// Same directory as the target, so the rename cannot cross a filesystem
	// boundary. The pid keeps concurrent writers off each other's temp file.
	tmp := fmt.Sprintf("%s.tmp.%d", path, os.Getpid())
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
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
	return writeContextFile(path, ctx)
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
