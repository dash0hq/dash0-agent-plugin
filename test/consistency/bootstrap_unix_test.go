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
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The bootstrap contracts only a POSIX host can run. The download ones put a shim
// named `curl` on PATH, which Git Bash ignores in favour of its own curl.exe, and
// the fail-open ones make a directory unwritable through its mode bits, which
// Windows does not honour that way. bootstrap_windows_test.go is the .ps1
// counterpart.

// The cache filename a bootstrap derives has to match the one it downloads to, or
// every hook event re-downloads. A stub pre-placed under the derived name is the
// only check that the two agree, and under a faked Git Bash uname the only check
// that the Windows name is right.
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

	for _, a := range sharedBodyBootstraps(t) {
		for _, tt := range tests {
			t.Run(a.Label+"/"+tt.name, func(t *testing.T) {
				fakeUname(t, tt.kernel, tt.machine)

				dataDir := t.TempDir()
				binDir := filepath.Join(dataDir, "bin")
				require.NoError(t, os.MkdirAll(binDir, 0o755))
				stub := fmt.Sprintf("%s-%s-%s-%s%s",
					a.CacheStem, a.bootstrapVersion(t), tt.wantOS, tt.wantArch, tt.wantExt)
				require.NoError(t, os.WriteFile(filepath.Join(binDir, stub),
					[]byte("#!/bin/sh\necho STUB-RAN\ncat >/dev/null\n"), 0o755))

				out, err := runBootstrap(t, a, dataDir)

				assert.NoError(t, err)
				assert.Contains(t, out, "STUB-RAN",
					"the bootstrap did not find %s, so it derived a different name and downloaded", stub)
				assert.NotContains(t, out, "download failed",
					"a cached binary must never trigger a download")
			})
		}
	}
}

// The same for Claude, which reads CLAUDE_PLUGIN_DATA and keeps the unprefixed
// legacy cache name.
func TestClaudeBootstrapDerivesTheCacheNamePerPlatform(t *testing.T) {
	tests := []struct {
		name, kernel, machine, wantOS, wantArch, wantExt string
	}{
		{"git bash on x64", "MINGW64_NT-10.0-26200", "x86_64", "windows", "amd64", ".exe"},
		{"git bash on arm64", "MINGW64_NT-10.0-26200", "aarch64", "windows", "arm64", ".exe"},
		{"linux", "Linux", "x86_64", "linux", "amd64", ""},
		{"macos", "Darwin", "arm64", "darwin", "arm64", ""},
	}

	claude := agentByLabel(t, "claude")

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fakeUname(t, tt.kernel, tt.machine)

			dataDir := t.TempDir()
			binDir := filepath.Join(dataDir, "bin")
			require.NoError(t, os.MkdirAll(binDir, 0o755))
			stub := fmt.Sprintf("%s-%s-%s-%s%s",
				claude.CacheStem, claude.bootstrapVersion(t), tt.wantOS, tt.wantArch, tt.wantExt)
			require.NoError(t, os.WriteFile(filepath.Join(binDir, stub),
				[]byte("#!/bin/sh\necho STUB-RAN\ncat >/dev/null\n"), 0o755))

			// hookEnv, because an exported DASH0_VERSION would shift the version
			// spliced into the cache name and leave the staged stub unfindable. The
			// shim serves an empty directory, so the run cannot reach the network.
			shimDir := fakeCurl(t, t.TempDir())
			cmd := exec.Command("bash", abs(t, claude.Bootstrap))
			cmd.Stdin = strings.NewReader("{}")
			cmd.Env = append(hookEnv(t, dataDir),
				"PATH="+shimDir+string(os.PathListSeparator)+os.Getenv("PATH"))
			out, err := cmd.CombinedOutput()

			assert.NoError(t, err, "output: %s", out)
			assert.Contains(t, string(out), "STUB-RAN",
				"the bootstrap did not find %s, so it derived a different name", stub)
		})
	}
}

