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

// The POSIX-only fixtures: shims standing in for uname and curl, the fake release
// they serve, the runner that invokes a bootstrap the way a hook does, and the
// exec-bit assertion. Each is a shell script or leans on mode bits, which is what
// keeps them out of bootstrap_test.go. helpers_windows_test.go is the counterpart.

// A `uname` on PATH reporting the given kernel and machine, so a POSIX host can
// exercise the Windows naming logic: Git Bash reports strings like
// MINGW64_NT-10.0-26200, which is why the bootstraps normalize at all.
//
// It cannot work on Windows, where Git Bash puts its own /usr/bin ahead of PATH,
// so the real uname.exe answers and every case collapses to this machine.
func fakeUname(t *testing.T, kernel, machine string) {
	t.Helper()

	dir := t.TempDir()
	script := fmt.Sprintf("#!/bin/sh\ncase \"$1\" in\n  -s) echo %q ;;\n  -m) echo %q ;;\nesac\n", kernel, machine)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "uname"), []byte(script), 0o755))
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

// An agent's bootstrap with its cache pointed at dataDir.
func runBootstrap(t *testing.T, a Agent, dataDir string, args ...string) (string, error) {
	t.Helper()

	cmd := exec.Command("bash", append([]string{abs(t, a.Bootstrap)}, args...)...)
	cmd.Stdin = strings.NewReader("{}")
	// A curl shim over an empty directory: callers here pre-stage a cache, so the
	// run that proves the derived name wrong is the one that would otherwise fetch
	// a multi-MB release.
	shimDir := fakeCurl(t, t.TempDir())
	cmd.Env = append(hookEnv(t, dataDir),
		"PATH="+shimDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// An asset and a matching checksums.txt in a directory the curl shim serves, plus
// the asset's digest.
//
// A shell stub rather than a real binary, since the bootstrap execs whatever it
// installed. It echoes its argv and stdin so a caller can check what was
// forwarded.
func stageFakeRelease(t *testing.T, asset string) (dir, digest string) {
	t.Helper()

	dir = t.TempDir()
	body := []byte("#!/bin/sh\necho STUB-RAN\necho \"STUB-ARGV:$*\"\necho \"STUB-STDIN:$(cat)\"\n")
	require.NoError(t, os.WriteFile(filepath.Join(dir, asset), body, 0o755))

	sum := sha256.Sum256(body)
	digest = hex.EncodeToString(sum[:])
	// Two spaces, as sha256sum writes and every bootstrap parses.
	require.NoError(t, os.WriteFile(filepath.Join(dir, "checksums.txt"),
		[]byte(fmt.Sprintf("%s  %s\n", digest, asset)), 0o644))
	return dir, digest
}

// A curl on PATH serving serveDir by the URL's basename, returning the PATH entry
// to prepend.
//
// A shim rather than the network, which would depend on a published release and so
// skip on every version-bump branch, when this path changes. The write is slow and
// in two halves, to give the concurrency contract a window wide enough to detect
// an interleave rather than passing by luck.
func fakeCurl(t *testing.T, serveDir string) string {
	t.Helper()

	dir := t.TempDir()
	shim := `#!/usr/bin/env bash
# Serve $SERVE by the URL's basename, exiting 22 as curl does on a 404 so a
# bootstrap's candidate fallback still works. Every URL is appended to $URLLOG when
# set, which is what lets a test assert on what was asked for rather than on what
# got cached.
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
	// The bootstraps fall back to wget, which must not reach the real network.
	require.NoError(t, os.WriteFile(filepath.Join(dir, "wget"),
		[]byte("#!/bin/sh\necho \"wget: no network in this test\" >&2\nexit 1\n"), 0o755))
	return dir
}

// A non-executable bootstrap fails every hook fire silently. Per platform rather
// than a branch; see the Windows twin for what it can assert instead.
func requireExecutable(t *testing.T, path, what string) {
	t.Helper()
	info, err := os.Stat(path)
	require.NoError(t, err, "%s does not resolve to a file", what)
	assert.NotZero(t, info.Mode()&0o111, "%s must be executable", what)
}

// The environment a shell bootstrap runs under: PATH, a throwaway home, and every
// plugin-data variable that decides where the cache goes.
//
// The home matters for the fallback, not for a config file: each bootstrap falls
// back to ${XDG_STATE_HOME:-$HOME/.local/state}/dash0-agent-plugin/<agent>, so a
// run that ignored the variables below would otherwise find the developer's real
// cache. Codex also reads a bare PLUGIN_DATA, which it sets itself for a
// marketplace install.
//
// psEnv subtracts from the real environment instead, a 5.1 child needing more of a
// Windows environment than this names.
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
