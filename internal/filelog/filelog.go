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

// FileName is the event log inside a session directory.
const FileName = "events.jsonl"

// MaxEvents is how many events the log keeps. Only the recent ones are ever
// read — FindEvent searches backwards for the current turn's prompt — so older
// lines are dead weight that a long session would otherwise carry forever.
const MaxEvents = 100

// trimSlack is how far past MaxEvents the log may run before it is rewritten.
// Trimming on every write would rewrite the file once per hook event; this makes
// it once per trimSlack events instead, and the cost of the slack is a few dozen
// lines on disk.
const trimSlack = 50

// WriteEvent marshals the event as JSON and appends it to events.jsonl in
// dataDir. Uses O_APPEND for atomic appends — safe under concurrent writers.
//
// The log is trimmed to the most recent MaxEvents once it runs trimSlack past
// them. A trim failure is not returned: the event is already written, and a log
// that is longer than intended is not worth failing a hook over.
func WriteEvent(event map[string]any, dataDir string) error {
	line, err := json.Marshal(event)
	if err != nil {
		return fmt.Errorf("marshalling JSON: %w", err)
	}
	line = append(line, '\n')

	logFile := filepath.Join(dataDir, FileName)

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

	trim(logFile)
	return nil
}

// trim rewrites the log with only its most recent MaxEvents lines, once it has
// grown trimSlack past them.
//
// Rewrite by temp and rename, so a concurrent reader sees either the old file or
// the new one and never a half-written log. A concurrent writer is the one case
// this cannot make safe: an append that lands on the old file between the read
// and the rename is lost with it. That costs at most one event out of a log only
// used to correlate the current turn, the window is a single rename, and it opens
// once every trimSlack events — cheaper than serializing every hook on a lock.
func trim(logFile string) {
	data, err := os.ReadFile(logFile)
	if err != nil {
		return
	}
	lines := bytes.Split(data, []byte("\n"))
	// A trailing newline yields an empty final element that is not an event.
	if len(lines) > 0 && len(lines[len(lines)-1]) == 0 {
		lines = lines[:len(lines)-1]
	}
	if len(lines) <= MaxEvents+trimSlack {
		return
	}

	kept := bytes.Join(lines[len(lines)-MaxEvents:], []byte("\n"))
	kept = append(kept, '\n')

	tmp := logFile + ".tmp"
	if err := os.WriteFile(tmp, kept, 0o644); err != nil {
		return
	}
	if err := os.Rename(tmp, logFile); err != nil {
		_ = os.Remove(tmp)
	}
}

// FindEvent searches events.jsonl from most recent to oldest, returning the
// first event for which the match function returns true. Returns nil if no
// match is found.
func FindEvent(dataDir string, match func(map[string]any) bool) (map[string]any, error) {
	logFile := filepath.Join(dataDir, FileName)

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
