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
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The behavioural bootstrap contracts, run against the PowerShell bootstraps.
//
// The .ps1 side had only static checks: it parses, it is ASCII, and the three
// files agree with each other. Agreement proves the three are consistent, not
// that they behave like the .sh files they mirror, so a shared region that is
// uniformly wrong passed all three.
//
// Windows-only by filename. The .sh twins of two of these skip there because Git
// Bash resolves its own curl.exe ahead of a shim, which PowerShell does not: it
// invokes curl.exe by name off PATH.

// No PowerShell bootstrap ends a hook with a non-zero exit.
//
// Behavioural rather than textual. Set-StrictMode with
// $ErrorActionPreference = 'Stop' turns any cmdlet failure into a terminating
// error, and reading the file will not settle whether the trap covers the first
// thing it does.
//
// The data directory is placed under a regular file, where New-Item fails. That
// is the Windows equivalent of the mode bits the .sh twin uses.
func TestPowerShellBootstrapsFailOpenWhenTheDataDirectoryIsUnwritable(t *testing.T) {
	blocker := filepath.Join(t.TempDir(), "blocker")
	require.NoError(t, os.WriteFile(blocker, []byte("not a directory"), 0o644))

	for _, a := range windowsBootstraps(t) {
		t.Run(a.Label, func(t *testing.T) {
			// The shim serves an empty directory, so a run that somehow got past the
			// mkdir still cannot reach the real network.
			env := servedEnv(t, filepath.Join(blocker, "data"), t.TempDir())

			out, err := psExec(abs(t, a.WindowsBootstrap), env, "someEvent")
			// Before the assertions below, so an environment fault reports itself
			// rather than being blamed on the mkdir this test is about.
			requirePastTheArchitectureGate(t, out)
			assert.NoError(t, err,
				"%s exited non-zero when its data directory could not be created, which "+
					"the user's session pays for: Cursor and Codex register a tool-gating hook "+
					"and read it as a refusal, and Copilot prints a hook error on every "+
					"event:\n%s",
				a.WindowsBootstrap, out)
			assert.Contains(t, out, "could not create",
				"%s exited 0 without reporting the failed mkdir, so this asserted "+
					"nothing about the fail-open path:\n%s", a.WindowsBootstrap, out)
		})
	}
}

// Concurrent invocations against a cold cache all succeed and converge on one
// correct binary. Fetch, checksum, rename and exec run end to end, eight at once,
// leaving one intact file.
//
// Staggered rather than simultaneous. The damaging overlap is one process
// starting the binary while a later one writes the same path.
//
// Move-Item -Force is the half of the rename contract that can fail, because
// Windows refuses to replace a running .exe. A bootstrap losing that race has to
// treat an already-present binary as success.
func TestPowerShellConcurrentColdCacheInvocationsConverge(t *testing.T) {
	for _, a := range windowsBootstraps(t) {
		t.Run(a.Label, func(t *testing.T) {
			serveDir, digest := stageWindowsRelease(t, a.releaseAsset())
			dataDir := t.TempDir()
			env := servedEnv(t, dataDir, serveDir)
			script := abs(t, a.WindowsBootstrap)

			const runs = 8
			type result struct {
				out string
				err error
			}
			results := make([]result, runs)
			var wg sync.WaitGroup
			for i := range runs {
				wg.Add(1)
				go func() {
					defer wg.Done()
					out, err := psExec(script, env)
					results[i] = result{out: out, err: err}
				}()
				time.Sleep(150 * time.Millisecond)
			}
			wg.Wait()

			for i, r := range results {
				// These bootstraps exit 0 whatever happens, so STUB-RAN is what proves
				// the install completed and the file was executed.
				assert.NoError(t, r.err, "invocation %d failed: %s", i, r.out)
				assert.Contains(t, r.out, "STUB-RAN",
					"invocation %d never reached the installed binary: %s", i, r.out)
			}

			binDir := filepath.Join(dataDir, "bin")
			entries, err := os.ReadDir(binDir)
			require.NoError(t, err)
			names := make([]string, 0, len(entries))
			for _, e := range entries {
				names = append(names, e.Name())
			}
			assert.Equal(t, []string{a.cacheName(t)}, names,
				"exactly one file must survive, under the derived cache name; a leftover "+
					".tmp.<pid> means a failure path did not clean up")

			installed, err := os.ReadFile(filepath.Join(binDir, names[0]))
			require.NoError(t, err)
			sum := sha256.Sum256(installed)
			assert.Equal(t, digest, hex.EncodeToString(sum[:]),
				"the cached binary is corrupt; concurrent writers interleaved into it")
		})
	}
}

