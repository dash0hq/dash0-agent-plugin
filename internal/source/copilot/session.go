// SPDX-FileCopyrightText: Copyright 2026 Dash0 Inc.
// SPDX-License-Identifier: Apache-2.0

package copilot

import (
	"os"
	"path/filepath"
	"time"
)

// startedDir holds one empty file per session that Copilot actually started, as
// opposed to a sub-agent's.
//
// It exists because a sub-agent is not told apart by its id. Copilot gives a
// sub-agent its own hook lifecycle under its own session id, and the shape of
// that id depends on how the CLI was launched: `copilot -p` names it
// call_<toolCallId>, an interactive session gives it a plain UUID. Normalize's
// call_ guard catches the first and cannot catch the second.
//
// What does hold in both, measured against Copilot CLI 1.0.80: a sub-agent
// session receives NO sessionStart and no sessionEnd. Only the session a person
// started gets those. So the marker is written on sessionStart and read on
// agentStop.
//
// A missing marker is not on its own enough to suppress a turn — only
// sessionStart writes one, and that hook has upstream ways to fail that say
// nothing about sub-agents. The entrypoint requires the native-OTel file to
// agree, which it does for a sub-agent and not for a real session; see the Stop
// branch in cmd/copilot-on-event.
//
// Each hook is its own short-lived process, so "this session never had a
// sessionStart" is a claim about something that happened in a different process.
// A file is the only way agentStop can learn it.
//
// It lives BESIDE the per-session directories rather than inside one, because
// two other things are entitled to delete that directory and both did. Its own
// SessionEnd handling removes it, so the last turn of a session — whose agentStop
// races sessionEnd and is the slower of the two, since it scans the native-OTel
// file — found no marker and was dropped. And SweepOldSessionDirs removes stale
// ones, which cannot tell a killed session from one idle over a weekend, so a
// live session lost its marker for good: sessionStart never fires again, so the
// marker never came back and every remaining turn went with it. Out here,
// neither reaches it.
//
// Nothing ever deletes a marker. It is an empty file named by a session id, and
// the alternative — reclaiming it on a timer — cannot tell a session that is over
// from one merely idle, so it eventually takes a live session's marker and every
// remaining turn goes with it, silently and for good. That was measured, twice,
// in two different places. The suppression is fail-closed on missing state by
// construction, so the state has to be permanent: a stale empty file is a far
// smaller problem than a session that stops reporting.
//
// Read at agentStop rather than at userPromptSubmitted on purpose. Copilot fires
// sessionStart and userPromptSubmitted in a nondeterministic order, so at prompt
// time a real session may not have been marked yet; by the end of its first turn
// it always has.
const startedDir = "started"

func startedPath(dataDir, sessionID string) string {
	return filepath.Join(dataDir, startedDir, sessionID)
}

// MarkSessionStarted records that this session had a sessionStart.
//
// A failure here is not cheap and is deliberately silent anyway: with no marker,
// every later turn of this session is read as a sub-agent's and dropped. There
// is nothing useful to do about it in a hook that must exit 0 — see startedDir
// for why the suppression is fail-closed, and copilot/README.md for the one
// upstream path that can leave a real session unmarked.
func MarkSessionStarted(dataDir, sessionID string) {
	if err := os.MkdirAll(filepath.Join(dataDir, startedDir), 0o755); err != nil {
		return
	}
	_ = os.WriteFile(startedPath(dataDir, sessionID), nil, 0o644)
}

// SessionStarted reports whether this session ever received a sessionStart.
func SessionStarted(dataDir, sessionID string) bool {
	_, err := os.Stat(startedPath(dataDir, sessionID))
	return err == nil
}

// binDir is the bootstrap's binary cache, which sits beside the session
// directories under the same dataDir.
const binDir = "bin"

