// SPDX-FileCopyrightText: Copyright 2026 Dash0 Inc.
// SPDX-License-Identifier: Apache-2.0

// Package credentials asserts that a configured Dash0 token reaches the
// Authorization header of a real OTLP request, for every runtime and from every
// source the plugin documents.
//
// This is the one contract a user notices immediately when it breaks and cannot
// see when it does. Every hook fails open, so a token that never arrives costs
// no exit code, no error in the session and no warning: the collector answers
// 401 and the run looks perfectly healthy while reporting nothing. qa/setup.md
// says the same thing about the QA token.
//
// Unit tests in internal/harness cover the resolution order. What they cannot
// cover is the whole process doing it: a hook binary reads its configuration
// from a relative path resolved against its own working directory, memoizes the
// answer, and exports asynchronously before exiting. Only running it proves the
// token survives that.
//
// Untagged and offline, on purpose. It needs no agent CLI and no credentials, so
// it runs in the build-test job on all three operating systems and on fork PRs,
// which get no secrets. Putting these assertions behind the e2e tag would leave
// Cursor uncovered, since it has no canary, and every runtime uncovered for
// external contributors.
package credentials

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/dash0hq/dash0-agent-plugin/test/helpers/otlpcapture"
	"github.com/dash0hq/dash0-agent-plugin/test/helpers/pluginrepo"
	"github.com/dash0hq/dash0-agent-plugin/test/helpers/testenv"
)

// runtime describes one entrypoint: where it reads configuration and how it is
// told which event fired.
type runtimeUnderTest struct {
	// Label is the cmd/<label>-on-event directory and the binary name.
	Label string
	// EnvPrefix is the <PREFIX>_PLUGIN_OPTION_* and <PREFIX>_PLUGIN_DATA prefix.
	EnvPrefix string
	// ConfigDir is the agent's dot-directory under the home, holding
	// dash0-agent-plugin.local.md.
	ConfigDir string
	// ArgvEvent is the event name passed as argv rather than in the payload.
	// Copilot only: its payload carries camelCase keys and no hook_event_name.
	ArgvEvent string
}

// The four entrypoints. Copilot is the one that differs: the event name reaches
// it through argv because its payload has no hook_event_name field.
var runtimes = []runtimeUnderTest{
	{Label: "claude", EnvPrefix: "CLAUDE", ConfigDir: ".claude"},
	{Label: "cursor", EnvPrefix: "CURSOR", ConfigDir: ".cursor"},
	{Label: "codex", EnvPrefix: "CODEX", ConfigDir: ".codex"},
	{Label: "copilot", EnvPrefix: "COPILOT", ConfigDir: ".copilot", ArgvEvent: "sessionStart"},
}

// configName is the file every runtime reads its configuration from, under
// ConfigDir. It matches internal/config.Name.
const configName = "dash0-agent-plugin.local.md"

// TestTheConfiguredTokenReachesTheWire drives each entrypoint once per
// credential source and reads the Authorization header off the request that
// arrives.
//
// SessionStart is the event under test because it is the one that exports
// without any prior state: the connectivity check fires on it, so a single
// process produces a request. Later events need a turn already open.
func TestTheConfiguredTokenReachesTheWire(t *testing.T) {
	for _, r := range runtimes {
		bin := buildEntrypoint(t, r.Label)

		for _, source := range []struct {
			name string
			// apply configures the token and returns any extra environment.
			apply func(t *testing.T, home, token string) []string
		}{
			{
				// What every installer writes, and what a user edits by hand.
				name: "configuration file",
				apply: func(t *testing.T, home, token string) []string {
					t.Helper()
					writeConfig(t, home, r.ConfigDir, token)
					return nil
				},
			},
			{
				// What a managed rollout sets, and what the Cursor QA driver uses
				// because Cursor passes the launcher's environment to hooks.
				name: "plugin option environment variable",
				apply: func(_ *testing.T, _, token string) []string {
					return []string{r.EnvPrefix + "_PLUGIN_OPTION_AUTH_TOKEN=" + token}
				},
			},
		} {
			t.Run(r.Label+"/"+source.name, func(t *testing.T) {
				const token = "credential-contract-token"

				cap, srv := otlpcapture.New(t)
				defer srv.Close()

				home := t.TempDir()
				extra := source.apply(t, home, token)

				out, err := runHook(t, bin, r, home, srv.URL, extra...)
				require.NoError(t, err,
					"the hook exited non-zero, so this asserted nothing about the header:\n%s", out)

				requests := waitForTrace(t, cap)
				require.NotEmpty(t, requests,
					"%s-on-event sent no OTLP request, so there is no header to check. "+
						"Output:\n%s", r.Label, out)

				for i, req := range requests {
					assert.Equal(t, "Bearer "+token, req.Auth,
						"request %d carried the wrong Authorization header. Every hook fails "+
							"open, so in production this is a 401 and a session that reports "+
							"nothing while looking healthy. Output:\n%s", i+1, out)
				}
			})
		}
	}
}