// A cached binary that will not run does not loop.
//
// Deleting it would make the next hook re-download the asset, fail to start it,
// and repeat, at a multi-MB fetch per tool call. Keeping it costs one failed
// start.
//
// The stand-in carries no MZ header, so CreateProcess refuses it. Windows has no
// equivalent of the shell fallback that makes the .sh twin picky about its
// fixture, so the only requirement here is that the file is not a PE. The body is
// the .sh twin's shebang line, rejected for a different reason on each platform.
func TestPowerShellKeepsAnUnrunnableCachedBinary(t *testing.T) {
	body := []byte("#!/nonexistent/interpreter\n")

	for _, a := range windowsBootstraps(t) {
		t.Run(a.Label, func(t *testing.T) {
			// The curl shim stays on PATH so a re-download cannot reach the network.
			serveDir, _ := stageWindowsRelease(t, a.releaseAsset())
			dataDir := t.TempDir()
			env := servedEnv(t, dataDir, serveDir)

			// Pre-place it under the name the bootstrap derives, so the cache is warm.
			cached := filepath.Join(dataDir, "bin", a.cacheName(t))
			require.NoError(t, os.MkdirAll(filepath.Dir(cached), 0o755))
			require.NoError(t, os.WriteFile(cached, body, 0o755))

			out, err := psExec(abs(t, a.WindowsBootstrap), env)
			requirePastTheArchitectureGate(t, out)
			assert.NoError(t, err,
				"a cached binary that will not start must still exit 0:\n%s", out)
			// Both assertions below hold on a bootstrap that did nothing, so the
			// run has to be shown to have reached the start and failed there.
			//
			// CreateProcess reports a bad format in the runner's own language, so
			// there is no message to match. The trap's prefix is the
			// locale-independent stand-in: reaching it means a terminating error
			// arrived and was swallowed. Exit-FailOpen shares that prefix, which
			// the loop below rules out. STUB-RAN would mean the cache was read as
			// cold and the staged file never reached CreateProcess.
			require.Contains(t, out, a.Label+"-on-event:",
				"nothing reported a failure, so the bootstrap never tried to start the "+
					"cached binary:\n%s", out)
			require.NotContains(t, out, "STUB-RAN",
				"the bootstrap ran a freshly downloaded binary instead of the staged one, "+
					"so it never tried to start an unrunnable file:\n%s", out)

			// Every early bail-out satisfies both assertions above while the staged
			// file sits untouched, so the two checks below would hold trivially.
			//
			// Windows on ARM running an amd64 test binary reaches exactly that:
			// cacheName derives from GOARCH=amd64, the .ps1 prefers
			// PROCESSOR_ARCHITEW6432=ARM64, Test-Path misses, the shim 404s, and
			// the run reports "download failed" without calling CreateProcess.
			for _, bail := range []string{
				"download failed", "could not create", "checksums fetch failed",
				"no checksum for", "checksum mismatch", "unsupported architecture",
				"could not move",
			} {
				require.NotContains(t, out, bail,
					"the bootstrap stopped at %q, before it could start the cached binary, "+
						"so this asserted nothing about an unrunnable file:\n%s", bail, out)
			}

			got, err := os.ReadFile(cached)
			require.NoError(t, err, "the bad binary was deleted, so the next hook re-downloads it")
			assert.Equal(t, body, got, "the bad binary was replaced, so every hook re-downloads it")
		})
	}
}

// The pipeline branch delivers the payload byte for byte.
//
// Cursor on Windows does not put the event on the hook process's stdin. It
// writes the payload to a temp file and runs
// `Get-Content ... | & { $input | <command> }`, which lands in $input instead,
// and the bootstrap carries a separate branch for it.
//
// The payload is non-ASCII on purpose. That branch writes UTF-8 bytes to the
// child's raw stdin stream precisely because a PowerShell 5.1 pipeline
// re-encodes text through $OutputEncoding, which is ASCII by default. A branch
// that regressed to a plain pipeline still runs, still exits 0, and quietly
// replaces every non-ASCII character in the user's prompt with a question mark.
func TestPowerShellPipelineDeliveryPreservesThePayload(t *testing.T) {
	const payload = `{"hook_event_name":"SessionStart","prompt":"Grüße, 世界 — naïve café"}`

	for _, a := range windowsBootstraps(t) {
		t.Run(a.Label, func(t *testing.T) {
			serveDir, _ := stageWindowsRelease(t, a.releaseAsset())
			dataDir := t.TempDir()
			env := servedEnv(t, dataDir, serveDir)

			payloadFile := filepath.Join(t.TempDir(), "event.json")
			require.NoError(t, os.WriteFile(payloadFile, []byte(payload), 0o644))

			script := abs(t, a.WindowsBootstrap)
			// The same shape Cursor uses. -Raw and -Encoding UTF8 so the file is read
			// back as the characters it holds rather than as the host's code page.
			//
			// Two arguments, one carrying a space. This branch builds a
			// $Psi.Arguments string and quotes any argument matching \s, so with no
			// argument the string is empty and the quoting is never reached. No
			// shipped event name contains a space; what the space pins is that the
			// argument survives as one rather than splitting in two.
			command := fmt.Sprintf(
				"Get-Content -Raw -Encoding UTF8 -LiteralPath '%s' | & { $input | & '%s' someEvent 'two words' }",
				payloadFile, script)

			cmd := exec.Command("powershell", "-NoProfile", "-ExecutionPolicy", "Bypass", "-Command", command)
			cmd.Env = env
			raw, err := cmd.CombinedOutput()
			out := string(raw)
			requirePastTheArchitectureGate(t, out)

			assert.NoError(t, err, "the pipeline branch exited non-zero:\n%s", out)
			require.Contains(t, out, "STUB-RAN", "the binary never ran:\n%s", out)
			assert.Contains(t, out, "STUB-ARGV:someEvent|two words",
				"the arguments reached the binary regrouped; $Psi.Arguments has to quote "+
					"an argument containing whitespace, or Windows splits it in two:\n%s", out)
			assert.Contains(t, out, payload,
				"the payload reached the binary altered; a PowerShell 5.1 pipeline re-encodes "+
					"text through $OutputEncoding, so the branch has to write UTF-8 bytes to the "+
					"child's raw stdin:\n%s", out)
		})
	}
}
