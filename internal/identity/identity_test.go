// SPDX-FileCopyrightText: Copyright 2026 Dash0 Inc.
// SPDX-License-Identifier: Apache-2.0

package identity

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestResolveFallbackChain(t *testing.T) {
	tests := []struct {
		name       string
		gitName    string
		gitEmail   string
		osName     string
		osUsername string
		envUser    string
		ci         bool
		wantName   string
		wantSource string
		wantEmail  string
	}{{
		name: "git identity wins over every fallback",
		// The whole point of the chain: a configured git name is the user's own
		// answer and must never be second-guessed by the OS.
		gitName: "Guy Moses", gitEmail: "guy@dash0.com",
		osName: "someone else", osUsername: "guymoses",
		wantName: "Guy Moses", wantSource: SourceGit, wantEmail: "guy@dash0.com",
	}, {
		name:    "falls back to the OS display name",
		osName:  "Guy Moses",
		envUser: "guymoses",
		// GECOS carries a real full name on macOS, closest match to a git user.name.
		wantName: "Guy Moses", wantSource: SourceOS,
	}, {
		name:       "falls back to the OS username when there is no display name",
		osUsername: "guymoses",
		wantName:   "guymoses", wantSource: SourceOS,
	}, {
		name:     "falls back to the environment when os/user lookup failed",
		envUser:  "guymoses",
		wantName: "guymoses", wantSource: SourceOS,
	}, {
		name: "no identity anywhere yields no name and no source",
		// Must stay absent rather than becoming "" — a blank name is
		// indistinguishable from a real user whose name is blank.
		wantName: "", wantSource: "",
	}, {
		name: "git email survives a missing git name",
		// The two git settings are independent; one being unset must not
		// suppress the other.
		gitEmail: "guy@dash0.com", osUsername: "guymoses",
		wantName: "guymoses", wantSource: SourceOS, wantEmail: "guy@dash0.com",
	}, {
		name: "CI suppresses the OS fallback entirely",
		// "runner" would aggregate every job in the org into one bucket that
		// looks like a single impossibly productive developer.
		osName: "runner-display", osUsername: "runner", envUser: "runner", ci: true,
		wantName: "", wantSource: "",
	}, {
		name: "CI does not suppress a real git identity",
		// Git identity in CI is a deliberate configuration, not a shared account.
		gitName: "Guy Moses", ci: true,
		wantName: "Guy Moses", wantSource: SourceGit,
	}, {
		name: "generic account names are rejected without any CI marker",
		// Containers frequently set no CI variable at all.
		osUsername: "root", envUser: "docker",
		wantName: "", wantSource: "",
	}, {
		name:       "generic rejection is case insensitive",
		osUsername: "ROOT",
		wantName:   "", wantSource: "",
	}, {
		name: "a generic candidate does not shadow a usable later one",
		// Rejecting root must continue the chain, not abort it.
		osUsername: "root", envUser: "guymoses",
		wantName: "guymoses", wantSource: SourceOS,
	}, {
		name:   "whitespace-only candidates are not identities",
		osName: "   ", osUsername: "guymoses",
		wantName: "guymoses", wantSource: SourceOS,
	}, {
		name:     "resolved names are trimmed",
		osName:   "  Guy Moses  ",
		wantName: "Guy Moses", wantSource: SourceOS,
	}}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := resolve(tt.gitName, tt.gitEmail, tt.osName, tt.osUsername, tt.envUser, tt.ci)

			assert.Equal(t, tt.wantName, got.Name)
			assert.Equal(t, tt.wantSource, got.Source)
			assert.Equal(t, tt.wantEmail, got.Email)
		})
	}
}

// TestResolveSourceAccompaniesName guards the invariant the dashboard depends
// on: a source is never reported without a name, and a name never without a
// source. Either half alone is unattributable.
func TestResolveSourceAccompaniesName(t *testing.T) {
	inputs := []struct{ gitName, osName, osUsername, envUser string }{
		{gitName: "Guy Moses"},
		{osName: "Guy Moses"},
		{osUsername: "guymoses"},
		{envUser: "guymoses"},
		{},
		{osUsername: "root"},
	}

	for _, in := range inputs {
		for _, ci := range []bool{false, true} {
			got := resolve(in.gitName, "", in.osName, in.osUsername, in.envUser, ci)
			assert.Equal(t, got.Name == "", got.Source == "",
				"name and source must appear together (input %+v, ci=%v)", in, ci)
		}
	}
}

// TestResolveReadsTheHost exercises the real I/O path. It asserts only the
// invariants that hold on any host, since the values are machine-dependent.
func TestResolveReadsTheHost(t *testing.T) {
	got := Resolve()

	if got.Name != "" {
		require.Contains(t, []string{SourceGit, SourceOS}, got.Source)
	} else {
		assert.Empty(t, got.Source)
	}
	t.Logf("resolved Name=%q Email=%q Source=%q", got.Name, got.Email, got.Source)
}

// TestResolveFallsBackWithoutGitIdentity is the regression test for SIG-208:
// with git identity suppressed the way an unconfigured developer machine has
// it, we must still produce a name instead of going silent.
func TestResolveFallsBackWithoutGitIdentity(t *testing.T) {
	// Present as an interactive developer machine. Reading the production list
	// rather than restating it keeps this from drifting when a CI system is
	// added — the scenario under test is a developer's laptop, and this
	// assertion is the one that most needs to run on the machine gating merges.
	for _, key := range ciEnvVars {
		t.Setenv(key, "")
	}
	// Point git at empty config files at every level it would search.
	t.Setenv("GIT_CONFIG_GLOBAL", "/dev/null")
	t.Setenv("GIT_CONFIG_SYSTEM", "/dev/null")
	t.Chdir(t.TempDir())

	require.Empty(t, gitConfig("user.name"), "git identity should be suppressed")

	got := Resolve()
	assert.NotEmpty(t, got.Name, "an unconfigured developer must still be attributable")
	assert.Equal(t, SourceOS, got.Source)
}
