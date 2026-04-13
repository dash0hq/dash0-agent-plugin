package filelog

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

const MaxEvents = 100

// WriteEvent marshals the event as JSON and appends it to events.jsonl in
// dataDir, keeping only the last MaxEvents lines.
func WriteEvent(event map[string]any, dataDir string) error {
	line, err := json.Marshal(event)
	if err != nil {
		return fmt.Errorf("marshalling JSON: %w", err)
	}

	logFile := filepath.Join(dataDir, "events.jsonl")

	// Read existing lines.
	var lines [][]byte
	if data, err := os.ReadFile(logFile); err == nil {
		for _, l := range bytes.Split(data, []byte("\n")) {
			if len(l) > 0 {
				lines = append(lines, l)
			}
		}
	}

	lines = append(lines, line)

	// Keep only the last MaxEvents lines.
	if len(lines) > MaxEvents {
		lines = lines[len(lines)-MaxEvents:]
	}

	var buf bytes.Buffer
	for _, l := range lines {
		buf.Write(l)
		buf.WriteByte('\n')
	}

	if err := os.WriteFile(logFile, buf.Bytes(), 0o644); err != nil {
		return fmt.Errorf("writing %s: %w", logFile, err)
	}

	return nil
}

// FindEvent searches events.jsonl from most recent to oldest, returning the
// first event for which the match function returns true. Returns nil if no
// match is found.
func FindEvent(dataDir string, match func(map[string]any) bool) (map[string]any, error) {
	logFile := filepath.Join(dataDir, "events.jsonl")

	data, err := os.ReadFile(logFile)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("reading %s: %w", logFile, err)
	}

	lines := bytes.Split(data, []byte("\n"))

	// Search from the end (most recent first).
	for i := len(lines) - 1; i >= 0; i-- {
		if len(lines[i]) == 0 {
			continue
		}
		var event map[string]any
		if err := json.Unmarshal(lines[i], &event); err != nil {
			continue
		}
		if match(event) {
			return event, nil
		}
	}

	return nil, nil
}
