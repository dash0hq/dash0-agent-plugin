// SPDX-FileCopyrightText: Copyright 2026 Dash0 Inc.
// SPDX-License-Identifier: Apache-2.0

//go:build !windows

package consistency

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/dash0hq/dash0-agent-plugin/test/helpers/testenv"
)

// The POSIX-only test fixtures: the shims that stand in for uname and curl, the
// fake release they serve, the runner that invokes a bootstrap the way a hook
// does, and the exec-bit assertion.
//
// Every one of them is a shell script or relies on mode bits, which is what puts
// them here rather than beside the platform-neutral fixtures in
// bootstrap_test.go. helpers_windows_test.go is the Windows counterpart, and
// declares the same requireExecutable.

// fakeUname puts a `uname` on PATH reporting the given kernel and machine, so a
// POSIX host can exercise the Windows naming logic. Git Bash reports strings like
// MINGW64_NT-10.0-26200, which is why the bootstraps normalize at all.
//
// It cannot work on Windows: Git Bash puts its own /usr/bin ahead of anything on
// PATH, so the real uname.exe answers and every case collapses to this machine.
func fakeUname(t *testing.T, kernel, machine string) {
	t.Helper()

	dir := t.TempDir()
	script := fmt.Sprintf("#!/bin/sh\ncase \"$1\" in\n  -s) echo %q ;;\n  -m) echo %q ;;\nesac\n", kernel, machine)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "uname"), []byte(script), 0o755))
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

// runBootstrap runs an agent's bootstrap with its cache pointed at dataDir and
// returns the combined output.
func runBootstrap(t *testing.T, a Agent, dataDir string, args ...string) (string, error) {
	t.Helper()

	cmd := exec.Command("bash", append([]string{abs(t, a.Bootstrap)}, args...)...)
	cmd.Stdin = strings.NewReader("{}")
	// A curl shim over an empty directory. A caller that pre-stages a cache is
	// testing that the cache is found, so the run that proves the name wrong is
	// the one that would otherwise fetch a multi-MB release. Serving nothing keeps
	// the 404 local and the failure message about the derived name.
	shimDir := fakeCurl(t, t.TempDir())
	cmd.Env = append(hookEnv(t, dataDir),
		"PATH="+shimDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// stageFakeRelease writes an asset and a matching checksums.txt into a directory
// the curl shim serves, and returns that directory with the asset's digest.
//
// The asset is a shell stub rather than a real binary: the bootstrap execs whatever
// it installed, and a stub that echoes and drains stdin proves the install ran.
//
// It reports its argv and its stdin so a caller can check what the bootstrap
// forwarded. The final line of every bootstrap is `exec "$BINARY" "$@"`, and a
// dropped "$@" leaves Copilot's event name unset, which nothing else here sees.
func stageFakeRelease(t *testing.T, asset string) (dir, digest string) {
	t.Helper()

	dir = t.TempDir()
	body := []byte("#!/bin/sh\necho STUB-RAN\necho \"STUB-ARGV:$*\"\necho \"STUB-STDIN:$(cat)\"\n")
	require.NoError(t, os.WriteFile(filepath.Join(dir, asset), body, 0o755))

	sum := sha256.Sum256(body)
	digest = hex.EncodeToString(sum[:])
	// Two spaces between digest and name, as sha256sum writes and every bootstrap
	// parses.
	require.NoError(t, os.WriteFile(filepath.Join(dir, "checksums.txt"),
		[]byte(fmt.Sprintf("%s  %s\n", digest, asset)), 0o644))
	return dir, digest
}

// fakeCurl puts a curl on PATH that serves serveDir by the URL's basename, and
// returns the PATH entry to prepend.
//
// A shim rather than the network: the bootstraps hardcode the GitHub release URL,
// so the alternative depends on a published release and skips on every version-bump
// branch, which is when this path changes.
//
// The write is slow and in two halves, so the concurrency contract has a window wide
// enough to detect an interleave rather than passing by luck.
func fakeCurl(t *testing.T, serveDir string) string {
	t.Helper()

	dir := t.TempDir()
	shim := `#!/usr/bin/env bash
# Serve $SERVE from the URL's basename. Exits 22, as curl does on a 404, when the
# file is absent, so a bootstrap's candidate fallback still works.
#
# Every URL is appended to $URLLOG when set. That is what lets a test assert on
# what the bootstrap asked for rather than on what it managed to cache.
out=""
url=""
while [ $# -gt 0 ]; do
  case "$1" in
    -o) out="$2"; shift 2 ;;
    -*) shift ;;
    *)  url="$1"; shift ;;
  esac
done
[ -z "${URLLOG:-}" ] || printf '%s\n' "$url" >>"$URLLOG"
src="$SERVE/$(basename "$url")"
[ -f "$src" ] || exit 22
if [ -z "$out" ]; then
  cat "$src"
  exit 0
fi
half=$(( ($(wc -c <"$src") + 1) / 2 ))
head -c "$half" "$src" >"$out"
sleep 0.2
tail -c +$(( half + 1 )) "$src" >>"$out"
`
	require.NoError(t, os.WriteFile(filepath.Join(dir, "curl"), []byte(shim), 0o755))
	// wget too: the bootstraps prefer curl but pick wget when curl is absent, and
	// a host with only wget must not silently reach the real network.
	require.NoError(t, os.WriteFile(filepath.Join(dir, "wget"),
		[]byte("#!/bin/sh\necho \"wget: no network in this test\" >&2\nexit 1\n"), 0o755))
	return dir
}

// requireExecutable asserts a file the runtime execs carries the bit that lets
// it. A non-executable bootstrap makes every hook fire fail silently.
//
// See the Windows twin for why this is per platform rather than a branch.
func requireExecutable(t *testing.T, path, what string) {
	t.Helper()
	info, err := os.Stat(path)
	require.NoError(t, err, "%s does not resolve to a file", what)
	assert.NotZero(t, info.Mode()&0o111, "%s must be executable", what)
}

// hookEnv is the environment a shell bootstrap runs under: PATH, a throwaway
// home, and every plugin-data variable that decides where the cache goes, all
// pointed at dataDir.
//
// The home is thrown away for the fallback rather than for a config file, since
// no bootstrap reads dash0-agent-plugin.local.md. Each falls back to
// ${XDG_STATE_HOME:-$HOME/.local/state}/dash0-agent-plugin/<agent>, so without a
// temp home a run that ignored the variables below would find the developer's
// real cache.
//
// Codex also reads a bare PLUGIN_DATA, which it sets itself for a marketplace
// install, so that is set here too. DASH0_PLUGIN_DATA outranks it, so omitting
// it was harmless only by accident of precedence.
//
// Unix-only. psEnv subtracts from the real environment instead, because a 5.1
// child needs more of a Windows environment than this names.
func hookEnv(t *testing.T, dataDir string) []string {
	t.Helper()

	home := filepath.Join(t.TempDir(), "home")
	require.NoError(t, os.MkdirAll(home, 0o755))
	return append(testenv.Home(home),
		"PATH="+os.Getenv("PATH"),
		"CLAUDE_PLUGIN_DATA="+dataDir,
		"DASH0_PLUGIN_DATA="+dataDir,
		"COPILOT_PLUGIN_DATA="+dataDir,
		"PLUGIN_DATA="+dataDir,
	)
}
