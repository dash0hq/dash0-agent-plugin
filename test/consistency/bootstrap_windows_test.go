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
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The behavioural bootstrap contracts, run against the PowerShell bootstraps. The
// static checks in powershell_test.go prove the three files agree with each other,
// which a shared region that is uniformly wrong also satisfies.
//
// Windows-only by filename. Two of these have no .sh twin, Git Bash resolving its
// own curl.exe ahead of a shim where PowerShell takes one off PATH.

// No PowerShell bootstrap ends a hook with a non-zero exit.
//
// Behavioural, because Set-StrictMode with $ErrorActionPreference = 'Stop' turns
// any cmdlet failure into a terminating error, and reading the file will not settle
// whether the trap covers the first thing it does. A regular file takes the name
// the bootstrap wants for its bin directory, which is the Windows counterpart of
// the .sh twin's mode bits.
//
// Unlike that twin, no message is asserted: New-Item -ItemType Directory -Force
// does not fail on a name a file already holds, so the catch that prints "could
// not create" never runs and the write fails later instead. The guarantee is that
// the session survives an unusable data directory.
func TestPowerShellBootstrapsFailOpenWhenTheDataDirectoryIsUnwritable(t *testing.T) {
	for _, a := range windowsBootstraps(t) {
		t.Run(a.Label, func(t *testing.T) {
			dataDir := t.TempDir()
			blocker := filepath.Join(dataDir, "bin")
			require.NoError(t, os.WriteFile(blocker, []byte("not a directory"), 0o644))

			// The shim serves an empty directory, so a run that got further than
			// expected cannot reach the real network.
			env := servedEnv(t, dataDir, t.TempDir())

			out, err := psExec(abs(t, a.WindowsBootstrap), env, "someEvent")
			// First, so an environment fault reports itself rather than being blamed
			// on the data directory.
			requirePastTheArchitectureGate(t, out)
			assert.NoError(t, err,
				"%s exited non-zero when its data directory could not be used, which "+
					"the user's session pays for: Cursor and Codex register a tool-gating hook "+
					"and read it as a refusal, and Copilot prints a hook error on every "+
					"event:\n%s",
				a.WindowsBootstrap, out)
			assert.Contains(t, out, a.Label+"-on-event:",
				"%s exited 0 saying nothing, so a broken install is indistinguishable "+
					"from a working one:\n%s", a.WindowsBootstrap, out)

			// The premise, checked rather than assumed: a run that turned the file
			// into a directory had a usable data directory after all.
			info, statErr := os.Stat(blocker)
			if assert.NoError(t, statErr, "the blocking file is gone, so nothing blocked the run") {
				assert.False(t, info.IsDir(),
					"the blocking file became a directory, so this contract no longer "+
						"blocks anything and needs a different blocker")
			}
		})
	}
}

// Concurrent invocations against a cold cache converge on one correct binary:
// fetch, checksum, rename and exec, eight at once. Staggered rather than
// simultaneous, the damaging overlap being one process starting the binary while a
// later one writes the same path.
//
// Move-Item -Force is the half that can fail, Windows refusing to replace a
// running .exe, so a bootstrap losing that race has to treat an already-present
// binary as success.
//
// Reaching the binary is not required of every invocation, unlike in the .sh twin
// where `mv -f` is an atomic rename. Move-Item -Force on 5.1 deletes the
// destination before renaming over it, so an invocation whose cache check passed
// can find the path gone by the time it starts the binary, and the trap then fails
// it open with a .NET message. That loses the event, and no shipped guard closes
// the window today. What holds either way: nobody exits non-zero, nobody stops at
// a fault the bootstrap itself diagnosed, and one intact binary is left behind.
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

			ran := 0
			for i, r := range results {
				assert.NoError(t, r.err, "invocation %d failed: %s", i, r.out)

				if strings.Contains(r.out, "STUB-RAN") {
					ran++
					continue
				}

				// The replace window, which arrives as a runtime message in the
				// runner's own language, so the shape is what can be asserted: the
				// trap fired, and none of the faults the bootstrap diagnoses itself
				// did. Any of those would be a real failure hiding behind exit 0.
				assert.Contains(t, r.out, a.Label+"-on-event:",
					"invocation %d neither reached the binary nor reported why:\n%s", i, r.out)
				for _, bail := range []string{
					"download failed", "could not create", "checksums fetch failed",
					"no checksum for", "checksum mismatch", "unsupported architecture",
					"could not move",
				} {
					assert.NotContains(t, r.out, bail,
						"invocation %d stopped at %q, which is not the replace window:\n%s",
						i, bail, r.out)
				}
			}
			assert.NotZero(t, ran,
				"no invocation reached the installed binary, so nothing here exercised the "+
					"fetch, checksum, rename and start path: %v", results)

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

