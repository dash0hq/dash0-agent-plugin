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

// The Windows-only test fixtures: the runner that invokes a .ps1 the way a hook
// does, the environment it needs, the stub binary and curl.exe the download path
// resolves, and the exec-bit assertion Windows cannot make.
//
// Both stand-ins are compiled Go rather than scripts, because Process.Start and
// `& curl.exe` accept nothing else. helpers_unix_test.go is the POSIX
// counterpart. It declares a requireExecutable of the same name and deliberately
// not the same assertion: there is no mode bit to check here, so this one only
// asserts the file exists.

// psExec runs a PowerShell bootstrap the way a hook does and returns the
// combined output. -NoProfile so a developer's own profile cannot change what is
// under test, -ExecutionPolicy Bypass because a checked-out script carries no
// signature.
//
// It takes no *testing.T and asserts nothing, so it is safe to call from a
// worker: the concurrency contract does, and testing documents FailNow as usable
// only from the goroutine running the test. Callers resolve script with abs and
// check the returned output themselves, on the test goroutine.
func psExec(script string, env []string, args ...string) (string, error) {
	return psExecIn(script, "", env, `{"hook_event_name":"SessionStart"}`, args...)
}

// psExecIn is psExec with the payload and the working directory chosen by the
// caller: one contract needs a payload the entrypoint rejects, and one needs to
// run from somewhere other than this package.
//
// An empty dir inherits the test's, which is the package directory. That matters
// for any contract that runs the REAL binary rather than a stub: harness's
// configFile reads a RELATIVE <ConfigDir>/dash0-agent-plugin.local.md before the
// copy under the home, so a .cursor, .codex or .copilot directory dropped in the
// checkout would hand it a live endpoint and token. The POSIX twin sets cmd.Dir
// for the same reason; see TestBootstrapsExitZeroForTheRealBinary.
func psExecIn(script, dir string, env []string, payload string, args ...string) (string, error) {
	argv := append([]string{
		"-NoProfile", "-ExecutionPolicy", "Bypass",
		"-File", script,
	}, args...)

	cmd := exec.Command("powershell", argv...)
	cmd.Stdin = strings.NewReader(payload)
	cmd.Env = env
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()

	if gateErr := architectureGateErr(string(out)); gateErr != nil {
		return string(out), gateErr
	}
	return string(out), err
}

// requirePastTheArchitectureGate fails when a run stopped at the first thing the
// shared region does.
//
// The bootstrap resolves the architecture before it touches the cache, and it
// fails open when it cannot, so a run with the wrong environment exits 0 having
// done nothing. Two of the contracts below assert on a file the bootstrap was
// supposed to leave alone, and they pass on a bootstrap that never ran.
func requirePastTheArchitectureGate(t *testing.T, out string) {
	t.Helper()
	require.NoError(t, architectureGateErr(out))
}

// architectureGateErr is the same check as an error, for callers on a goroutine.
//
// A require reached from inside a worker calls runtime.Goexit on the wrong
// goroutine: the wg.Done defer still runs, so the suite survives, but the stop is
// not the one intended and anything after it in that worker is skipped in
// silence.
func architectureGateErr(out string) error {
	if strings.Contains(out, "unsupported architecture") {
		return fmt.Errorf("the bootstrap could not resolve the architecture and failed "+
			"open before reaching anything under test; psEnv must pass "+
			"PROCESSOR_ARCHITECTURE through:\n%s", out)
	}
	return nil
}

