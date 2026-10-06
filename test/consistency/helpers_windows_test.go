// SPDX-FileCopyrightText: Copyright 2026 Dash0 Inc.
// SPDX-License-Identifier: Apache-2.0

package consistency

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/dash0hq/dash0-agent-plugin/test/helpers/testenv"
)

// The Windows-only fixtures: the runner that invokes a .ps1 the way a hook does,
// the environment it needs, and the stub binary and curl.exe the download path
// resolves. Both stand-ins are compiled Go rather than scripts, because
// Process.Start and `& curl.exe` accept nothing else. helpers_unix_test.go is the
// POSIX counterpart.

// A PowerShell bootstrap run the way a hook does. -NoProfile so a developer's own
// profile cannot change what is under test, -ExecutionPolicy Bypass because a
// checked-out script carries no signature.
//
// No *testing.T and no assertions, so a worker can call it: the concurrency
// contract does, and FailNow is only usable from the goroutine running the test.
func psExec(script string, env []string, args ...string) (string, error) {
	argv := append([]string{
		"-NoProfile", "-ExecutionPolicy", "Bypass",
		"-File", script,
	}, args...)

	cmd := exec.Command("powershell", argv...)
	cmd.Stdin = strings.NewReader(`{"hook_event_name":"SessionStart"}`)
	cmd.Env = env
	out, err := cmd.CombinedOutput()

	if gateErr := architectureGateErr(string(out)); gateErr != nil {
		return string(out), gateErr
	}
	return string(out), err
}

// Fails when a run stopped at the first thing the shared region does. The
// bootstrap resolves the architecture before it touches the cache and fails open
// when it cannot, so a run with the wrong environment exits 0 having done nothing,
// which is indistinguishable from success for any contract asserting on a file it
// was supposed to leave alone.
func requirePastTheArchitectureGate(t *testing.T, out string) {
	t.Helper()
	require.NoError(t, architectureGateErr(out))
}

// The same check as an error, for a caller on a worker goroutine: a require there
// calls runtime.Goexit on the wrong goroutine, and the rest of that worker is
// skipped in silence.
func architectureGateErr(out string) error {
	if strings.Contains(out, "unsupported architecture") {
		return fmt.Errorf("the bootstrap could not resolve the architecture and failed "+
			"open before reaching anything under test; psEnv must pass "+
			"PROCESSOR_ARCHITECTURE through:\n%s", out)
	}
	return nil
}

// The environment a PowerShell bootstrap runs under.
//
// Subtracted from the real one rather than built up: a 5.1 child needs TEMP,
// PATHEXT, PSModulePath, APPDATA and windir, exec.Cmd replaces the environment
// outright, and enumerating what startup needs is a list nobody keeps correct.
//
// The architecture variables survive the filter anyway, so naming them records a
// dependency: the shared region reads PROCESSOR_ARCHITEW6432 then
// PROCESSOR_ARCHITECTURE and fails open when both are empty.
func psEnv(t *testing.T, dataDir string) []string {
	t.Helper()

	home := filepath.Join(t.TempDir(), "home")
	require.NoError(t, os.MkdirAll(home, 0o755))

	env := testenv.CleanHome(home,
		"CLAUDE_PLUGIN_DATA="+dataDir,
		"DASH0_PLUGIN_DATA="+dataDir,
		"COPILOT_PLUGIN_DATA="+dataDir,
		// The two the filter lets through, both read above the shared marker: a bare
		// PLUGIN_DATA misses its suffix rule and XDG_STATE_HOME matches no rule at
		// all. The prefixed variable outranks both, so leaving them set would only
		// be harmless by accident of precedence.
		"PLUGIN_DATA="+dataDir,
		"XDG_STATE_HOME="+dataDir,
	)
	for _, name := range []string{"PROCESSOR_ARCHITECTURE", "PROCESSOR_ARCHITEW6432"} {
		if v := os.Getenv(name); v != "" {
			env = append(env, name+"="+v)
		}
	}
	require.NotEmpty(t, os.Getenv("PROCESSOR_ARCHITECTURE"),
		"PROCESSOR_ARCHITECTURE is unset in this shell, so every bootstrap below would fail open")
	return env
}

