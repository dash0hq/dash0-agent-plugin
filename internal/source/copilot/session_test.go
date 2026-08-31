// SPDX-FileCopyrightText: Copyright 2026 Dash0 Inc.
// SPDX-License-Identifier: Apache-2.0

package copilot

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// session writes a Copilot session the way a run leaves one: the directory and
// events.jsonl the pipeline writes, plus the sessionStart marker beside them.
func session(t *testing.T, dataDir, id string, age time.Duration) string {
	t.Helper()
	dir := foreignSession(t, dataDir, id, age)
	MarkSessionStarted(dataDir, id)
	return dir
}

// foreignSession writes what another runtime leaves in a shared data root: the
// same directory and events.jsonl, and no marker, because only Copilot writes
// one. pipeline.Process is what writes that file, for every runtime.
func foreignSession(t *testing.T, dataDir, id string, age time.Duration) string {
	t.Helper()
	dir := filepath.Join(dataDir, id)
	require.NoError(t, os.MkdirAll(dir, 0o755))
	events := filepath.Join(dir, activityFile)
	require.NoError(t, os.WriteFile(events, []byte("{}\n"), 0o644))
	when := time.Now().Add(-age)
	require.NoError(t, os.Chtimes(events, when, when))
	return dir
}

// TestSweepOldSessionDirs covers the leak a killed run leaves behind. A session
// that ends deletes its own directory; one that is killed delivers no
// sessionEnd, and until this sweep existed nothing collected those — a
// developer's machine held 20, the oldest six weeks old, each with an
// events.jsonl holding that session's prompts.
func TestSweepOldSessionDirs(t *testing.T) {
	dataDir := t.TempDir()

	stale := session(t, dataDir, "stale", 48*time.Hour)
	fresh := session(t, dataDir, "fresh", time.Minute)
	live := session(t, dataDir, "live", 48*time.Hour) // old, but it is this session

	// The bootstrap's binary cache lives alongside the session directories and
	// must survive: it has no events.jsonl, which is the rule the sweep uses.
	binDir := filepath.Join(dataDir, "bin")
	require.NoError(t, os.MkdirAll(binDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(binDir, "copilot-on-event"), []byte("x"), 0o755))
	old := time.Now().Add(-90 * 24 * time.Hour)
	require.NoError(t, os.Chtimes(binDir, old, old))

	SweepOldSessionDirs(dataDir, "live", time.Now())

	assert.NoDirExists(t, stale, "a session untouched for longer than the TTL is swept")
	assert.DirExists(t, fresh, "a recently active session is kept")
	assert.DirExists(t, live, "the running session is never swept, however old it looks")
	assert.DirExists(t, binDir, "the binary cache carries no events.jsonl and must survive")
	assert.FileExists(t, filepath.Join(binDir, "copilot-on-event"))
}

// TestSweepOldSessionDirsSkipsTheDirsThisPluginOwns covers the case the
// events.jsonl rule does not. A malformed payload naming a reserved directory
// reaches pipeline.Process, which knows nothing of these names and appends its
// events file wherever the id points — and from then on the sweep would read
// bin/ or otel/ as an ordinary stale session and take the binary cache, or every
// live native-OTel file and every persisted cursor, with it.
func TestSweepOldSessionDirsSkipsTheDirsThisPluginOwns(t *testing.T) {
	dataDir := t.TempDir()

	owned := []string{startedDir, binDir, otelDirName}
	for _, name := range owned {
		session(t, dataDir, name, 48*time.Hour)
	}
	stale := session(t, dataDir, "stale", 48*time.Hour)

	SweepOldSessionDirs(dataDir, "live", time.Now())

	for _, name := range owned {
		assert.DirExists(t, filepath.Join(dataDir, name),
			"%s names a directory this plugin owns and is never a session's", name)
	}
	assert.NoDirExists(t, stale, "an ordinary stale session is still swept")
}

// TestSweepOldSessionDirsLeavesAnotherRuntimeAlone covers a shared data root.
//
// DASH0_PLUGIN_DATA is a documented override that Cursor, Codex and Copilot all
// resolve to, with no per-runtime subdirectory beneath it, so under it one
// directory holds every runtime's sessions. pipeline.Process writes events.jsonl
// for all of them, which makes the activity rule alone unable to tell whose
// session it is looking at: a Copilot sessionStart would delete a Codex session
// idle since yesterday, costing it its trace context and the PreToolUse its
// postToolUse reads back to time a tool span.
//
// Only Copilot writes a sessionStart marker, so that is what the sweep keys on.
func TestSweepOldSessionDirsLeavesAnotherRuntimeAlone(t *testing.T) {
	dataDir := t.TempDir()

	codex := foreignSession(t, dataDir, "codex-session", 48*time.Hour)
	mine := session(t, dataDir, "copilot-session", 48*time.Hour)

	SweepOldSessionDirs(dataDir, "live", time.Now())

	assert.DirExists(t, codex,
		"another runtime's session carries no Copilot marker and is not this sweep's to collect")
	assert.NoDirExists(t, mine, "this runtime's own stale session is still swept")
}

