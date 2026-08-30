// SPDX-FileCopyrightText: Copyright 2026 Dash0 Inc.
// SPDX-License-Identifier: Apache-2.0

package pipeline

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/dash0hq/dash0-agent-plugin/internal/incident"
	"github.com/dash0hq/dash0-agent-plugin/internal/spool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// session lays down a session directory holding an events.jsonl of a given age.
func session(t *testing.T, dataDir, id string, age time.Duration) string {
	t.Helper()
	dir := filepath.Join(dataDir, id)
	require.NoError(t, os.MkdirAll(dir, 0o755))
	events := filepath.Join(dir, "events.jsonl")
	require.NoError(t, os.WriteFile(events, []byte("{}\n"), 0o644))

	stamp := time.Now().Add(-age)
	require.NoError(t, os.Chtimes(events, stamp, stamp))
	require.NoError(t, os.Chtimes(dir, stamp, stamp))
	return dir
}

// Codex has no SessionEnd hook, so nothing ever deleted a session directory
// there. Every session the user ever ran kept its events.jsonl in the state
// directory forever.
func TestSweepRemovesStaleSessions(t *testing.T) {
	dataDir := t.TempDir()
	stale := session(t, dataDir, "sess-old", sessionRetention+time.Hour)
	recent := session(t, dataDir, "sess-recent", sessionRetention-time.Hour)

	sweepStaleSessions(dataDir, time.Now(), "sess-current")

	assert.NoDirExists(t, stale, "a session untouched for longer than the retention is swept")
	assert.DirExists(t, recent, "a session inside the retention window is left alone")
}

// Nothing but a session directory may be swept, however old it looks.
//
// For an installer-based Codex setup the data root is the same directory the
// bootstrap keeps the plugin binary in — a downloaded binary is never written
// again, so it is always older than the retention window. A sweep that deleted
// whatever it did not recognize would delete the plugin.
func TestSweepOnlyTouchesSessionDirectories(t *testing.T) {
	dataDir := t.TempDir()

	// bin/ holds the binary the bootstrap execs; the other two are the state
	// that survives a dead endpoint, and both bound themselves already.
	for _, dir := range []string{
		filepath.Join(dataDir, "bin"),
		spool.Dir(dataDir),
		incident.Dir(dataDir),
	} {
		require.NoError(t, os.MkdirAll(dir, 0o755))
		file := filepath.Join(dir, "kept")
		require.NoError(t, os.WriteFile(file, []byte("x"), 0o644))
		stamp := time.Now().Add(-10 * sessionRetention)
		require.NoError(t, os.Chtimes(file, stamp, stamp))
		require.NoError(t, os.Chtimes(dir, stamp, stamp))
	}

	sweepStaleSessions(dataDir, time.Now(), "sess-current")

	assert.FileExists(t, filepath.Join(dataDir, "bin", "kept"), "the plugin binary must survive")
	assert.FileExists(t, filepath.Join(spool.Dir(dataDir), "kept"))
	assert.FileExists(t, filepath.Join(incident.Dir(dataDir), "kept"))
}

// The live session is in use no matter what its timestamps say.
func TestSweepKeepsTheCurrentSession(t *testing.T) {
	dataDir := t.TempDir()
	current := session(t, dataDir, "sess-current", 10*sessionRetention)

	sweepStaleSessions(dataDir, time.Now(), "sess-current")

	assert.DirExists(t, current)
}

// A long session only ever appends to events.jsonl, which leaves the directory's
// own mtime at the hour it started. Judging age by that alone would sweep a
// session that is still running.
func TestSweepMeasuresTheNewestFileNotTheDirectory(t *testing.T) {
	dataDir := t.TempDir()
	dir := session(t, dataDir, "sess-long", 10*sessionRetention)

	// The session is still writing: the log is fresh, the directory is not.
	now := time.Now()
	require.NoError(t, os.Chtimes(filepath.Join(dir, "events.jsonl"), now, now))

	sweepStaleSessions(dataDir, now, "sess-other")

	assert.DirExists(t, dir, "a session that is still appending must survive")
}

// SessionStart is where the sweep runs, so a real event has to trigger it.
func TestProcess_SessionStartSweepsStaleSessions(t *testing.T) {
	url, _, _ := mockOTLPServer(t)
	s := newSetup(t, url)
	stale := session(t, s.dataDir, "sess-old", sessionRetention+time.Hour)

	s.feed(t, map[string]any{"hook_event_name": "SessionStart", "session_id": "sess-1", "model": "gpt-5.5"})

	assert.NoDirExists(t, stale)
}