// A cached binary that will not run is kept, because deleting it costs a multi-MB
// fetch per tool call: re-download, failed start, repeat.
//
// The stand-in carries no MZ header, so CreateProcess refuses it. Windows has no
// equivalent of the shell fallback that makes the .sh twin picky about its fixture,
// so anything that is not a PE will do; the body is that twin's shebang line,
// rejected for a different reason on each platform.
func TestPowerShellKeepsAnUnrunnableCachedBinary(t *testing.T) {
	body := []byte("#!/nonexistent/interpreter\n")

	for _, a := range windowsBootstraps(t) {
		t.Run(a.Label, func(t *testing.T) {
			// The shim stays on PATH so a re-download cannot reach the network.
			serveDir, _ := stageWindowsRelease(t, a.releaseAsset())
			dataDir := t.TempDir()
			env := servedEnv(t, dataDir, serveDir)

			// Under the name the bootstrap derives, so the cache is warm.
			cached := filepath.Join(dataDir, "bin", a.cacheName(t))
			require.NoError(t, os.MkdirAll(filepath.Dir(cached), 0o755))
			require.NoError(t, os.WriteFile(cached, body, 0o755))

			out, err := psExec(abs(t, a.WindowsBootstrap), env)
			requirePastTheArchitectureGate(t, out)
			assert.NoError(t, err,
				"a cached binary that will not start must still exit 0:\n%s", out)
			// The assertions at the end hold for a bootstrap that did nothing, so the
			// run has to be shown to have reached the start and failed there.
			// CreateProcess reports a bad format in the runner's own language, so the
			// trap's prefix is the locale-independent stand-in for a terminating error
			// that was swallowed. STUB-RAN would mean the cache was read as cold and
			// the staged file never reached CreateProcess.
			require.Contains(t, out, a.Label+"-on-event:",
				"nothing reported a failure, so the bootstrap never tried to start the "+
					"cached binary:\n%s", out)
			require.NotContains(t, out, "STUB-RAN",
				"the bootstrap ran a freshly downloaded binary instead of the staged one, "+
					"so it never tried to start an unrunnable file:\n%s", out)

			// Exit-FailOpen shares that prefix, so every early bail-out satisfies the
			// assertion above with the staged file untouched. Windows on ARM running an
			// amd64 test binary reaches exactly that: cacheName derives from
			// GOARCH=amd64, the .ps1 prefers PROCESSOR_ARCHITEW6432=ARM64, Test-Path
			// misses, the shim 404s, and the run reports "download failed" without
			// calling CreateProcess.
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
// Cursor on Windows does not put the event on the hook process's stdin. It writes
// the payload to a temp file and runs `Get-Content ... | & { $input | <command> }`,
// so it arrives in $input and the bootstrap carries a separate branch for it.
//
// The payload is non-ASCII because that branch writes UTF-8 bytes to the child's
// raw stdin stream precisely to avoid a 5.1 pipeline re-encoding text through
// $OutputEncoding, which is ASCII by default. A regression to a plain pipeline
// still exits 0 and replaces every non-ASCII character in the user's prompt with a
// question mark.
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
			// The same shape Cursor uses. -Raw and -Encoding UTF8 so the file comes
			// back as the characters it holds rather than as the host's code page.
			//
			// Two arguments, one carrying a space: the branch builds a $Psi.Arguments
			// string and quotes any argument matching \s, which with no argument is
			// never reached. No shipped event name has a space, so what the space pins
			// is that the argument survives as one rather than splitting in two.
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