// A real PE binary, because the bootstrap resolves curl.exe off PATH and starts
// the cached binary through Process.Start, and neither accepts a .bat or a shebang.
func buildExe(t *testing.T, dir, name, src string) string {
	t.Helper()

	srcDir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(srcDir, "main.go"), []byte(src), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(srcDir, "go.mod"), []byte("module stub\n\ngo 1.21\n"), 0o644))

	out := filepath.Join(dir, name)
	build := exec.Command("go", "build", "-o", out, ".")
	build.Dir = srcDir
	if b, err := build.CombinedOutput(); err != nil {
		t.Fatalf("building %s:\n%s", name, b)
	}
	return out
}

// Echoes a marker and its argv, then copies stdin, so a test can prove the
// installed binary ran, what reached it, and that the payload arrived intact.
//
// argv matters on the pipeline branch, which builds a $Psi.Arguments string by
// hand and has to quote an argument containing whitespace; splitting one argument
// into two there still exits 0.
const stubSource = `package main

import (
	"io"
	"os"
	"strings"
)

func main() {
	os.Stdout.WriteString("STUB-RAN\n")
	os.Stdout.WriteString("STUB-ARGV:" + strings.Join(os.Args[1:], "|") + "\n")
	io.Copy(os.Stdout, os.Stdin)
}
`

// A curl.exe serving a directory by the URL's basename, rather than the network,
// which would depend on a published release and so skip on every version-bump
// branch, when this path changes.
//
// It exits 22 for an absent file, as curl does on a 404, and writes a download in
// two halves around a sleep, giving the concurrency contract a window wide enough
// to detect an interleave rather than passing by luck.
const curlSource = `package main

import (
	"os"
	"path"
	"strings"
	"time"
)

func main() {
	var out, url string
	args := os.Args[1:]
	for i := 0; i < len(args); i++ {
		switch {
		case args[i] == "-o" && i+1 < len(args):
			out = args[i+1]
			i++
		case strings.HasPrefix(args[i], "-"):
		default:
			url = args[i]
		}
	}

	body, err := os.ReadFile(path.Join(os.Getenv("SERVE"), path.Base(url)))
	if err != nil {
		os.Exit(22)
	}
	if out == "" {
		os.Stdout.Write(body)
		return
	}

	f, err := os.Create(out)
	if err != nil {
		os.Exit(23)
	}
	half := (len(body) + 1) / 2
	f.Write(body[:half])
	f.Sync()
	time.Sleep(200 * time.Millisecond)
	f.Write(body[half:])
	f.Close()
}
`

// The asset a bootstrap downloads plus a matching checksums.txt, in a directory
// the curl shim serves.
func stageWindowsRelease(t *testing.T, asset string) (serveDir, digest string) {
	t.Helper()

	serveDir = t.TempDir()
	buildExe(t, serveDir, asset, stubSource)

	body, err := os.ReadFile(filepath.Join(serveDir, asset))
	require.NoError(t, err)
	sum := sha256.Sum256(body)
	digest = hex.EncodeToString(sum[:])

	// Two spaces, as sha256sum writes and the bootstrap parses.
	require.NoError(t, os.WriteFile(filepath.Join(serveDir, "checksums.txt"),
		[]byte(fmt.Sprintf("%s  %s\n", digest, asset)), 0o644))
	return serveDir, digest
}

// psEnv plus the curl shim ahead of the real curl.exe.
func servedEnv(t *testing.T, dataDir, serveDir string) []string {
	t.Helper()

	shimDir := t.TempDir()
	buildExe(t, shimDir, "curl.exe", curlSource)

	return append(psEnv(t, dataDir),
		"PATH="+shimDir+string(os.PathListSeparator)+os.Getenv("PATH"),
		"SERVE="+serveDir,
	)
}

