// SPDX-FileCopyrightText: Copyright 2026 Dash0 Inc.
// SPDX-License-Identifier: Apache-2.0

package hookcheck

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A hook event must never end its process non-zero. What that costs differs per
// runtime and none of it is something a user can act on mid-session: Claude and
// Copilot print a hook error on every event, Cursor reads a non-zero exit from a
// tool-gating hook as a refusal, and Codex registers this plugin on PreToolUse
// and PermissionRequest, so a raise there sits between the user and their own
// tool calls.
//
// Every failure therefore has to reach main as a returned error, which main logs
// to stderr and drops. FailOpen covers the returning half, in-process. The other
// half is that main does not turn the error back into an exit code, and an exit
// code is only observable from another process: that is
// TestBootstrapsExitZeroForTheRealBinary in test/consistency, which runs the real
// binary under the bootstrap a hook actually invokes.

// failOpenCase is one input every entrypoint must survive.
type failOpenCase struct {
	// name is the subtest name.
	name string
	// payload goes in on stdin. Empty means the session-start payload for this
	// runtime.
	payload string
	// sessionID is added to the session-start payload, so the run reaches the
	// point where it creates a directory.
	sessionID string
	// dataDir picks where the state root points: "" leaves *_PLUGIN_DATA unset so
	// the default path is taken, "temp" a writable throwaway, "unwritable" a path
	// under a directory with no write bit.
	dataDir string
	// noHome unsets every variable os.UserHomeDir reads.
	noHome bool
	// posixOnly skips the case where mode bits do not stop a write.
	posixOnly bool
	// wantErr reports whether this input must fail. A Spec, because one of them is
	// survivable for three runtimes and an error for the fourth.
	wantErr func(Spec) bool
}

var alwaysFails = func(Spec) bool { return true }

// FailOpen drives run() over the inputs that broke one of these entrypoints
// before.
//
// An error is the correct outcome for most of them, and the contract is that
// raising one costs nothing, so each case says which it expects. What this catches
// beyond that is run() panicking or exiting instead of returning.
//
// pipeline.ReadEvent rejects a null payload explicitly, so that case and the
// malformed one stop on the same line. What they pin is that the nil check stays
// there, since a nil event map reaching Normalize would panic.
func FailOpen(t *testing.T, s Spec, run Run) {
	t.Helper()

	for _, c := range []failOpenCase{
		{
			name:      "data directory left to the default",
			sessionID: "fail-open",
			// Survivable for three of the four, which fall through to
			// $HOME/.local/state. Claude requires its variable instead, so for it
			// this is a failure and the case below is the redundant one.
			wantErr: func(s Spec) bool { return s.DataDirRequired },
		},
		{
			// os.UserHomeDir is the last fallback in Harness.DataDir, so with no home
			// there is nowhere left to put the state.
			name:      "no home to derive a data directory from",
			sessionID: "fail-open",
			noHome:    true,
			wantErr:   alwaysFails,
		},
		{name: "malformed payload", payload: `not json`, dataDir: "temp", wantErr: alwaysFails},
		{name: "null payload", payload: `null`, dataDir: "temp", wantErr: alwaysFails},
		{
			name:      "unwritable session directory",
			sessionID: "fail-open",
			dataDir:   "unwritable",
			posixOnly: true,
			wantErr:   alwaysFails,
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			if c.posixOnly && runtime.GOOS == "windows" {
				t.Skip("mode bits do not stop a write on Windows")
			}
			if c.posixOnly && os.Geteuid() == 0 {
				// Root ignores the mode bits too, so the write succeeds and the case
				// measures nothing. GitHub-hosted runners are non-root; a
				// container-based one would otherwise drop this in silence.
				t.Skip("root ignores the mode bits that make this directory unwritable")
			}

			home := s.isolate(t, dataDirFor(t, c.dataDir))
			if c.noHome {
				// After isolate, so this outlives the home it points at. isolate
				// ends by emptying the configuration cache, so run() resolves the
				// configuration itself and finds no home either.
				t.Setenv("HOME", "")
				t.Setenv("USERPROFILE", "")
			}
			// No endpoint, by any route. PluginOption falls back to DASH0_OTLP_URL, so
			// one left exported had these cases export for real, and one pointing
			// nowhere stalled each of them for the connectivity timeout.
			t.Setenv(s.Harness.EnvPrefix+"_PLUGIN_OPTION_OTLP_URL", "")
			t.Setenv("DASH0_OTLP_URL", "")

			payload := c.payload
			if payload == "" {
				payload = s.SessionStart(c.sessionID)
			}
			err := s.call(t, run, payload)

			if c.wantErr(s) {
				require.Error(t, err,
					"run() accepted an input it cannot serve, so the failure is now "+
						"silent rather than fail-open")
				return
			}

			require.NoError(t, err, "this input is survivable and must not error")
			// The positive half. Returning nil is also what a run that did nothing
			// looks like, so the default state root has to be there: that is the
			// fallback under test, and reaching it means the configuration resolved
			// and run() got as far as its first write.
			assert.DirExists(t, filepath.Join(home, ".local", "state",
				"dash0-agent-plugin", s.Harness.DataSubdir),
				"nothing was written under the default state root, so this case passed "+
					"on a run that stopped before using it")
		})
	}
}

// dataDirFor resolves a case's dataDir mode to a path. "" stays empty, so the
// caller leaves *_PLUGIN_DATA unset and the default path is taken.
func dataDirFor(t *testing.T, mode string) string {
	t.Helper()

	switch mode {
	case "":
		return ""
	case "temp":
		return t.TempDir()
	case "unwritable":
		locked := t.TempDir()
		require.NoError(t, os.Chmod(locked, 0o500))
		// Restored, or t.TempDir cannot remove it during cleanup.
		t.Cleanup(func() { _ = os.Chmod(locked, 0o700) })
		return filepath.Join(locked, "data")
	default:
		require.FailNowf(t, "unknown dataDir mode", "%q", mode)
		return ""
	}
}
