// SPDX-FileCopyrightText: Copyright 2026 Dash0 Inc.
// SPDX-License-Identifier: Apache-2.0

package consistency

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Markers delimiting the part of a bootstrap that must not diverge between
// agents. Everything above the opening marker is agent-specific: the doc
// comment, AGENT, VERSION, and the data-directory chain.
const (
	sharedBegin = "# >>> shared bootstrap"
	sharedEnd   = "# <<< shared bootstrap <<<"
)

// failOpenAgents are the agents whose bootstrap keeps the session alive on any
// error. Claude's is deliberately excluded: it uses `set -euo pipefail` and
// exits non-zero, and its cache filename is the unprefixed legacy one, so its
// body cannot be identical.
var failOpenAgents = []string{"cursor", "codex", "copilot", "opencode-v2", "copilot-app"}

func bootstrapPath(t *testing.T, agent string) string {
	t.Helper()
	return filepath.Join(repoRoot(t), agent, agent+"-on-event.sh")
}

func readBootstrap(t *testing.T, agent string) string {
	t.Helper()
	body, err := os.ReadFile(bootstrapPath(t, agent))
	require.NoError(t, err)
	return string(body)
}

// sharedRegion returns the marker-delimited body of a file, markers included.
func sharedRegion(t *testing.T, name, body string) string {
	t.Helper()

	start := strings.Index(body, sharedBegin)
	require.NotEqual(t, -1, start, "%s has no %q marker", name, sharedBegin)
	end := strings.Index(body, sharedEnd)
	require.NotEqual(t, -1, end, "%s has no %q marker", name, sharedEnd)
	require.Less(t, start, end, "%s has the markers in the wrong order", name)

	return body[start : end+len(sharedEnd)]
}

// shellRegion is the shared region of an agent's POSIX bootstrap.
func shellRegion(t *testing.T, agent string) string {
	t.Helper()
	return sharedRegion(t, agent+"-on-event.sh", readBootstrap(t, agent))
}

// powerShellRegion is the shared region of an agent's Windows bootstrap.
func powerShellRegion(t *testing.T, agent string) string {
	t.Helper()
	name := agent + "-on-event.ps1"
	body, err := os.ReadFile(filepath.Join(repoRoot(t), agent, name))
	require.NoError(t, err)
	return sharedRegion(t, name, string(body))
}

// The fail-open bootstraps carry one implementation. Nothing enforces that
// at runtime — each agent ships a single self-contained file, because Copilot's
// marketplace source is ./copilot and both installers fetch one file from a raw
// URL — so this test is what keeps a fix from landing in one and not the others.
func TestFailOpenBootstrapsShareOneImplementation(t *testing.T) {
	reference := shellRegion(t, failOpenAgents[0])
	require.NotEmpty(t, strings.TrimSpace(reference))

	for _, agent := range failOpenAgents[1:] {
		assert.Equal(t, reference, shellRegion(t, agent),
			"%s-on-event.sh has diverged from %s-on-event.sh inside the shared region — "+
				"apply the change to all of %v", agent, failOpenAgents[0], failOpenAgents)
	}
}

// The shared region must not name one agent, or copying it to the next one
// carries a wrong asset name that only shows up as a download 404.
func TestSharedRegionIsAgentAgnostic(t *testing.T) {
	region := shellRegion(t, failOpenAgents[0])

	for _, agent := range failOpenAgents {
		assert.NotContains(t, region, agent+"-on-event",
			"the shared region names %s; derive the name from $AGENT instead", agent)
	}
}

// Every bootstrap declares what the shared region consumes. A missing one is a
// `set -u` failure on the first hook event, which fail_open then swallows.
func TestBootstrapsDeclareTheSharedInputs(t *testing.T) {
	for _, agent := range failOpenAgents {
		t.Run(agent, func(t *testing.T) {
			head := strings.SplitN(readBootstrap(t, agent), sharedBegin, 2)[0]

			assert.Contains(t, head, "AGENT=\""+agent+"\"")
			assert.Regexp(t, `(?m)^VERSION="[0-9]+\.[0-9]+\.[0-9]+"$`, head)
			assert.Regexp(t, `(?m)^BASE=`, head)
			assert.Regexp(t, `(?m)^DOWNLOAD_MAX_TIME=(""|[0-9]+)$`, head)
		})
	}
}

