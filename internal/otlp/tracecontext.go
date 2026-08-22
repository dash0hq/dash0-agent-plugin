// SPDX-FileCopyrightText: Copyright 2026 Dash0 Inc.
// SPDX-License-Identifier: Apache-2.0

package otlp

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// TraceContext holds the active trace and root span IDs for a session,
// along with session-level metadata to carry forward to child spans.
type TraceContext struct {
	TraceID   string `json:"trace_id"`
	SpanID    string `json:"span_id"`
	SessionID string `json:"session_id"`
	Model     string `json:"model,omitempty"`
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
	return writeFileAtomically(path, data)
}

func writeFileAtomically(path string, data []byte) error {
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
	// Agent IDs identify one invocation. A consumed marker is deliberately not
	// cleared here: unexpected ID reuse must stay fail-closed so a stale hook
	// cannot attach to a later invocation's snapshot.
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

func agentTraceContextConsumedFile(dataDir, agentID string) (string, error) {
	if !agentIDPattern.MatchString(agentID) {
		return "", fmt.Errorf("invalid agent ID %q", agentID)
	}
	return filepath.Join(dataDir, "agent_trace_context_"+agentID+".consumed"), nil
}

// MarkAgentTraceContextConsumed records that SubagentStop was observed for an
// agent. A later tool hook with no snapshot must then fail closed instead of
// falling back to whichever session turn is current.
func MarkAgentTraceContextConsumed(dataDir, agentID string) error {
	path, err := agentTraceContextConsumedFile(dataDir, agentID)
	if err != nil {
		return err
	}
	return writeFileAtomically(path, nil)
}

// AgentTraceContextConsumed reports whether SubagentStop has consumed the
// agent's snapshot. Invalid agent IDs return an error so callers can fail
// closed rather than using them to bypass the marker.
func AgentTraceContextConsumed(dataDir, agentID string) (bool, error) {
	path, err := agentTraceContextConsumedFile(dataDir, agentID)
	if err != nil {
		return false, err
	}
	_, err = os.Stat(path)
	if err == nil {
		return true, nil
	}
	if os.IsNotExist(err) {
		return false, nil
	}
	return false, err
}

var traceIDPattern = regexp.MustCompile(`^[A-Fa-f0-9]{32}$`)

func toolModelFile(dataDir, traceID, actorID string) (string, error) {
	if !traceIDPattern.MatchString(traceID) {
		return "", fmt.Errorf("invalid trace ID %q", traceID)
	}
	actor := "main"
	if actorID != "" {
		if !agentIDPattern.MatchString(actorID) {
			return "", fmt.Errorf("invalid actor ID %q", actorID)
		}
		actor = "agent_" + actorID
	}
	return filepath.Join(dataDir, "tool_model_"+traceID+"_"+actor+".json"), nil
}

type toolModel struct {
	Model string `json:"model"`
}

// SaveToolModel caches a transcript-resolved model under the turn's trace ID
// and actor ID. The empty actor identifies the main turn; a subagent uses its
// agent ID so actors with different transcripts cannot contaminate each other.
// It never rewrites trace_context.json, so a late writer cannot restore or
// replace another turn's context.
func SaveToolModel(dataDir, traceID, actorID, model string) error {
	path, err := toolModelFile(dataDir, traceID, actorID)
	if err != nil {
		return err
	}
	data, err := json.Marshal(toolModel{Model: model})
	if err != nil {
		return err
	}
	return writeFileAtomically(path, data)
}

// LoadToolModel reads the model cached for one trace actor. A missing cache
// returns an empty model.
func LoadToolModel(dataDir, traceID, actorID string) (string, error) {
	path, err := toolModelFile(dataDir, traceID, actorID)
	if err != nil {
		return "", err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil
		}
		return "", err
	}
	var cached toolModel
	if err := json.Unmarshal(data, &cached); err != nil {
		return "", err
	}
	return cached.Model, nil
}

// ClearToolModel removes one actor's cache for a turn.
func ClearToolModel(dataDir, traceID, actorID string) {
	if path, err := toolModelFile(dataDir, traceID, actorID); err == nil {
		_ = os.Remove(path)
	}
}

// ClearToolModels removes every actor cache for a completed or superseded turn.
func ClearToolModels(dataDir, traceID string) {
	if !traceIDPattern.MatchString(traceID) {
		return
	}
	entries, err := os.ReadDir(dataDir)
	if err != nil {
		return
	}
	prefix := "tool_model_" + traceID + "_"
	for _, entry := range entries {
		name := entry.Name()
		if !entry.IsDir() && strings.HasPrefix(name, prefix) && strings.HasSuffix(name, ".json") {
			_ = os.Remove(filepath.Join(dataDir, name))
		}
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