// TestDash0AuthTokenIsNotHonoured pins the one exclusion in the resolution
// order: DASH0_AUTH_TOKEN must never be read.
//
// Harness.authToken drops the DASH0_* fallback that every other option has,
// because the agent hands that environment to tool subprocesses; so a token
// accepted from there would be readable by anything the model chooses to run.
// internal/harness covers the resolution in-process; this covers the shipped
// binary, where the mistake would actually ship.
//
// The endpoint still comes from a plugin option, so a request is sent either
// way. That is what separates "the variable was ignored" from "nothing ran".
func TestDash0AuthTokenIsNotHonoured(t *testing.T) {
	for _, r := range runtimes {
		t.Run(r.Label, func(t *testing.T) {
			bin := buildEntrypoint(t, r.Label)

			cap, srv := otlpcapture.New(t)
			defer srv.Close()

			out, err := runHook(t, bin, r, t.TempDir(), srv.URL,
				"DASH0_AUTH_TOKEN=leaked-through-the-tool-environment")
			require.NoError(t, err, "output:\n%s", out)

			requests := waitForTrace(t, cap)
			require.NotEmpty(t, requests,
				"%s-on-event sent no request, so this proves nothing about "+
					"DASH0_AUTH_TOKEN being ignored rather than unreachable. Output:\n%s",
				r.Label, out)

			for i, req := range requests {
				assert.NotContains(t, req.Auth, "leaked-through-the-tool-environment",
					"request %d authenticated with DASH0_AUTH_TOKEN. The agent puts that "+
						"variable in the environment of every tool subprocess, so the token "+
						"would be readable by anything the model runs", i+1)
			}
		})
	}
}

// buildEntrypoint compiles one cmd/<label>-on-event and returns its path. Built
// per subtest tree rather than shared, because t.TempDir is per-test; the Go
// build cache makes the repeats cheap.
func buildEntrypoint(t *testing.T, label string) string {
	t.Helper()

	bin := filepath.Join(t.TempDir(), label+"-on-event"+pluginrepo.ExeSuffix())
	cmd := exec.Command("go", "build", "-o", bin, "../../cmd/"+label+"-on-event")
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "building %s-on-event: %s", label, out)
	return bin
}

// writeConfig puts a configuration file where the runtime's home lookup finds
// it. The frontmatter shape is internal/config's.
func writeConfig(t *testing.T, home, configDir, token string) {
	t.Helper()

	dir := filepath.Join(home, configDir)
	require.NoError(t, os.MkdirAll(dir, 0o755))
	body := fmt.Sprintf("---\nauth_token: %q\n---\n", token)
	require.NoError(t, os.WriteFile(filepath.Join(dir, configName), []byte(body), 0o600))
}

// runHook executes one hook event against otlpURL.
//
// The environment is built from scratch rather than from os.Environ, so a
// developer's own exported DASH0_* or *_PLUGIN_OPTION_* cannot decide the
// result. PATH is passed because the binary shells out to git for the repository
// identity.
func runHook(t *testing.T, bin string, r runtimeUnderTest, home, otlpURL string, extra ...string) (string, error) {
	t.Helper()

	var args []string
	if r.ArgvEvent != "" {
		args = append(args, r.ArgvEvent)
	}
	cmd := exec.Command(bin, args...)

	// The payload names the event for the three that read it from stdin, and is
	// harmless for Copilot, which takes it from argv.
	cmd.Stdin = strings.NewReader(`{"hook_event_name":"SessionStart","session_id":"credential-contract"}`)

	// A data directory inside the home, so nothing is written outside it. Claude
	// requires this variable rather than falling back.
	data := filepath.Join(home, "state")
	require.NoError(t, os.MkdirAll(data, 0o755))

	cmd.Env = append(testenv.Home(home),
		"PATH="+os.Getenv("PATH"),
		r.EnvPrefix+"_PLUGIN_DATA="+data,
		r.EnvPrefix+"_PLUGIN_OPTION_OTLP_URL="+otlpURL,
		r.EnvPrefix+"_PLUGIN_OPTION_DATASET=default",
	)
	cmd.Env = append(cmd.Env, extra...)

	// Working directory inside the home too. Harness.configFile reads a RELATIVE
	// <ConfigDir>/… first, so a run from this package's own directory would
	// consult the checkout before the throwaway home.
	cmd.Dir = home

	out, err := cmd.CombinedOutput()
	return string(out), err
}

// waitForTrace polls until a trace request arrives or the deadline passes.
//
// The export is asynchronous and the process exits without waiting for it, so
// the request can land after CombinedOutput returns. A poll rather than a fixed
// sleep: a sleep long enough to be safe on a loaded CI runner is dead time on
// every green run.
func waitForTrace(t *testing.T, cap *otlpcapture.Capture) []otlpcapture.Request {
	t.Helper()

	deadline := time.Now().Add(15 * time.Second)
	for {
		var traces []otlpcapture.Request
		for _, r := range cap.Requests() {
			if r.Path == otlpcapture.TracesPath {
				traces = append(traces, r)
			}
		}
		if len(traces) > 0 {
			return traces
		}
		if time.Now().After(deadline) {
			return nil
		}
		time.Sleep(50 * time.Millisecond)
	}
}