// TestUnreserveSessionIDLeavesAReservedNameBehind pins the two properties the
// entrypoint relies on: the result names no directory this plugin owns, and it
// is a function of the id alone, so every hook of one session agrees on it and
// the session keeps a single trace context.
func TestUnreserveSessionIDLeavesAReservedNameBehind(t *testing.T) {
	for _, id := range []string{startedDir, binDir, otelDirName} {
		got := UnreserveSessionID(id)

		assert.NotEqual(t, id, got)
		assert.False(t, ReservedSessionID(got), "the rename must not land on another reserved name")
		assert.Equal(t, got, UnreserveSessionID(id), "and it must be stable across a session's hooks")
	}
}

// TestSweepOldSessionDirsDatesBySessionActivity pins why the mtime read is the
// events file's and not the directory's. A directory's mtime changes only when
// an entry is added or removed, so it stays frozen at the first event — dating a
// long-running session by when it started, and sweeping it out from under itself.
func TestSweepOldSessionDirsDatesBySessionActivity(t *testing.T) {
	dataDir := t.TempDir()
	dir := session(t, dataDir, "long-running", time.Minute)

	// The directory itself looks ancient; the session is active.
	old := time.Now().Add(-90 * 24 * time.Hour)
	require.NoError(t, os.Chtimes(dir, old, old))

	SweepOldSessionDirs(dataDir, "someone-else", time.Now())

	assert.DirExists(t, dir, "activity is dated by events.jsonl, not by the directory's own mtime")
}

func TestSweepOldSessionDirsMissingDataDir(t *testing.T) {
	assert.NotPanics(t, func() {
		SweepOldSessionDirs(filepath.Join(t.TempDir(), "nope"), "", time.Now())
	})
}

func TestMarkAndReadSessionStarted(t *testing.T) {
	dataDir := t.TempDir()
	assert.False(t, SessionStarted(dataDir, "s"), "an unmarked session has not started")
	MarkSessionStarted(dataDir, "s")
	assert.True(t, SessionStarted(dataDir, "s"), "MarkSessionStarted creates the directory if needed")
}

// TestSessionMarkerSurvivesItsSessionDirectory is the reason the marker sits
// beside the session directories rather than in one. Two things delete that
// directory, and both did: pipeline.Process removes it on SessionEnd, and the
// sweep removes stale ones. Either taking the marker meant a turn arriving
// afterwards was read as a sub-agent's and dropped — including the last turn of
// every session, whose agentStop races sessionEnd and is the slower of the two,
// since it scans the native-OTel file.
func TestSessionMarkerSurvivesItsSessionDirectory(t *testing.T) {
	dataDir := t.TempDir()
	sessionDir := session(t, dataDir, "sess", time.Minute)
	MarkSessionStarted(dataDir, "sess")

	// SessionEnd: pipeline.Process removes the session directory wholesale.
	require.NoError(t, os.RemoveAll(sessionDir))
	assert.True(t, SessionStarted(dataDir, "sess"),
		"a turn arriving after SessionEnd must still be recognised as this session's")

	// And the sweep, which cannot tell a killed session from one idle for a day.
	stale := session(t, dataDir, "sess", 4*7*24*time.Hour)
	SweepOldSessionDirs(dataDir, "another-session", time.Now())
	assert.NoDirExists(t, stale, "the stale directory still goes")
	assert.True(t, SessionStarted(dataDir, "sess"), "its marker does not")
}

// TestSessionMarkersAreNeverSwept is the anti-regression for the decision that
// nothing reclaims a marker. Reclaiming on a timer cannot tell a session that is
// over from one merely idle, so it eventually takes a live session's marker —
// measured against the built binary: an idle session's marker went to another
// session's sweep, and from then on every turn of it was dropped and its state
// deleted, with no way back since sessionStart never fires again.
func TestSessionMarkersAreNeverSwept(t *testing.T) {
	dataDir := t.TempDir()
	MarkSessionStarted(dataDir, "idle-but-live")
	old := time.Now().Add(-52 * 7 * 24 * time.Hour)
	require.NoError(t, os.Chtimes(startedPath(dataDir, "idle-but-live"), old, old))

	// A year-old marker, and a sweep triggered by any other session.
	SweepOldSessionDirs(dataDir, "another-session", time.Now())

	assert.True(t, SessionStarted(dataDir, "idle-but-live"),
		"a marker must outlive any sweep; losing one silently ends a session's telemetry")
}
