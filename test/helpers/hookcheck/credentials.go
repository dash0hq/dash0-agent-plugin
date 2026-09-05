// SPDX-FileCopyrightText: Copyright 2026 Dash0 Inc.
// SPDX-License-Identifier: Apache-2.0

package hookcheck

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/dash0hq/dash0-agent-plugin/internal/harness"
	"github.com/dash0hq/dash0-agent-plugin/test/helpers/otlpcapture"
)

// configName is the file every runtime reads its configuration from, under
// Harness.ConfigDir. It matches internal/config.Name.
const configName = "dash0-agent-plugin.local.md"

// Credentials drives the entrypoint once per credential source and reads the
// Authorization header off the request that arrives.
//
// This is the one contract a user notices immediately when it breaks and cannot
// see when it does. Every hook fails open, so a token that never arrives costs no
// exit code, no error in the session and no warning: the collector answers 401 and
// the run looks perfectly healthy while reporting nothing.
//
// internal/harness covers the resolution order in isolation. What it cannot cover
// is the entrypoint doing it: run() reads its configuration from a path resolved
// against the working directory and memoizes the answer, so only calling it proves
// the token survives that.
//
// A session start is the event under test because it is the one that exports with
// no prior state: the connectivity check fires on it, so one call produces a
// request. Later events need a turn already open.
func Credentials(t *testing.T, s Spec, run Run) {
	t.Helper()

	for _, source := range []struct {
		name string
		// apply configures the token, given the throwaway home.
		apply func(t *testing.T, s Spec, home, token string)
	}{
		{
			// What every installer writes, and what a user edits by hand.
			name: "configuration file",
			apply: func(t *testing.T, s Spec, home, token string) {
				t.Helper()
				dir := filepath.Join(home, s.Harness.ConfigDir)
				require.NoError(t, os.MkdirAll(dir, 0o755))
				// The frontmatter shape is internal/config's.
				body := fmt.Sprintf("---\nauth_token: %q\n---\n", token)
				require.NoError(t, os.WriteFile(filepath.Join(dir, configName), []byte(body), 0o600))
			},
		},
		{
			// What a managed rollout sets, and what the Cursor QA driver uses because
			// Cursor passes the launcher's environment to hooks.
			name: "plugin option environment variable",
			apply: func(t *testing.T, s Spec, _, token string) {
				t.Helper()
				t.Setenv(s.Harness.EnvPrefix+"_PLUGIN_OPTION_AUTH_TOKEN", token)
			},
		},
	} {
		t.Run(source.name, func(t *testing.T) {
			const token = "credential-contract-token"

			cap, srv := otlpcapture.New(t)
			defer srv.Close()

			// A data directory under the home, so nothing is written outside it, and
			// Claude requires the variable rather than falling back.
			home := s.isolate(t, filepath.Join(t.TempDir(), "state"))
			source.apply(t, s, home, token)
			t.Setenv(s.Harness.EnvPrefix+"_PLUGIN_OPTION_OTLP_URL", srv.URL)
			t.Setenv(s.Harness.EnvPrefix+"_PLUGIN_OPTION_DATASET", "default")
			// Belt and braces. isolate already emptied the cache and nothing
			// between it and run() resolves the configuration: one apply writes a
			// file, the other sets an environment variable. This is here so an
			// apply that one day reads the configuration cannot cache an answer
			// from before the token was written.
			harness.ResetConfig()

			require.NoError(t, s.call(t, run, s.SessionStart("credential-contract")),
				"run() returned an error, so this asserted nothing about the header")

			requests := traces(cap)
			require.NotEmpty(t, requests,
				"%s-on-event sent no OTLP request, so there is no header to check", s.Label)

			for i, req := range requests {
				assert.Equal(t, "Bearer "+token, req.Auth,
					"request %d carried the wrong Authorization header. Every hook fails "+
						"open, so in production this is a 401 and a session that reports "+
						"nothing while looking healthy", i+1)
			}
		})
	}
}

// Dash0AuthTokenIsNotHonoured pins the one exclusion in the resolution order:
// DASH0_AUTH_TOKEN must never be read.
//
// Harness.authToken drops the DASH0_* fallback that every other option has,
// because the agent hands that environment to tool subprocesses, so a token
// accepted from there would be readable by anything the model chooses to run.
//
// The endpoint still comes from a plugin option, so a request is sent either way.
// That is what separates "the variable was ignored" from "nothing ran".
func Dash0AuthTokenIsNotHonoured(t *testing.T, s Spec, run Run) {
	t.Helper()

	cap, srv := otlpcapture.New(t)
	defer srv.Close()

	s.isolate(t, filepath.Join(t.TempDir(), "state"))
	t.Setenv("DASH0_AUTH_TOKEN", "leaked-through-the-tool-environment")
	t.Setenv(s.Harness.EnvPrefix+"_PLUGIN_OPTION_OTLP_URL", srv.URL)
	t.Setenv(s.Harness.EnvPrefix+"_PLUGIN_OPTION_DATASET", "default")
	harness.ResetConfig()

	require.NoError(t, s.call(t, run, s.SessionStart("credential-contract")))

	requests := traces(cap)
	require.NotEmpty(t, requests,
		"%s-on-event sent no request, so this proves nothing about DASH0_AUTH_TOKEN "+
			"being ignored rather than unreachable", s.Label)

	for i, req := range requests {
		assert.NotContains(t, req.Auth, "leaked-through-the-tool-environment",
			"request %d authenticated with DASH0_AUTH_TOKEN. The agent puts that "+
				"variable in the environment of every tool subprocess, so the token "+
				"would be readable by anything the model runs", i+1)
	}
}

// traces is the trace requests the capture server received.
//
// No poll: otlp.SendTracesRequest is a plain HTTP call, so run() has returned only
// once the request landed.
func traces(cap *otlpcapture.Capture) []otlpcapture.Request {
	var out []otlpcapture.Request
	for _, r := range cap.Requests() {
		if r.Path == otlpcapture.TracesPath {
			out = append(out, r)
		}
	}
	return out
}
