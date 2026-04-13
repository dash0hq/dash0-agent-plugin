package otlp

import (
	"encoding/json"
	"os"
	"path/filepath"
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

// LoadTraceContext reads the persisted trace context from the data directory.
// Returns nil if the file does not exist.
func LoadTraceContext(dataDir string) (*TraceContext, error) {
	data, err := os.ReadFile(filepath.Join(dataDir, traceContextFile))
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