// A downloaded binary that cannot be verified is never executed. Claude is
// included: its body differs, but the policy must not.
func TestNoBootstrapRunsAnUnverifiedBinary(t *testing.T) {
	for _, agent := range append([]string{"claude"}, failOpenAgents...) {
		t.Run(agent, func(t *testing.T) {
			body := readBootstrap(t, agent)

			assert.Contains(t, body, "refusing to run an unverified binary",
				"no refusal for a download with no checksums.txt entry")
			assert.Contains(t, body, "no sha256 tool",
				"no refusal for a host with no hash tool")
			assert.Contains(t, body, "checksum mismatch",
				"no refusal for a download whose digest does not match")
		})
	}
}

// fakeUname puts a `uname` on PATH that reports the given kernel and machine, so
// a POSIX host can exercise the Windows naming logic. Git Bash reports strings
// like MINGW64_NT-10.0-26200, which is why the bootstraps normalize at all.
//
// The shim is a POSIX-host device and cannot work on Windows: the bootstraps run
// under Git Bash, which puts its own /usr/bin ahead of anything inherited on
// PATH, so its real uname.exe answers and every case collapses to this machine.
// It is skipped there rather than asserting something it cannot control. Running
// the shell bootstraps for real on Windows is a manual check.
func fakeUname(t *testing.T, kernel, machine string) {
	t.Helper()

	if runtime.GOOS == "windows" {
		t.Skip("Git Bash resolves its own uname.exe ahead of the shim")
	}

	dir := t.TempDir()
	script := fmt.Sprintf("#!/bin/sh\ncase \"$1\" in\n  -s) echo %q ;;\n  -m) echo %q ;;\nesac\n", kernel, machine)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "uname"), []byte(script), 0o755))
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

