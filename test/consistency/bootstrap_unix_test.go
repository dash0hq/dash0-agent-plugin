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

	"github.com/dash0hq/dash0-agent-plugin/test/helpers/hookcheck"
	"github.com/dash0hq/dash0-agent-plugin/test/helpers/pluginrepo"
)

// The bootstrap contracts only a POSIX host can run, and the fixtures they need.
//
// Two reasons a contract lands here. The download contracts put a shim named
// `curl` on PATH, and Git Bash resolves its own curl.exe ahead of an
// extensionless file. The fail-open contracts make a directory unwritable through
// its mode bits, which Windows does not honour that way.
//
// bootstrap_windows_test.go carries the same contracts against the PowerShell
// bootstraps, where a real curl.exe shim does work.

// The cache filename a bootstrap derives has to match the one it downloads to, or
// every hook event re-downloads. Pre-placing a stub under the derived name and
// asserting it runs is the only check that the two agree, and under a faked Git
// Bash uname the only check that the Windows name is right.
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

	for _, a := range failOpenBootstraps(t) {
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

// The same contract for Claude, which differs in two ways: CLAUDE_PLUGIN_DATA
// rather than DASH0_PLUGIN_DATA, and the unprefixed legacy cache name so existing
// installs do not re-download.
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

			// hookEnv rather than os.Environ, because an exported DASH0_VERSION shifts
			// the version spliced into the cache name and the staged stub becomes
			// unfindable. The curl shim serves an empty directory, so the run cannot
			// reach the network either.
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

// Concurrent invocations against a cold cache all succeed and converge on one
// correct binary. Fetch, checksum, chmod, rename and exec run end to end, eight at
// once, leaving one intact file.
//
// Staggered rather than simultaneous. The damaging overlap is one process exec'ing
// while a later one truncates the same path, which a burst of identical starts
// mostly misses.
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

			// Resolved here rather than inside the worker: abs asserts, and testing
			// documents FailNow as usable only from the goroutine running the test.
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
				// These exit 0 whatever happens, so STUB-RAN is what proves the install
				// completed and the file ran.
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

// Every bootstrap hands the binary its argv and its stdin unchanged.
//
// Dropping the "$@" from the closing `exec "$BINARY" "$@"` is invisible to every
// other check here: the binary still runs, the cache is still correct, the exit
// code is still 0. Copilot takes its event name from argv, so a dropped argv
// unnames every event it reports.
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

// No bootstrap ends a hook with a non-zero exit.
//
// Behavioural rather than textual, because a grep for `exit [1-9]` cannot see a
// `set -e` exit, a `:?` expansion or a failing exec. An unwritable data directory
// poisons the first thing every bootstrap does.
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
			// cwd inside the unwritable directory too, so nothing in this checkout can
			// decide the outcome. Harness.configFile reads a relative
			// <ConfigDir>/dash0-agent-plugin.local.md against the working directory, so
			// a run left here would consult test/consistency/<ConfigDir>/ instead.
			cmd.Dir = ro
			cmd.Stdin = strings.NewReader(`{"hook_event_name":"SessionStart"}`)
			cmd.Env = hookEnv(t, filepath.Join(ro, "data"))

			out, err := cmd.CombinedOutput()
			assert.NoError(t, err,
				"%s exited non-zero when its data directory could not be created, which "+
					"takes the user's turn down with it:\n%s", a.Bootstrap, out)
			// Exit 0 alone holds on a bootstrap that failed open earlier for an
			// unrelated reason, so this pins which path it took.
			assert.Contains(t, string(out), "could not create",
				"%s exited 0 without reporting the failed mkdir, so this asserted nothing "+
					"about the fail-open path:\n%s", a.Bootstrap, out)
		})
	}
}

