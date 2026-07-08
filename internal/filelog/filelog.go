// SPDX-FileCopyrightText: Copyright 2026 Dash0 Inc.
// SPDX-License-Identifier: Apache-2.0

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
// dataDir. Uses O_APPEND for atomic appends — safe under concurrent writers.
func WriteEvent(event map[string]any, dataDir string) error {
	line, err := json.Marshal(event)
	if err != nil {
		return fmt.Errorf("marshalling JSON: %w", err)
	}
	line = append(line, '\n')

	logFile := filepath.Join(dataDir, "events.jsonl")

	f, err := os.OpenFile(logFile, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("opening %s: %w", logFile, err)
	}
	_, writeErr := f.Write(line)
	closeErr := f.Close()
	if writeErr != nil {
		return fmt.Errorf("writing %s: %w", logFile, writeErr)
	}
	if closeErr != nil {
		return fmt.Errorf("closing %s: %w", logFile, closeErr)
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