// runBootstrap runs an agent's bootstrap with its cache pointed at dataDir and
// returns the combined output.
func runBootstrap(t *testing.T, agent, dataDir string, args ...string) (string, error) {
	t.Helper()

	cmd := exec.Command("bash", append([]string{bootstrapPath(t, agent)}, args...)...)
	cmd.Stdin = strings.NewReader("{}")
	cmd.Env = append(os.Environ(),
		"DASH0_PLUGIN_DATA="+dataDir,
		"COPILOT_PLUGIN_DATA="+dataDir,
		"OPENCODE_V2_PLUGIN_DATA="+dataDir,
		"COPILOT_APP_PLUGIN_DATA="+dataDir,
		"DASH0_VERSION=",
	)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// A data directory set in the shell running the tests must not leak in, or a
// bootstrap would run the user's installed binary and write into their cache.
func TestRunBootstrapIgnoresTheCallersDataDir(t *testing.T) {
	stubIn := func(dir, agent, marker string) {
		binDir := filepath.Join(dir, "bin")
		require.NoError(t, os.MkdirAll(binDir, 0o755))
		name := fmt.Sprintf("%s-on-event-%s-%s-%s", agent, bootstrapVersion(t, agent), runtime.GOOS, runtime.GOARCH)
		require.NoError(t, os.WriteFile(filepath.Join(binDir, name),
			[]byte("#!/bin/sh\necho "+marker+"\ncat >/dev/null\n"), 0o755))
	}

	for _, agent := range failOpenAgents {
		t.Run(agent, func(t *testing.T) {
			if runtime.GOOS == "windows" {
				t.Skip("the cache name carries .exe under Git Bash")
			}
			callers := t.TempDir()
			stubIn(callers, agent, "CALLERS-RAN")
			for _, v := range []string{"DASH0_PLUGIN_DATA", "PLUGIN_DATA", "COPILOT_PLUGIN_DATA", "COPILOT_APP_PLUGIN_DATA"} {
				t.Setenv(v, callers)
			}
			dataDir := t.TempDir()
			stubIn(dataDir, agent, "STUB-RAN")

			out, err := runBootstrap(t, agent, dataDir)

			assert.NoError(t, err)
			assert.Contains(t, out, "STUB-RAN", "the bootstrap used the caller's data directory")
		})
	}
}

// bootstrapVersion reads the VERSION the bootstrap pins.
func bootstrapVersion(t *testing.T, agent string) string {
	t.Helper()
	m := regexp.MustCompile(`(?m)^VERSION="([^"]+)"$`).FindStringSubmatch(readBootstrap(t, agent))
	require.Len(t, m, 2, "no VERSION in %s-on-event.sh", agent)
	return m[1]
}

// powerShellVersion reads the $Version the PowerShell bootstrap pins.
func powerShellVersion(t *testing.T, agent string) string {
	t.Helper()
	name := agent + "-on-event.ps1"
	body, err := os.ReadFile(filepath.Join(repoRoot(t), agent, name))
	require.NoError(t, err)
	m := regexp.MustCompile(`(?m)^\$Version = '([^']+)'$`).FindStringSubmatch(string(body))
	require.Len(t, m, 2, "no $Version in %s", name)
	return m[1]
}

// Each pair of bootstraps pins its own version, outside the shared region because
// the syntax differs. scripts/version.sh bumps all fifteen pins together, but
// nothing stopped a hand edit or a dropped line in that script from moving one and
// not the other. The cost of drift is silent and total: the version is in the cache
// filename and the asset name, so Windows would fetch a release asset that does
// not exist, and the local-dev setup would stage a binary under a name the
// PowerShell bootstrap never looks for.
func TestBootstrapVersionsMatchAcrossPlatforms(t *testing.T) {
	for _, agent := range failOpenAgents {
		t.Run(agent, func(t *testing.T) {
			assert.Equal(t, bootstrapVersion(t, agent), powerShellVersion(t, agent),
				"%s-on-event.sh and %s-on-event.ps1 pin different versions — "+
					"bump both (scripts/version.sh does)", agent, agent)
		})
	}
}

// The cache filename a bootstrap derives has to match the one it downloads to,
// or every hook event re-downloads. Pre-placing a stub under the derived name and
// asserting it runs is the only check that the two agree — and under a Git Bash
// uname it is also the only check that the Windows name is right, since no
// release asset is fetched.
func TestBootstrapDerivesTheCacheNamePerPlatform(t *testing.T) {
	tests := []struct {
		name, kernel, machine, wantOS, wantArch, wantExt string
	}{
		{"git bash on x64", "MINGW64_NT-10.0-26200", "x86_64", "windows", "amd64", ".exe"},
		{"git bash on arm64", "MINGW64_NT-10.0-26200", "aarch64", "windows", "arm64", ".exe"},
		{"msys2", "MSYS_NT-10.0-19045", "x86_64", "windows", "amd64", ".exe"},
		{"cygwin", "CYGWIN_NT-10.0", "x86_64", "windows", "amd64", ".exe"},
		{"linux", "Linux", "aarch64", "linux", "arm64", ""},
		{"macos", "Darwin", "arm64", "darwin", "arm64", ""},
	}

	for _, agent := range failOpenAgents {
		for _, tt := range tests {
			t.Run(agent+"/"+tt.name, func(t *testing.T) {
				fakeUname(t, tt.kernel, tt.machine)

				dataDir := t.TempDir()
				binDir := filepath.Join(dataDir, "bin")
				require.NoError(t, os.MkdirAll(binDir, 0o755))
				stub := fmt.Sprintf("%s-on-event-%s-%s-%s%s",
					agent, bootstrapVersion(t, agent), tt.wantOS, tt.wantArch, tt.wantExt)
				require.NoError(t, os.WriteFile(filepath.Join(binDir, stub),
					[]byte("#!/bin/sh\necho STUB-RAN\ncat >/dev/null\n"), 0o755))

				out, err := runBootstrap(t, agent, dataDir)

				assert.NoError(t, err)
				assert.Contains(t, out, "STUB-RAN",
					"the bootstrap did not find %s — it derived a different name and tried to download", stub)
				assert.NotContains(t, out, "download failed",
					"a cached binary must never trigger a download")
			})
		}
	}
}

// Claude is the same contract with two differences: CLAUDE_PLUGIN_DATA rather
// than DASH0_PLUGIN_DATA, and a cache filename that keeps the unprefixed legacy
// name so existing installs do not re-download. It matters most of the four on
// Windows, because Claude Code runs this script directly through Git Bash.
func TestClaudeBootstrapDerivesTheCacheNamePerPlatform(t *testing.T) {
	tests := []struct {
		name, kernel, machine, wantOS, wantArch, wantExt string
	}{
		{"git bash on x64", "MINGW64_NT-10.0-26200", "x86_64", "windows", "amd64", ".exe"},
		{"git bash on arm64", "MINGW64_NT-10.0-26200", "aarch64", "windows", "arm64", ".exe"},
		{"linux", "Linux", "x86_64", "linux", "amd64", ""},
		{"macos", "Darwin", "arm64", "darwin", "arm64", ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fakeUname(t, tt.kernel, tt.machine)

			dataDir := t.TempDir()
			binDir := filepath.Join(dataDir, "bin")
			require.NoError(t, os.MkdirAll(binDir, 0o755))
			stub := fmt.Sprintf("on-event-%s-%s-%s%s",
				bootstrapVersion(t, "claude"), tt.wantOS, tt.wantArch, tt.wantExt)
			require.NoError(t, os.WriteFile(filepath.Join(binDir, stub),
				[]byte("#!/bin/sh\necho STUB-RAN\ncat >/dev/null\n"), 0o755))

			cmd := exec.Command("bash", bootstrapPath(t, "claude"))
			cmd.Stdin = strings.NewReader("{}")
			cmd.Env = append(os.Environ(), "CLAUDE_PLUGIN_DATA="+dataDir)
			out, err := cmd.CombinedOutput()

			assert.NoError(t, err, "output: %s", out)
			assert.Contains(t, string(out), "STUB-RAN",
				"the bootstrap did not find %s — it derived a different name", stub)
		})
	}
}

