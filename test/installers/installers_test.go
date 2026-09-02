// SPDX-FileCopyrightText: Copyright 2026 Dash0 Inc.
// SPDX-License-Identifier: Apache-2.0

// Package installers drives the cursor and codex install/uninstall scripts as
// subprocesses and asserts what they leave on disk. The merge into
// ~/.cursor/hooks.json and the strip of the managed block in ~/.codex/config.toml
// live in those scripts and have no Go caller.
//
// Each script exists twice: the .sh pair merges with jq and awk, the .ps1 pair
// with ConvertFrom-Json and a line filter. Two implementations, so a fix in one is
// not a fix in the other, which is why the tests run whichever flavour the host
// platform uses. The assertions are shared; only the interpreter, the install
// paths and the hook command differ.
//
// test/marketplaces runs the other direction: there an agent's own CLI installs
// the plugin. Claude Code and Copilot ship only that way, so neither appears here.
//
// Every run is offline and hermetic: a throwaway home, the binary pre-staged at
// the version-pinned path the installer resolves, and DASH0_SOURCE_DIR pointing
// the plugin files at this checkout.
package installers

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/dash0hq/dash0-agent-plugin/test/helpers/testenv"
)

// scriptExt is the installer flavour this platform ships. Git Bash could run the
// .sh on Windows, but no Windows user reaches it.
func scriptExt() string {
	if runtime.GOOS == "windows" {
		return ".ps1"
	}
	return ".sh"
}

// unreachableOTLP makes the installers take the branch that writes the config
// file. Nothing listens on it: their connectivity check only warns, and these
// contracts assert the config merge.
const unreachableOTLP = "http://127.0.0.1:1"

// installerEnv builds the environment for an installer run: temp directories for
// the home and XDG_STATE_HOME, and a pinned DASH0_VERSION so the installer never
// queries the GitHub releases API.
func installerEnv(home, state, version string, extra ...string) []string {
	env := testenv.CleanHome(home,
		"XDG_STATE_HOME="+state,
		"DASH0_VERSION="+version,
		"DASH0_OTLP_URL="+unreachableOTLP,
		"DASH0_AUTH_TOKEN=contract-token",
		// Every prompted value is supplied, so no installer reaches its interactive
		// branch. The shell pair fails on the /dev/tty open; the PowerShell pair
		// would sit on a Read-Host until the test timed out.
		"DASH0_DATASET=contract-ds",
		"DASH0_TEAM_NAME=contract-team",
	)
	return append(env, extra...)
}

// requireConfigCarriesTheCredentials reads the file the installer wrote and
// checks the values installerEnv supplied arrived in it.
//
// FileExists alone is not enough. Every prompted value is passed as an
// environment variable, and an installer that dropped one, or that fell through
// to its own default, still writes a file: the hook then resolves a token that
// is empty or somebody else's, the collector answers 401, and every hook still
// exits 0. test/helpers/hookcheck covers a token reaching the wire once it is in
// the file; this covers it getting into the file.
func requireConfigCarriesTheCredentials(t *testing.T, path string) {
	t.Helper()

	body, err := os.ReadFile(path)
	require.NoError(t, err, "installer did not write the config file the bootstrap parses")

	for _, want := range []string{
		`auth_token: "contract-token"`,
		`otlp_url: "` + unreachableOTLP + `"`,
		`dataset: "contract-ds"`,
		`team_name: "contract-team"`,
	} {
		assert.Contains(t, string(body), want,
			"the config file is missing %s, so the value passed to the installer did "+
				"not reach the file the hook reads:\n%s", want, body)
	}
}

// runInstall runs one agent's installer. Pass the base name, e.g. "install-codex".
func runInstall(t *testing.T, pluginDir, base string, env []string) {
	t.Helper()
	runScript(t, pluginDir, base+scriptExt(), env)
}

// runUninstall runs one agent's uninstaller with the confirmation already given.
// The flavours spell it differently (`--yes` against `-Yes`), so the callers do
// not name it.
func runUninstall(t *testing.T, pluginDir, base string, env []string) {
	t.Helper()
	yes := "--yes"
	if runtime.GOOS == "windows" {
		yes = "-Yes"
	}
	runScript(t, pluginDir, base+scriptExt(), env, yes)
}

// runScript executes a repo script through the interpreter its extension implies
// and fails the test on a non-zero exit. The output is logged either way: it is
// the first thing to read when an assertion below fails.
func runScript(t *testing.T, pluginDir, script string, env []string, args ...string) {
	t.Helper()

	argv := append([]string{filepath.Join(pluginDir, script)}, args...)
	interpreter := "bash"
	if strings.HasSuffix(script, ".ps1") {
		// -NoProfile so a developer's own profile cannot change what is under test,
		// -ExecutionPolicy Bypass because a checked-out script carries no signature.
		interpreter = "powershell"
		argv = append([]string{"-NoProfile", "-ExecutionPolicy", "Bypass", "-File"}, argv...)
	}

	cmd := exec.Command(interpreter, argv...)
	cmd.Env = env
	// A clean cwd, so a project-local .cursor/ or .codex/ cannot shadow the global
	// config the installer writes.
	cmd.Dir = t.TempDir()

	out, err := cmd.CombinedOutput()
	t.Logf("%s (err=%v):\n%s", script, err, out)
	require.NoError(t, err, "%s failed", script)
}

// bootstrapName is the bootstrap filename an installer writes, e.g.
// "codex-on-event.sh". It also appears verbatim in the registered hook command,
// which is how the uninstall assertions recognize a Dash0 entry.
func bootstrapName(agent string) string {
	return agent + "-on-event" + scriptExt()
}

// versionLine is how a bootstrap of this flavour declares the release it pins.
// Both tests assert on it: the bootstrap's path carries no version, so this line
// is the only thing that separates a fresh write from one an older release left
// behind, and a reused one pins the plugin to the old binary while the install
// reports success.
func versionLine(version string) string {
	if runtime.GOOS == "windows" {
		return "$Version = '" + version + "'"
	}
	return `VERSION="` + version + `"`
}

// staleBootstrap is what an older release would have left on disk. Seeding it
// gives the version assertion something to catch: from a clean directory the
// file is always freshly written and the check proves nothing.
func staleBootstrap() string {
	shebang := "#!/usr/bin/env bash\n"
	if runtime.GOOS == "windows" {
		shebang = "# a bootstrap from an older release\n"
	}
	return shebang + versionLine("0.0.1-stale") + "\n"
}

// readFile reads a file the installer produced, failing with the path on error.
func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	require.NoError(t, err, "reading %s", path)
	return string(b)
}

// requireNotExists asserts the uninstaller removed a path completely.
func requireNotExists(t *testing.T, path string) {
	t.Helper()
	_, err := os.Stat(path)
	require.True(t, os.IsNotExist(err), "uninstaller left behind: %s", path)
}

// requireCommand fails the test when a tool the script needs is absent. The make
// target and the CI job both provide the tools, so a skip would report green
// while proving nothing.
func requireCommand(t *testing.T, name, install string) {
	t.Helper()
	_, err := exec.LookPath(name)
	require.NoError(t, err, "%s is required by the installer under test; install with: %s", name, install)
}

// anyContains reports whether any element of list contains sub.
func anyContains(list []string, sub string) bool {
	for _, s := range list {
		if strings.Contains(s, sub) {
			return true
		}
	}
	return false
}
