// SPDX-FileCopyrightText: Copyright 2026 Dash0 Inc.
// SPDX-License-Identifier: Apache-2.0

//go:build marketplace

// Package marketplaces drives a real agent CLI through `plugin marketplace add`
// and `plugin install` against this checkout. One file per agent, because each
// CLI has its own marketplace schema, install verb and install layout.
//
// Cursor is absent because of its CLI. `cursor-agent plugin` has no install verb,
// `add` takes a git URL with no local-source variant, and `list` reports
// marketplaces "visible to this account", so driving it needs network, auth and a
// write to the developer's real Cursor account. Cursor also ships no marketplace
// manifest here; it installs through install-cursor.sh, which test/installers
// covers.
//
// This package sits between the unit suite and test/e2e. It needs the agent's own
// CLI on PATH, because only that CLI proves the marketplace manifest resolves:
// test/consistency proves the JSON is well formed, and a source that looked valid
// yet was never indexed is the failure that got past those checks. It runs no
// turn, so it needs no API key and no fork guard.
//
// The build tag keeps `make test` runnable with nothing installed. Run this with
// `make test-marketplaces`.
package marketplaces

import (
	"os"
	"os/exec"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/dash0hq/dash0-agent-plugin/test/helpers/pluginrepo"
	"github.com/dash0hq/dash0-agent-plugin/test/helpers/testenv"
)

// throwawayHome returns a temp directory to point an agent CLI's home at, removed
// on a BEST EFFORT basis when the test ends.
//
// Deliberately not t.TempDir(), whose cleanup is a RemoveAll that FAILS the test
// when it errors. What lives here is written by an agent CLI, and an install keeps
// writing after the command returns: `claude plugin install` raced that RemoveAll
// to "unlinkat .../cache/claude-plugins-official/dash0/0.1.24: directory not
// empty" and turned an install this test had already asserted succeeded into a red
// job. RemoveAll walks a directory, then unlinks it, so a file landing in between
// is all it takes.
//
// Retried once, because the writer is usually done a moment later, and never the
// verdict: the runner discards its temp root anyway, and a developer's is
// reclaimed by the OS. What this package tests is whether a marketplace resolves,
// not whether a CLI stops writing on cue.
func throwawayHome(t *testing.T) string {
	t.Helper()

	home, err := os.MkdirTemp("", "agent-home")
	require.NoError(t, err, "creating a throwaway home")
	t.Cleanup(func() {
		if os.RemoveAll(home) == nil {
			return
		}
		time.Sleep(time.Second)
		if err := os.RemoveAll(home); err != nil {
			t.Logf("could not remove the throwaway home %s: %v", home, err)
		}
	})
	return home
}

// agentCLI resolves one agent's CLI and returns a runner bound to a throwaway
// home directory, so a test never touches the developer's real ~/.codex or
// ~/.copilot.
//
// It FAILS rather than skips when the CLI is absent. A skip reports green while
// proving nothing, and the target that runs this installs the CLIs first.
func agentCLI(t *testing.T, name, homeEnv, install string) (run func(args ...string) (string, error), home string) {
	t.Helper()

	bin, err := pluginrepo.LookAgent(t, name)
	require.NoError(t, err, "%s CLI not found on PATH; install with: %s", name, install)

	home = throwawayHome(t)
	// Clean drops the plugin's own configuration variables. No turn runs here,
	// but an ambient <PREFIX>_PLUGIN_DATA would move the install's state root out
	// of the throwaway home.
	env := testenv.Clean(homeEnv + "=" + home)

	return func(args ...string) (string, error) {
		cmd := exec.Command(bin, args...)
		cmd.Env = env
		out, err := cmd.CombinedOutput()
		return string(out), err
	}, home
}