// The PowerShell bootstraps carry one implementation too, and their banner says
// so. Nothing enforced it: this file only ever read <agent>-on-event.sh, so the
// three .ps1 regions could drift silently — and the Windows install path is the
// one nobody exercises locally.
func TestPowerShellBootstrapsShareOneImplementation(t *testing.T) {
	reference := powerShellRegion(t, failOpenAgents[0])
	require.NotEmpty(t, strings.TrimSpace(reference))

	for _, agent := range failOpenAgents[1:] {
		assert.Equal(t, reference, powerShellRegion(t, agent),
			"%s-on-event.ps1 has diverged from %s-on-event.ps1 inside the shared region — "+
				"apply the change to all of %v", agent, failOpenAgents[0], failOpenAgents)
	}
}

// powerShellFiles is every .ps1 the repository ships: the installers and
// uninstallers at the root, plus each agent's bootstrap.
func powerShellFiles(t *testing.T) []string {
	t.Helper()
	root := repoRoot(t)
	files, err := filepath.Glob(filepath.Join(root, "*.ps1"))
	require.NoError(t, err)
	for _, agent := range failOpenAgents {
		files = append(files, filepath.Join(root, agent, agent+"-on-event.ps1"))
	}
	require.NotEmpty(t, files)
	return files
}

// Nothing else parses these files. The .sh side has shellcheck over every script
// and a `bash -n` on the bootstrap; this side had two tests that read the bytes
// and none that asked whether the result is valid PowerShell. A missing brace
// would ship green in all three bootstraps at once and surface only as a hook that
// produces no output, which is the failure mode with no error message anywhere.
//
// powershell.exe, not pwsh, where both exist: 5.1 is the target and the stricter
// parser of the two, and windows-latest supplies it. Elsewhere pwsh parses the same
// grammar minus the 5.1 restrictions, which still catches an unbalanced brace.
// ParseFile reports every error in the file rather than stopping at the first.
func TestPowerShellFilesParse(t *testing.T) {
	shell := "pwsh"
	if runtime.GOOS == "windows" {
		shell = "powershell"
	}
	if _, err := exec.LookPath(shell); err != nil {
		t.Skipf("%s is not on PATH", shell)
	}

	for _, file := range powerShellFiles(t) {
		t.Run(filepath.Base(file), func(t *testing.T) {
			// The path goes in as a single-quoted PowerShell literal, doubling any
			// quote it contains. A double-quoted one would need the backslashes of a
			// Windows path escaped, which is the mistake this test exists to catch.
			literal := "'" + strings.ReplaceAll(file, "'", "''") + "'"
			script := "$e = $null; " +
				"[System.Management.Automation.Language.Parser]::ParseFile(" + literal + ", [ref]$null, [ref]$e) | Out-Null; " +
				"if ($e.Count) { $e | ForEach-Object { [Console]::Error.WriteLine($_) }; exit 1 }"
			out, err := exec.Command(shell, "-NoProfile", "-Command", script).CombinedOutput()
			assert.NoError(t, err, "%s reported parse errors:\n%s", shell, out)
		})
	}
}