// Existence only: Windows decides what it will run by extension and reports 0444
// or 0666 for every regular file, so there is no bit to assert. The file still has
// to be there, Claude Code running these scripts through Git Bash.
func requireExecutable(t *testing.T, path, what string) {
	t.Helper()
	require.FileExists(t, path, "%s does not resolve to a file", what)
}

// Every cmdlet the shipped .ps1 files call, resolved in the environment a hook
// hands them.
//
// A cmdlet that cannot be resolved is otherwise invisible: the bootstrap traps the
// CommandNotFoundException and exits 0, so the binary is never installed,
// telemetry is off and nothing says why. Get-FileHash was one, living in
// Microsoft.PowerShell.Utility, which a 5.1 child cannot autoload when its
// inherited PSModulePath lists PowerShell 7's module directories first: a runner,
// and also a hook started from a pwsh terminal.
//
// The list is read from the scripts, so a cmdlet added later is covered without
// anyone remembering to add it.
func TestEveryCmdletABootstrapCallsResolvesInAHooksEnvironment(t *testing.T) {
	for _, a := range windowsBootstraps(t) {
		t.Run(a.Label, func(t *testing.T) {
			called := cmdletsCalled(t, abs(t, a.WindowsBootstrap))
			require.NotEmpty(t, called, "no cmdlet calls found, so this asserted nothing")
			t.Logf("cmdlets called: %v", called)

			var probe strings.Builder
			probe.WriteString("$ErrorActionPreference = 'Continue'\n")
			// The startup inputs a 5.1 child resolves modules from: the environment is
			// subtracted from the parent's, and a value the parent shell mangled
			// cannot be seen from the verdict alone.
			probe.WriteString("[Console]::Out.WriteLine('version=' + $PSVersionTable.PSVersion)\n")
			probe.WriteString("[Console]::Out.WriteLine('PSModulePath=' + $env:PSModulePath)\n")
			for _, name := range called {
				fmt.Fprintf(&probe,
					"if (Get-Command %[1]s -ErrorAction SilentlyContinue) "+
						"{ [Console]::Out.WriteLine('HAVE %[1]s') } "+
						"else { [Console]::Out.WriteLine('MISSING %[1]s') }\n", name)
			}

			script := filepath.Join(t.TempDir(), "probe.ps1")
			require.NoError(t, os.WriteFile(script, []byte(probe.String()), 0o644))

			out, err := psExec(script, psEnv(t, t.TempDir()))
			require.NoError(t, err, "the probe itself could not run:\n%s", out)

			assert.NotContains(t, out, "MISSING",
				"%s calls a cmdlet that cannot be resolved in the environment psEnv "+
					"builds, so the bootstrap traps a CommandNotFoundException and exits 0 "+
					"having installed nothing:\n%s", a.WindowsBootstrap, out)
		})
	}
}

// The Verb-Noun names a script calls, minus the ones it defines itself. Comments
// are excluded, or prose naming a cmdlet the script deliberately stopped calling
// would be probed for.
func cmdletsCalled(t *testing.T, file string) []string {
	t.Helper()

	code := strings.Join(powerShellCodeLines(t, file), "\n")

	own := map[string]bool{}
	for _, m := range psFunctionDecl.FindAllStringSubmatch(code, -1) {
		own[m[1]] = true
	}

	seen := map[string]bool{}
	var names []string
	for _, m := range psVerbNoun.FindAllString(code, -1) {
		if own[m] || seen[m] {
			continue
		}
		seen[m] = true
		names = append(names, m)
	}
	sort.Strings(names)
	return names
}

// A function the script declares, and a PowerShell command name. The noun allows
// no digits, which keeps a version or an asset name out of the results.
var (
	psFunctionDecl = regexp.MustCompile(`(?m)^\s*function\s+([A-Z][a-zA-Z]*-[A-Z][a-zA-Z]*)`)
	psVerbNoun     = regexp.MustCompile(`\b([A-Z][a-z]+-[A-Z][a-zA-Z]*)\b`)
)
