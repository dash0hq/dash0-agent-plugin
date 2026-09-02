// SPDX-FileCopyrightText: Copyright 2026 Dash0 Inc.
// SPDX-License-Identifier: Apache-2.0

package pluginrepo

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestLookAgentSkipsOurLaunchWrapper pins the one thing LookAgent does that
// exec.LookPath does not: it walks past the plugin's own launcher and keeps
// looking. Without it a canary runs the wrapper, which redirects the native-OTel
// file the test is about to read, and the failure names everything except the
// cause.
func TestLookAgentSkipsOurLaunchWrapper(t *testing.T) {
	wrapperDir := t.TempDir()
	realDir := t.TempDir()

	// The fixture name is per platform, because LookAgent resolves per platform.
	// On Windows it probes name+PATHEXT and never a bare name, so extensionless
	// fixtures make this test look for a file it will not consider: it reported a
	// miss and the require below failed, on the one leg nobody runs locally.
	// PATHEXT is pinned so the probe order is the fixture's regardless of host.
	name, body := "someagent", "#!/bin/sh\n"
	if runtime.GOOS == "windows" {
		t.Setenv("PATHEXT", ".CMD")
		name, body = "someagent.cmd", "@echo off\r\n"
	}

	wrapper := filepath.Join(wrapperDir, name)
	require.NoError(t, os.WriteFile(wrapper,
		[]byte(body+"# dash0-agent-plugin launcher\n"), 0o755))
	real := filepath.Join(realDir, name)
	require.NoError(t, os.WriteFile(real, []byte(body+"exit 0\n"), 0o755))

	// Wrapper first, as PATH really orders it: ~/.local/bin comes ahead of the
	// npm prefix on every machine dash0-configure has run on.
	t.Setenv("PATH", wrapperDir+string(os.PathListSeparator)+realDir)

	got, err := LookAgent(t, "someagent")
	require.NoError(t, err)
	assert.Equal(t, real, got, "LookAgent returned the launch wrapper")
}

// And it reports a miss rather than an empty path, so a caller's require.NoError
// carries the reason.
func TestLookAgentReportsAMiss(t *testing.T) {
	t.Setenv("PATH", t.TempDir())

	_, err := LookAgent(t, "someagent")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "someagent")
}
