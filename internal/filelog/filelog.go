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