// ReservedSessionID reports whether a session id names something this plugin
// owns inside its data directory.
//
// pipeline.IsSafeSessionID answers only "is this a usable path segment", and
// each of these is a perfectly usable one. A Stop under any of them, in a
// session with no marker, would resolve to the real directory and the sub-agent
// suppression would remove it:
//
//   - the markers, taking every live session's and so silencing all of them;
//   - the bootstrap's binary cache, which at least re-downloads;
//   - the native-OTel directory, which in the default layout is a child of the
//     data root (DASH0_COPILOT_OTEL_DIR moves it out, and the name is reserved
//     either way) — that one takes every concurrent session's live .jsonl and
//     every persisted cursor, and a session whose file was unlinked under it
//     goes on writing to a dangling inode, so it reports no usage and no tools
//     for the rest of its life.
//
// Copilot's ids are UUIDs, so this bounds what a malformed payload can do
// rather than describing an observed failure.
func ReservedSessionID(sessionID string) bool {
	switch sessionID {
	case startedDir, binDir, otelDirName:
		return true
	}
	return false
}

// UnreserveSessionID maps a reserved session id onto one that names nothing this
// plugin owns, for the payload pipeline.Process reads.
//
// A prefix rather than a random id, because Process keys a session's trace
// context on this and every hook of the session must arrive at the same answer.
// The result is a safe path segment and is not itself reserved, since no
// reserved name starts with the prefix.
func UnreserveSessionID(sessionID string) string {
	return "session-" + sessionID
}

// activityFile is the file whose mtime dates a session directory. Every event
// the pipeline processes is appended to it, so it tracks the session's last
// activity — which the directory's own mtime does not: that changes only when an
// entry is added or removed, so it stays frozen at the first event and would
// date a long-running session by when it started.
const activityFile = "events.jsonl"

// SweepOldSessionDirs removes session directories under dataDir that nothing has
// touched for staleFileTTL, except the one named by keep.
//
// A session directory is deleted when its session ends. A run that is killed —
// SIGKILL, a crash, a closed terminal — delivers no sessionEnd, so its directory
// is never deleted and nothing else was collecting them: measured 2026-08-28, a
// developer's machine held 20, the oldest six weeks old. They are small, but
// each holds an events.jsonl containing that session's prompts, so they are user
// content sitting on disk indefinitely.
//
// Run on sessionStart, alongside SweepOldOtelFiles, which has covered the
// native-OTel directory the same way all along.
//
// Only directories carrying an events.jsonl are considered, and the names this
// plugin owns are skipped outright. Either check alone would keep the sweep off
// bin/ today; together they also hold if a malformed payload ever gets Process
// to write an events.jsonl into one of them. Best-effort throughout: a directory
// that cannot be read or removed is left alone rather than failing the hook.
//
// A sessionStart marker is required too, and that is what keeps the sweep off
// another runtime's sessions. dataDir is not necessarily Copilot's own:
// DASH0_PLUGIN_DATA is a documented override that every runtime resolves to,
// with no per-runtime subdirectory under it (see harness.DataDir and
// FEATURE_MATRIX.md), so under that setting a Copilot sessionStart reads a
// directory holding Cursor and Codex sessions as well. pipeline.Process writes
// events.jsonl for all of them, so the activity rule alone would delete an idle
// Codex session — costing it its trace context and the PreToolUse its
// postToolUse looks up to time a tool span.
//
// Only Copilot writes a marker, so requiring one confines the sweep to sessions
// this runtime started. A Copilot session whose sessionStart never ran leaks its
// directory instead of being collected, which is the same gap the suppression
// documents and errs in the direction that keeps data.
//
// A stale directory goes wholesale. That is only safe because the sessionStart
// marker is no longer inside one: sweeping a live-but-idle session's directory
// costs it a trace context, which its next prompt mints again, rather than the
// marker, which nothing would. The markers themselves are never swept — see
// startedDir.
func SweepOldSessionDirs(dataDir, keep string, now time.Time) {
	entries, err := os.ReadDir(dataDir)
	if err != nil {
		return
	}
	for _, e := range entries {
		if !e.IsDir() || e.Name() == keep || ReservedSessionID(e.Name()) {
			continue
		}
		if !SessionStarted(dataDir, e.Name()) {
			continue
		}
		dir := filepath.Join(dataDir, e.Name())
		info, err := os.Stat(filepath.Join(dir, activityFile))
		if err != nil || now.Sub(info.ModTime()) < staleFileTTL {
			continue
		}
		_ = os.RemoveAll(dir)
	}
}