// A cached binary that will not run does not loop.
//
// Deleting it would make the next hook re-download the asset, fail to exec it, and
// repeat, at a multi-MB fetch per tool call. Keeping it costs one failed exec.
//
// The stand-in names an interpreter that does not exist. It must not be an empty
// or garbage file instead: bash answers ENOEXEC by re-reading the file as a shell
// script, and an empty script exits 0, which would satisfy both assertions below
// on a bootstrap that never reached an exec.
func TestAnUnrunnableCachedBinaryIsKept(t *testing.T) {
	const interpreter = "/nonexistent/interpreter"
	body := []byte("#!" + interpreter + "\n")

	for _, a := range Agents {
		t.Run(a.Label, func(t *testing.T) {
			serveDir, _ := stageFakeRelease(t, a.releaseAsset())
			shimDir := fakeCurl(t, serveDir)

			// Pre-placed under the name the bootstrap derives, so the cache is warm.
			// The curl shim stays on PATH so a re-download cannot reach the network.
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

			// The two assertions at the end hold on a bootstrap that did nothing at
			// all, so bash reporting the file it could not run is what proves it tried.
			//
			// The path, not the interpreter. bash's wording for a missing interpreter
			// is not portable: it names it on macOS ("bad interpreter:
			// /nonexistent/interpreter") and does not on the GNU/Linux runners
			// ("cannot execute: required file not found"). Both name the file they
			// were asked to run, and no other message here carries this absolute
			// path — a download reports the asset name and the URL.
			require.Contains(t, string(out), cached,
				"the bootstrap never tried to exec the cached binary, so this asserted "+
					"nothing about what it does when the exec fails:\n%s", out)

			// Exit 0 for Claude only, which is a finding rather than a decision.
			// Claude sets `shopt -s execfail` and reaches its fail_open after a
			// failed exec; the three that share a body end in a bare `exec`, and
			// bash exits 126 first.
			if !a.SharesBootstrapBody {
				assert.NoError(t, err, "an unrunnable cached binary must still exit 0:\n%s", out)
			}

			// Whatever the exit code, the file stays.
			got, err := os.ReadFile(cached)
			require.NoError(t, err, "the bad binary was deleted, so the next hook re-downloads it")
			assert.Equal(t, body, got, "the bad binary was replaced, so every hook re-downloads it")
		})
	}
}