// Windows PowerShell 5.1 reads a .ps1 with no byte-order mark using the system's
// legacy codepage, so a multi-byte character is mis-decoded and can cascade into
// a parse error that shows up as a hook doing nothing at all. Every file here is
// written without a BOM, so every file here has to stay ASCII. This has regressed
// three times.
func TestPowerShellFilesAreASCII(t *testing.T) {
	for _, file := range powerShellFiles(t) {
		t.Run(filepath.Base(file), func(t *testing.T) {
			body, err := os.ReadFile(file)
			require.NoError(t, err)
			for i, b := range body {
				require.Less(t, b, byte(0x80),
					"non-ASCII byte %#x at offset %d — line %d",
					b, i, 1+strings.Count(string(body[:i]), "\n"))
			}
		})
	}
}

// downloadTimeScale is how much faster than real time the download tests run.
// The curl shim divides every timeout the bootstrap passes by it.
const downloadTimeScale = 10

// curlAgainst puts a `curl` on PATH that sends the bootstrap's GitHub requests to
// server and divides its timeouts by downloadTimeScale. Every other flag reaches
// the real curl unchanged, so the timeouts are enforced by curl itself.
func curlAgainst(t *testing.T, server string) {
	t.Helper()

	if runtime.GOOS == "windows" {
		t.Skip("Git Bash resolves its own curl.exe ahead of the shim")
	}
	realCurl, err := exec.LookPath("curl")
	if err != nil {
		t.Skip("curl is not on PATH")
	}

	dir := t.TempDir()
	script := fmt.Sprintf(`#!/usr/bin/env bash
args=()
scale=""
for a in "$@"; do
  case "$scale" in
    decimal) a=$(LC_ALL=C awk -v t="$a" 'BEGIN { print t / %[1]d }') ;;
    # curl takes whole seconds only here; round up, since 0 means no limit.
    whole) a=$(( (a + %[1]d - 1) / %[1]d )) ;;
  esac
  scale=""
  case "$a" in
    --max-time|-m|--connect-timeout) scale=decimal ;;
    --speed-time|-y) scale=whole ;;
  esac
  args+=("${a/https:\/\/github.com/%[2]s}")
done
exec %[3]q "${args[@]}"
`, downloadTimeScale, server, realCurl)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "curl"), []byte(script), 0o755))
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

// curl reads a timeout of 0 as no limit, so a timeout under downloadTimeScale
// must still bind once the shim has scaled it.
func TestCurlShimKeepsSubScaleTimeouts(t *testing.T) {
	tests := []struct {
		name  string
		flags []string
	}{
		{"max-time", []string{"--max-time", "5"}},
		{"speed-time", []string{"--speed-limit", "1024", "--speed-time", "5"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, _ = w.Write([]byte("x"))
				w.(http.Flusher).Flush()
				<-r.Context().Done()
			}))
			t.Cleanup(srv.Close)
			curlAgainst(t, srv.URL)

			ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
			defer cancel()
			args := append(append([]string{"-fsS"}, tt.flags...), "-o", os.DevNull, "https://github.com/stalled")
			out, err := exec.CommandContext(ctx, "curl", args...).CombinedOutput()

			require.NoError(t, ctx.Err(), "the scaled %s no longer bounds the transfer", tt.name)
			var exit *exec.ExitError
			require.ErrorAs(t, err, &exit, "output: %s", out)
			assert.Equal(t, 28, exit.ExitCode(), "curl did not time out; output: %s", out)
		})
	}
}