// psEnv is the environment a PowerShell bootstrap runs under.
//
// Subtracted from the real environment through testenv.CleanHome rather than
// built from scratch. A 5.1 child needs TEMP, PATHEXT, PSModulePath, APPDATA and
// windir, and exec.Cmd replaces the environment outright, adding back only
// SYSTEMROOT. Enumerating what startup needs is a list nobody can keep correct.
//
// Clean's suffix rule matches CODEX_PLUGIN_DATA and misses a bare
// PLUGIN_DATA, so the names below are set explicitly rather than trusted to it.
//
// The architecture variables survive Clean anyway, so naming them documents a
// dependency rather than fixing one: the shared region reads
// PROCESSOR_ARCHITEW6432 then PROCESSOR_ARCHITECTURE, and fails open when both
// are empty. architectureGateErr catches that.
func psEnv(t *testing.T, dataDir string) []string {
	t.Helper()

	home := filepath.Join(t.TempDir(), "home")
	require.NoError(t, os.MkdirAll(home, 0o755))

	env := testenv.CleanHome(home,
		"CLAUDE_PLUGIN_DATA="+dataDir,
		"DASH0_PLUGIN_DATA="+dataDir,
		"COPILOT_PLUGIN_DATA="+dataDir,
		// The two the filter lets through, both read in the per-runtime block
		// above the shared marker. A bare PLUGIN_DATA is codex only, for a
		// marketplace install, and misses the "_PLUGIN_DATA" suffix rule.
		// XDG_STATE_HOME is read by all three and matches no rule at all. The
		// prefixed variable outranks both, so omitting these was harmless only
		// by accident of precedence.
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

// buildExe compiles src into dir/name and returns the path. A real PE binary,
// because everything under test here execs what it installed: the bootstrap
// resolves curl.exe off PATH and starts the cached binary through
// Process.Start, and neither accepts a .bat or a script with a shebang.
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

// stubSource echoes a marker, its argv and then copies stdin to stdout, so a test
// can prove that the installed binary ran, which arguments reached it, and that
// the payload arrived intact.
//
// argv matters on the pipeline branch: that branch builds a $Psi.Arguments string
// by hand and has to quote an argument containing whitespace, so a regression
// there splits one argument into two and the bootstrap still exits 0.
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

// curlSource is a curl.exe that serves a directory by the URL's basename.
//
// A shim rather than the network: the bootstraps hardcode the GitHub release
// URL, so the alternative depends on a published release and skips on every
// version-bump branch, which is when this path changes.
//
// It exits 22 for an absent file, as curl does on a 404, and writes a download
// in two halves around a sleep, so the concurrency contract below has a window
// wide enough to detect an interleave rather than passing by luck.
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

// stageWindowsRelease writes the asset a bootstrap downloads, plus a matching
// checksums.txt, into a directory the curl shim serves.
func stageWindowsRelease(t *testing.T, asset string) (serveDir, digest string) {
	t.Helper()

	serveDir = t.TempDir()
	buildExe(t, serveDir, asset, stubSource)

	body, err := os.ReadFile(filepath.Join(serveDir, asset))
	require.NoError(t, err)
	sum := sha256.Sum256(body)
	digest = hex.EncodeToString(sum[:])

	// Two spaces between digest and name, as sha256sum writes and the bootstrap
	// parses.
	require.NoError(t, os.WriteFile(filepath.Join(serveDir, "checksums.txt"),
		[]byte(fmt.Sprintf("%s  %s\n", digest, asset)), 0o644))
	return serveDir, digest
}

// servedEnv is hookEnv plus the curl shim ahead of the real curl.exe.
func servedEnv(t *testing.T, dataDir, serveDir string) []string {
	t.Helper()

	shimDir := t.TempDir()
	buildExe(t, shimDir, "curl.exe", curlSource)

	return append(psEnv(t, dataDir),
		"PATH="+shimDir+string(os.PathListSeparator)+os.Getenv("PATH"),
		"SERVE="+serveDir,
	)
}

// requireExecutable checks the file is there and stops.
//
// Windows decides what it will run by extension and reports 0444 or 0666 for
// every regular file, so there is no bit to assert. The file still has to exist:
// Claude Code runs these scripts through Git Bash, which resolves the same path.
func requireExecutable(t *testing.T, path, what string) {
	t.Helper()
	require.FileExists(t, path, "%s does not resolve to a file", what)
}

// TestEveryCmdletABootstrapCallsResolvesInAHooksEnvironment checks that each
// cmdlet the shipped .ps1 files call can be resolved in the environment a hook
// hands them.
//
// A cmdlet that cannot be resolved is invisible here. Every bootstrap traps the
// unforeseen and exits 0, so a CommandNotFoundException reads as a fail-open:
// the binary is never installed, telemetry is off, and nothing says why. This
// caught Get-FileHash, which lives in Microsoft.PowerShell.Utility and does not
// autoload in a Windows PowerShell 5.1 child whose inherited PSModulePath lists
// PowerShell 7's module directories first. That is a runner, and it is also a hook
// started from a pwsh terminal.
//
// The list is read from the scripts rather than written here, so a cmdlet added
// later is covered without anyone remembering to add it.
func TestEveryCmdletABootstrapCallsResolvesInAHooksEnvironment(t *testing.T) {
	for _, a := range windowsBootstraps(t) {
		t.Run(a.Label, func(t *testing.T) {
			called := cmdletsCalled(t, abs(t, a.WindowsBootstrap))
			require.NotEmpty(t, called, "no cmdlet calls found, so this asserted nothing")
			t.Logf("cmdlets called: %v", called)

			var probe strings.Builder
			probe.WriteString("$ErrorActionPreference = 'Continue'\n")
			// The startup inputs a 5.1 child resolves modules from, because the
			// environment is subtracted from the parent's and a value the parent
			// shell mangled cannot be seen from the verdict alone.
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

// cmdletsCalled returns the Verb-Noun names a script calls, minus the ones it
// defines itself. Comments are excluded, or the prose naming a cmdlet the script
// deliberately stopped calling would be probed for.
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

// psFunctionDecl matches a function the script declares, and psVerbNoun a
// PowerShell command name. The noun allows no digits, which keeps a version or an
// asset name out of the results.
var (
	psFunctionDecl = regexp.MustCompile(`(?m)^\s*function\s+([A-Z][a-zA-Z]*-[A-Z][a-zA-Z]*)`)
	psVerbNoun     = regexp.MustCompile(`\b([A-Z][a-z]+-[A-Z][a-zA-Z]*)\b`)
)