// Concurrent invocations against a cold cache converge on one correct binary:
// fetch, checksum, chmod, rename and exec, eight at once. Staggered rather than
// simultaneous, the damaging overlap being one process exec'ing while a later one
// truncates the same path, which a burst of identical starts mostly misses.
func TestConcurrentColdCacheInvocationsConverge(t *testing.T) {
	for _, a := range Agents {
		t.Run(a.Label, func(t *testing.T) {
			serveDir, digest := stageFakeRelease(t, a.releaseAsset())
			shimDir := fakeCurl(t, serveDir)

			dataDir := t.TempDir()
			env := append(hookEnv(t, dataDir),
				"PATH="+shimDir+string(os.PathListSeparator)+os.Getenv("PATH"),
				"SERVE="+serveDir,
			)

			// Resolved outside the workers: abs asserts, and FailNow is only usable
			// from the goroutine running the test.
			script := abs(t, a.Bootstrap)

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
					cmd := exec.Command("bash", script)
					cmd.Stdin = strings.NewReader("{}")
					cmd.Env = env
					out, err := cmd.CombinedOutput()
					results[i] = result{out: string(out), err: err}
				}()
				time.Sleep(150 * time.Millisecond)
			}
			wg.Wait()

			for i, r := range results {
				// These exit 0 whatever happens, so STUB-RAN is what proves the file ran.
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
			assert.Len(t, names, 1,
				"exactly one file must survive; a leftover .tmp.<pid> means a failure path did not clean up: %v", names)

			installed, err := os.ReadFile(filepath.Join(binDir, names[0]))
			require.NoError(t, err)
			sum := sha256.Sum256(installed)
			assert.Equal(t, digest, hex.EncodeToString(sum[:]),
				"the cached binary is corrupt; concurrent writers interleaved into it")
		})
	}
}

// Every bootstrap hands the binary its argv and its stdin unchanged. Dropping the
// "$@" from the closing exec is invisible to every other check here, the binary
// still running and still exiting 0, and Copilot takes its event name from argv.
func TestBootstrapsForwardArgvAndStdin(t *testing.T) {
	const payload = `{"hook_event_name":"SessionStart","session_id":"forwarded"}`

	for _, a := range Agents {
		t.Run(a.Label, func(t *testing.T) {
			serveDir, _ := stageFakeRelease(t, a.releaseAsset())
			shimDir := fakeCurl(t, serveDir)

			cmd := exec.Command("bash", abs(t, a.Bootstrap), "someEvent")
			cmd.Stdin = strings.NewReader(payload)
			cmd.Env = append(hookEnv(t, t.TempDir()),
				"PATH="+shimDir+string(os.PathListSeparator)+os.Getenv("PATH"),
				"SERVE="+serveDir,
			)

			out, err := cmd.CombinedOutput()
			require.NoError(t, err, "output: %s", out)
			require.Contains(t, string(out), "STUB-RAN", "the binary never ran:\n%s", out)

			assert.Contains(t, string(out), "STUB-ARGV:someEvent",
				"%s did not forward its argv; the last line must exec with \"$@\":\n%s",
				a.Bootstrap, out)
			assert.Contains(t, string(out), "STUB-STDIN:"+payload,
				"%s did not forward stdin unchanged:\n%s", a.Bootstrap, out)
		})
	}
}

// No bootstrap ends a hook with a non-zero exit. Behavioural, because a grep for
// `exit [1-9]` cannot see a `set -e` exit, a `:?` expansion or a failing exec. An
// unwritable data directory poisons the first thing every bootstrap does.
func TestBootstrapsFailOpenWhenTheDataDirectoryIsUnwritable(t *testing.T) {
	ro := t.TempDir()
	require.NoError(t, os.Chmod(ro, 0o500))
	t.Cleanup(func() { _ = os.Chmod(ro, 0o700) })
	if f, err := os.Create(filepath.Join(ro, "probe")); err == nil {
		_ = f.Close()
		t.Skip("cannot make a directory unwritable here; running as root?")
	}

	for _, a := range Agents {
		t.Run(a.Label, func(t *testing.T) {
			cmd := exec.Command("bash", abs(t, a.Bootstrap), "someEvent")
			// cwd there too: configFile reads a relative
			// <ConfigDir>/dash0-agent-plugin.local.md against the working directory, so
			// a run left here would consult test/consistency/<ConfigDir>/ instead.
			cmd.Dir = ro
			cmd.Stdin = strings.NewReader(`{"hook_event_name":"SessionStart"}`)
			cmd.Env = hookEnv(t, filepath.Join(ro, "data"))

			out, err := cmd.CombinedOutput()
			assert.NoError(t, err,
				"%s exited non-zero when its data directory could not be created, which "+
					"takes the user's turn down with it:\n%s", a.Bootstrap, out)
			// Exit 0 alone holds for a bootstrap that failed open earlier and for
			// another reason, so this pins which path it took.
			assert.Contains(t, string(out), "could not create",
				"%s exited 0 without reporting the failed mkdir, so this asserted nothing "+
					"about the fail-open path:\n%s", a.Bootstrap, out)
		})
	}
}