// releaseServer serves a release whose binary is body, written by send, and a
// checksums.txt that matches it.
func releaseServer(t *testing.T, agent string, body []byte, send func(w http.ResponseWriter, r *http.Request)) string {
	t.Helper()

	asset := fmt.Sprintf("%s-on-event-%s-%s", agent, runtime.GOOS, runtime.GOARCH)
	sum := sha256.Sum256(body)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/checksums.txt"):
			_, _ = fmt.Fprintf(w, "%s  %s\n", hex.EncodeToString(sum[:]), asset)
		case strings.HasSuffix(r.URL.Path, "/"+asset):
			w.Header().Set("Content-Length", fmt.Sprint(len(body)))
			send(w, r)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

// checksums.txt is fetched before the binary, so a link that cannot reach it
// fails fast instead of after a long download that the trap then deletes.
func TestBootstrapFetchesChecksumsBeforeTheBinary(t *testing.T) {
	for _, agent := range failOpenAgents {
		t.Run(agent, func(t *testing.T) {
			var binaryRequests atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if strings.HasSuffix(r.URL.Path, "/checksums.txt") {
					http.Error(w, "unavailable", http.StatusServiceUnavailable)
					return
				}
				binaryRequests.Add(1)
				http.NotFound(w, r)
			}))
			t.Cleanup(srv.Close)
			curlAgainst(t, srv.URL)

			out, err := runBootstrap(t, agent, t.TempDir())

			assert.NoError(t, err, "the bootstrap must fail open")
			assert.Contains(t, out, "checksums fetch failed")
			assert.Zero(t, binaryRequests.Load(), "the binary was requested after checksums.txt failed")
		})
	}
}

// A slow link must still install the binary: the bootstrap fails open, so a
// download cut off while it is still moving silently costs every event's
// telemetry. A link that stops moving must still release the hook.
func TestBootstrapDownloadBoundsStallsNotSlowLinks(t *testing.T) {
	agent := failOpenAgents[0]

	// At 2 KiB/s, twice what counts as stalled, this takes 14 s here: 140 s at
	// production timeouts, past the 100 s total cap the bootstraps used to set.
	stub := []byte("#!/bin/sh\necho STUB-RAN\ncat >/dev/null\nexit 0\n")
	body := append(stub, []byte(strings.Repeat("#", 28*1024-len(stub)))...)
	trickle := func(w http.ResponseWriter, r *http.Request) {
		const chunk = 256
		for i := 0; i < len(body); i += chunk {
			if _, err := w.Write(body[i:min(i+chunk, len(body))]); err != nil {
				return
			}
			w.(http.Flusher).Flush()
			time.Sleep(125 * time.Millisecond)
		}
	}

	t.Run("slow but moving", func(t *testing.T) {
		curlAgainst(t, releaseServer(t, agent, body, trickle))

		out, err := runBootstrap(t, agent, t.TempDir())

		assert.NoError(t, err)
		assert.Contains(t, out, "STUB-RAN", "a download that was still moving was abandoned")
	})

	// The Copilot app's extension SIGKILLs the bootstrap after 120 s, which skips
	// the cleanup trap and leaves curl running. Its download has to end first.
	t.Run("slow but moving, under the copilot-app kill", func(t *testing.T) {
		curlAgainst(t, releaseServer(t, "copilot-app", body, trickle))

		start := time.Now()
		out, err := runBootstrap(t, "copilot-app", t.TempDir())
		held := time.Since(start) * downloadTimeScale

		assert.NoError(t, err, "the bootstrap must fail open")
		assert.Contains(t, out, "download failed")
		assert.Less(t, held, 2*time.Minute, "the download outlived the extension's kill: %s", held)
	})

	t.Run("stalled", func(t *testing.T) {
		curlAgainst(t, releaseServer(t, agent, body, func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write(body[:1024])
			w.(http.Flusher).Flush()
			<-r.Context().Done()
		}))

		start := time.Now()
		out, err := runBootstrap(t, agent, t.TempDir())
		held := time.Since(start) * downloadTimeScale

		assert.NoError(t, err, "the bootstrap must fail open")
		assert.Contains(t, out, "download failed")
		assert.Less(t, held, time.Minute, "a stalled download held the hook for %s of real time", held)
	})
}
