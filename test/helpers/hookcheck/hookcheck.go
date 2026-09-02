// SPDX-FileCopyrightText: Copyright 2026 Dash0 Inc.
// SPDX-License-Identifier: Apache-2.0

// Package hookcheck drives one cmd/*-on-event entrypoint through the contracts
// all four share, so each of them is asserted once rather than four times.
//
// It lives here rather than in test/consistency because run() and main() are
// unexported members of a package main, which nothing can import. The contracts
// therefore run from inside each entrypoint's own package, which passes its run
// as a func value; everything else is here.
//
// In-process on purpose. A run() called directly needs no `go build` and no
// spawned binary, and its returned error is the thing under test: fail-open means
// a failure has to arrive at main as a value, not as an exit code.
package hookcheck

import (
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/dash0hq/dash0-agent-plugin/internal/harness"
)

// Run is an entrypoint's run function. Each cmd package's test passes its own.
type Run func() error

// Spec is what differs between the four entrypoints. One value per runtime,
// below, so a contract reads the same for all of them.
type Spec struct {
	// Label is the cmd/<Label>-on-event directory.
	Label string
	// Harness is the constant the entrypoint uses, which carries EnvPrefix,
	// ConfigDir and DataSubdir.
	Harness harness.Harness
	// ArgvEvent is the event name the entrypoint reads from os.Args rather than
	// from the payload. Copilot only: its payload carries camelCase keys and no
	// hook_event_name.
	ArgvEvent string
	// PayloadEvent is the hook_event_name a session-start payload must carry,
	// empty for the runtime that takes it from argv.
	PayloadEvent string
	// DataDirRequired is true only for Claude, which treats a missing
	// CLAUDE_PLUGIN_DATA as an error rather than falling through to
	// $HOME/.local/state.
	DataDirRequired bool
}

// The four entrypoints. Harness comes from internal/harness rather than being
// restated, so EnvPrefix and ConfigDir cannot drift from what ships.
var (
	Claude = Spec{
		Label:   "claude",
		Harness: harness.Claude,
		// Claude Code always supplies CLAUDE_PLUGIN_DATA, so falling back would
		// hide a broken install.
		DataDirRequired: true,
		PayloadEvent:    "SessionStart",
	}
	Cursor = Spec{
		Label:        "cursor",
		Harness:      harness.Cursor,
		PayloadEvent: "sessionStart",
	}
	Codex = Spec{
		Label:        "codex",
		Harness:      harness.Codex,
		PayloadEvent: "SessionStart",
	}
	Copilot = Spec{
		Label:   "copilot",
		Harness: harness.Copilot,
		// The event reaches this entrypoint through argv, so PayloadEvent stays
		// empty and a payload carries only the fields under test.
		ArgvEvent: "sessionStart",
	}
)

// Specs is every entrypoint's Spec, by the label naming its cmd directory. For a
// caller that iterates runtimes from its own table; see
// TestBootstrapsExitZeroForTheRealBinary.
var Specs = map[string]Spec{
	"claude":  Claude,
	"cursor":  Cursor,
	"codex":   Codex,
	"copilot": Copilot,
}

// SessionStart is the stdin payload that makes this entrypoint treat the event as
// a session start.
func (s Spec) SessionStart(sessionID string) string {
	var fields []string
	if s.PayloadEvent != "" {
		fields = append(fields, fmt.Sprintf(`"hook_event_name":%q`, s.PayloadEvent))
	}
	if sessionID != "" {
		fields = append(fields, fmt.Sprintf(`"session_id":%q`, sessionID))
	}
	return "{" + strings.Join(fields, ",") + "}"
}

// isolate points this process at a throwaway home and at dataDir, and returns the
// home. An empty dataDir leaves *_PLUGIN_DATA unset, so the default path is taken.
//
// A home per subtest, so a fixture one case writes cannot decide another. It stays
// inside a test rather than moving up into a TestMain, which may build a binary
// under the real home: a Go toolchain resolved through $HOME (asdf, mise) cannot
// run once this is moved.
func (s Spec) isolate(t *testing.T, dataDir string) string {
	t.Helper()

	home := t.TempDir()
	t.Setenv("HOME", home)
	// USERPROFILE too: os.UserHomeDir reads it on Windows, so setting only HOME
	// leaves the case reading the developer's real home.
	t.Setenv("USERPROFILE", home)

	// XDG_STATE_HOME outranks the home in Harness.DataDir, so one left exported
	// would move the default path somewhere these contracts never look.
	t.Setenv("XDG_STATE_HOME", "")
	t.Setenv("DASH0_PLUGIN_DATA", "")
	t.Setenv(s.Harness.EnvPrefix+"_PLUGIN_DATA", dataDir)

	// Harness.configFile reads a RELATIVE <ConfigDir>/… before the one under the
	// home, so a run from the entrypoint's own directory would consult the
	// checkout. t.Chdir restores the previous directory when the subtest ends.
	t.Chdir(home)

	// One hook process resolves its configuration once, so the cache has to go
	// between cases or the first answer stands for all of them.
	harness.ResetConfig()
	return home
}

// call runs the entrypoint with payload on stdin and the runtime's own argv.
func (s Spec) call(t *testing.T, run Run, payload string) error {
	t.Helper()

	if s.ArgvEvent != "" {
		// run() reads os.Args itself, so the argv a hook would receive has to be
		// in place rather than passed.
		old := os.Args
		t.Cleanup(func() { os.Args = old })
		os.Args = []string{s.Label + "-on-event", s.ArgvEvent}
	}

	oldStdin := os.Stdin
	t.Cleanup(func() { os.Stdin = oldStdin })

	r, w, err := os.Pipe()
	require.NoError(t, err)
	os.Stdin = r
	go func() {
		_, _ = w.WriteString(payload)
		_ = w.Close()
	}()

	return run()
}