// A cached binary that will not run is kept, because deleting it costs a multi-MB
// fetch per tool call: re-download, failed exec, repeat.
//
// The stand-in names an interpreter that does not exist, rather than being empty or
// garbage. bash answers ENOEXEC by re-reading the file as a shell script, and an
// empty script exits 0, which satisfies the assertions below on a bootstrap that
// never reached an exec.
func TestAnUnrunnableCachedBinaryIsKept(t *testing.T) {
	body := []byte("#!/nonexistent/interpreter\n")

	for _, a := range Agents {
		t.Run(a.Label, func(t *testing.T) {
			serveDir, _ := stageFakeRelease(t, a.releaseAsset())
			shimDir := fakeCurl(t, serveDir)

			// Under the name the bootstrap derives, so the cache is warm. The shim
			// stays on PATH so a re-download cannot reach the network.
			dataDir := t.TempDir()
			cached := filepath.Join(dataDir, "bin", a.cacheName(t))
			require.NoError(t, os.MkdirAll(filepath.Dir(cached), 0o755))
			require.NoError(t, os.WriteFile(cached, body, 0o755))

			cmd := exec.Command("bash", abs(t, a.Bootstrap))
			cmd.Stdin = strings.NewReader(`{"hook_event_name":"SessionStart"}`)
			cmd.Env = append(hookEnv(t, dataDir),
				"PATH="+shimDir+string(os.PathListSeparator)+os.Getenv("PATH"),
				"SERVE="+serveDir,
			)

			out, err := cmd.CombinedOutput()

			// The assertions at the end hold for a bootstrap that did nothing at all,
			// so the run has to be shown to have reached the exec and failed there.
			// Which message arrives is per platform, macOS bash naming the interpreter
			// where Linux reports "required file not found", so what both have in
			// common is the path they tried to run.
			require.Contains(t, string(out), a.cacheName(t),
				"the bootstrap never tried to exec the cached binary, so this asserted "+
					"nothing about what it does when the exec fails:\n%s", out)
			require.NotContains(t, string(out), "STUB-RAN",
				"the bootstrap ran a freshly downloaded binary instead of the staged one, "+
					"so it never tried to exec an unrunnable file:\n%s", out)

			// Two fail-open messages carry the same name, so every early bail-out
			// would satisfy the assertion above with the staged file untouched.
			for _, bail := range []string{
				"download failed", "could not create", "checksums fetch failed",
				"no checksum for", "no sha256 tool", "checksum mismatch",
				"could not mark", "could not move",
			} {
				require.NotContains(t, string(out), bail,
					"the bootstrap stopped at %q, before it could exec the cached binary, "+
						"so this asserted nothing about an unrunnable file:\n%s", bail, out)
			}

			// Exit 0 for Claude only: it sets `shopt -s execfail` and reaches its
			// fail_open after a failed exec, where the three sharing a body end in a
			// bare `exec` and bash exits 126 first.
			if !a.SharesBootstrapBody {
				assert.NoError(t, err, "an unrunnable cached binary must still exit 0:\n%s", out)
			}

			got, err := os.ReadFile(cached)
			require.NoError(t, err, "the bad binary was deleted, so the next hook re-downloads it")
			assert.Equal(t, body, got, "the bad binary was replaced, so every hook re-downloads it")
		})
	}
}