// DASH0_VERSION reaches both a download URL and a filesystem path, so an
// unvalidated value injects into two places at once. `curl` squashes `..` in a
// path, so `../../../attacker/repo/releases/download/v9` retargets BASE_URL at
// another repository, and checksums.txt comes from the same base, so verification
// passes against the attacker's own manifest. A hook runs inside an agent session,
// so a project .envrc is enough to reach it.
//
// All four bootstraps, because each carries its own copy of the guard and a fix
// missed in one file is the drift worth catching.
//
// The shim records every URL, so the assertion is that nothing was requested from
// the injected path. Asserting only that the hook kept running would pass with the
// guard removed.
func TestBootstrapsRefuseAVersionOverrideThatIsNotAVersion(t *testing.T) {
	bad := []string{
		"../../../../attacker/repo/releases/download/v9",
		"../../etc",
		// The realistic typo, and the reason rejection must not end the hook: a
		// leading v is wrong here and right in a tag.
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
					// override 404s and falling back reaches the stub. Neither can
					// reach the network.
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

					// Rejecting the value has to leave the hook running on the pinned
					// version. Asserting only on the message would not tell a fallback
					// apart from an exit, which turns the `v0.1.25` typo into a session
					// with no telemetry.
					assert.Contains(t, out, "STUB-RAN",
						"%s stopped instead of falling back to %s; a typo here must not "+
							"cost the session its telemetry:\n%s",
						a.Bootstrap, pinned, out)

					// What the guard is for: every request names the pinned version and
					// none names the injected value.
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

			// A real version must still be honoured, or the guard is an off switch.
			// The pin itself rather than a literal, which stops being a published
			// version after the next release.
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

// A hook must never end non-zero, and the exit code is what only another process
// can see.
//
// hookcheck.FailOpen calls each run() in-process and proves it returns an error
// rather than exiting. The other half is that main drops that error instead of
// re-raising it, and no in-process call can observe it: main ends its process
// either way. So this runs the real binary, under the bootstrap a hook actually
// invokes, which is the exit code the agent sees.
//
// The rejected payload is the case that matters. On a payload run() serves it
// returns nil and main falls off the end, so every exit-code check passes whether
// or not the error branch carries an os.Exit. Claude's did once.
//
// Claude has no .ps1 twin, so bootstrap_windows_test.go covers three runtimes
// there and this covers four here.
func TestBootstrapsExitZeroForTheRealBinary(t *testing.T) {
	for _, a := range Agents {
		t.Run(a.Label, func(t *testing.T) {
			spec, ok := hookcheck.Specs[a.Label]
			require.True(t, ok,
				"no hookcheck.Spec for %s, so this runtime's payload cannot be built", a.Label)

			// Staged under the name the bootstrap derives, so the cache is warm and
			// the download path is not reached.
			dataDir := t.TempDir()
			cached := filepath.Join(dataDir, "bin", a.cacheName(t))
			require.NoError(t, os.MkdirAll(filepath.Dir(cached), 0o755))
			pluginrepo.CopyExecutable(t,
				pluginrepo.BuildBinary(t, pluginrepo.Root(t), "./cmd/"+a.Label+"-on-event"),
				cached)

			for i, c := range []struct {
				name, payload  string
				servesSession  bool
				wantParseError bool
			}{
				{name: "a payload run() rejects", payload: badPayload, wantParseError: true},
				{name: "a payload it can serve", servesSession: true},
			} {
				t.Run(c.name, func(t *testing.T) {
					require.NotEqual(t, c.servesSession, c.wantParseError,
						"%q must either serve a session or expect a parse error", c.name)

					payload := c.payload
					if c.servesSession {
						payload = spec.SessionStart(fmt.Sprintf("exit-code-%d", i))
					}

					var args []string
					if spec.ArgvEvent != "" {
						args = append(args, spec.ArgvEvent)
					}
					cmd := exec.Command("bash", append([]string{abs(t, a.Bootstrap)}, args...)...)
					cmd.Stdin = strings.NewReader(payload)
					// No endpoint, so nothing leaves the machine and no case waits out
					// the connectivity timeout.
					// The curl shim serves an empty directory, so a bootstrap that
					// derived a different name 404s instead of fetching the published
					// release and asserting the exit code of shipped code.
					cmd.Env = append(hookEnv(t, dataDir),
						"PATH="+fakeCurl(t, t.TempDir())+string(os.PathListSeparator)+os.Getenv("PATH"),
						"DASH0_OTLP_URL=",
						a.Harness.EnvPrefix+"_PLUGIN_OPTION_OTLP_URL=",
					)
					// Inside the data directory, so a relative <ConfigDir>/… lookup
					// cannot reach this checkout.
					cmd.Dir = dataDir

					out, err := cmd.CombinedOutput()
					assert.NoError(t, err,
						"%s exited non-zero. Claude and Copilot print that as a hook error on "+
							"every event, Cursor reads it from a tool-gating hook as a refusal, "+
							"and Codex sits between the user and their own tool calls:\n%s",
						a.Bootstrap, out)

					want := "telemetry is not active"
					if c.wantParseError {
						want = "parsing JSON from stdin"
					}
					assert.Contains(t, string(out), want,
						"the staged binary never said %q, so %s exited 0 without "+
							"running it:\n%s", want, a.Bootstrap, out)
					entries, err := os.ReadDir(filepath.Dir(cached))
					require.NoError(t, err)
					names := make([]string, 0, len(entries))
					for _, e := range entries {
						names = append(names, e.Name())
					}
					assert.Equal(t, []string{a.cacheName(t)}, names,
						"%s left something other than the staged binary in its cache",
						a.Bootstrap)
				})
			}
		})
	}
}
