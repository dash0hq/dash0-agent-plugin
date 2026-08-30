// SPDX-FileCopyrightText: Copyright 2026 Dash0 Inc.
// SPDX-License-Identifier: Apache-2.0

package pipeline

import (
	"os"
	"path/filepath"
	"time"

	"github.com/dash0hq/dash0-agent-plugin/internal/filelog"
)

// sessionRetention is how long a session directory may sit untouched before it
// is swept. It has to outlast a real session that is merely idle — someone
// leaves the agent open over a long weekend — and nothing else here depends on
// the exact number, so it is generous.
const sessionRetention = 7 * 24 * time.Hour

// sweepStaleSessions removes session directories nothing has touched in a week.
//
// `SessionEnd` deletes a session's directory, but not every harness has that
// event: Codex has no `SessionEnd` hook at all, so without this every Codex
// session ever run would leave its `events.jsonl` in the user's state directory
// forever. Even where the event exists, a session killed with SIGKILL never
// fires it. Users noticed the directory growing, and they were right.
//
// This runs on `SessionStart` only. It is housekeeping, not telemetry, so every
// failure is ignored: a directory that cannot be removed is retried in a week.
//
// A directory is swept only if it holds an events.jsonl, which is what makes it a
// session. That test is deliberately positive rather than a list of names to
// skip: for an installer-based Codex setup the data root is also where the
// bootstrap keeps `bin/`, so a sweep that deletes whatever it does not recognize
// would delete the plugin's own binary.
func sweepStaleSessions(dataDir string, now time.Time, keep string) {
	if dataDir == "" {
		return
	}
	entries, err := os.ReadDir(dataDir)
	if err != nil {
		return
	}
	for _, de := range entries {
		if !de.IsDir() || de.Name() == keep {
			continue
		}
		dir := filepath.Join(dataDir, de.Name())
		if _, err := os.Stat(filepath.Join(dir, filelog.FileName)); err != nil {
			continue // not a session directory
		}
		if now.Sub(lastTouched(dir)) > sessionRetention {
			_ = os.RemoveAll(dir)
		}
	}
}

// lastTouched reports when a session directory was last written to.
//
// The directory's own mtime is not enough: it changes when a file is created or
// removed, not when one is appended to, so a long session that only ever grows
// events.jsonl would look untouched since the hour it started. The newest mtime
// inside is what the retention window has to measure.
//
// A directory that cannot be read reports the zero time, which sweeps it. It is
// either empty or broken, and neither is worth keeping.
func lastTouched(dir string) time.Time {
	var newest time.Time
	if fi, err := os.Stat(dir); err == nil {
		newest = fi.ModTime()
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return newest
	}
	for _, de := range entries {
		fi, err := de.Info()
		if err == nil && fi.ModTime().After(newest) {
			newest = fi.ModTime()
		}
	}
	return newest
}