// DASH0_VERSION reaches both a download URL and a filesystem path. `curl` squashes
// `..`, so `../../../attacker/repo/releases/download/v9` retargets BASE_URL at
// another repository, and checksums.txt comes from the same base and verifies the
// binary it serves. A hook runs inside an agent session, so a project .envrc is
// enough to reach it.
//
// All four bootstraps, each carrying its own copy of the guard. The shim records
// every URL, so the assertion is that nothing was requested from the injected
// path; asserting only that the hook kept running would pass with the guard gone.
func TestBootstrapsRefuseAVersionOverrideThatIsNotAVersion(t *testing.T) {
	bad := []string{
		"../../../../attacker/repo/releases/download/v9",
		"../../etc",
		// The realistic typo, and why a rejection must not end the hook: a leading
		// v is wrong here and right in a tag.
		"v0.1.25",
		"0.1.25; id",
		"0.1.25 && id",
		"$(id)",
	}

	for _, a := range Agents {
		t.Run(a.Label, func(t *testing.T) {
			pinned := a.bootstrapVersion(t)

			for _, value := range bad {
				t.Run(value, func(t *testing.T) {
					// The shim serves the pinned version's asset, so honouring the
					// override 404s and falling back reaches the stub.
					serveDir, _ := stageFakeRelease(t, a.releaseAsset())
					shimDir := fakeCurl(t, serveDir)
					urlLog := filepath.Join(t.TempDir(), "urls")

					dataDir := t.TempDir()
					cmd := exec.Command("bash", abs(t, a.Bootstrap))
					cmd.Stdin = strings.NewReader(`{"hook_event_name":"SessionStart"}`)
					cmd.Env = append(hookEnv(t, dataDir),
						"PATH="+shimDir+string(os.PathListSeparator)+os.Getenv("PATH"),
						"SERVE="+serveDir,
						"URLLOG="+urlLog,
						"DASH0_VERSION="+value,
					)
					raw, err := cmd.CombinedOutput()
					out := string(raw)

					assert.NoError(t, err,
						"a rejected DASH0_VERSION must not end the hook non-zero:\n%s", out)
					assert.Contains(t, out, "ignoring",
						"%s accepted %q, which reaches a URL and a path and so has to be "+
							"validated:\n%s", a.Bootstrap, value, out)

					// The message alone would not tell a fallback from an exit, which
					// turns the `v0.1.25` typo into a session with no telemetry.
					assert.Contains(t, out, "STUB-RAN",
						"%s stopped instead of falling back to %s; a typo here must not "+
							"cost the session its telemetry:\n%s",
						a.Bootstrap, pinned, out)

					urls, readErr := os.ReadFile(urlLog)
					require.NoError(t, readErr, "the shim recorded no request at all")
					for _, u := range strings.Fields(string(urls)) {
						assert.Contains(t, u, "/v"+pinned+"/",
							"%s requested %q, which is not the pinned version", a.Bootstrap, u)
						assert.NotContains(t, u, "attacker",
							"%s let DASH0_VERSION retarget the download: %q", a.Bootstrap, u)
					}
				})
			}

			// Or the guard is an off switch. The pin itself rather than a literal,
			// which stops being a published version after the next release.
			t.Run("accepts the pinned version", func(t *testing.T) {
				serveDir, _ := stageFakeRelease(t, a.releaseAsset())
				shimDir := fakeCurl(t, serveDir)

				cmd := exec.Command("bash", abs(t, a.Bootstrap))
				cmd.Stdin = strings.NewReader(`{"hook_event_name":"SessionStart"}`)
				cmd.Env = append(hookEnv(t, t.TempDir()),
					"PATH="+shimDir+string(os.PathListSeparator)+os.Getenv("PATH"),
					"SERVE="+serveDir,
					"DASH0_VERSION="+pinned,
				)
				raw, err := cmd.CombinedOutput()
				out := string(raw)

				assert.NoError(t, err, "output: %s", out)
				assert.NotContains(t, out, "ignoring",
					"%s rejected %s, its own pinned version", a.Bootstrap, pinned)
				assert.Contains(t, out, "STUB-RAN", "the binary never ran:\n%s", out)
			})
		})
	}
}
