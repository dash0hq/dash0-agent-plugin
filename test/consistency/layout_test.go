// SPDX-FileCopyrightText: Copyright 2026 Dash0 Inc.
// SPDX-License-Identifier: Apache-2.0

package consistency

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestNoAutoDiscoveredRootDirs keeps the runtime assets isolated per agent.
// Cursor auto-discovers commands/, skills/ and hooks/hooks.json at the plugin
// root when its manifest does not override them, and Claude auto-discovers
// skills/ and commands/. A directory of either name at the repo root would be
// picked up by whichever runtime is installed, so one agent's assets would load
// into another. Each runtime's assets live under its own directory instead, and
// the manifests declare them (see TestManifestDeclaresSkills).
func TestNoAutoDiscoveredRootDirs(t *testing.T) {
	for _, dir := range []string{"commands", "skills", "hooks"} {
		t.Run(dir, func(t *testing.T) {
			_, err := os.Stat(abs(t, dir))
			assert.True(t, os.IsNotExist(err),
				"root %s/ must not exist; runtime assets belong under the per-agent directories", dir)
		})
	}
}

// TestEveryEntrypointDrivesTheSharedContracts keeps the fail-open and credential
// contracts applied to all four runtimes.
//
// They cannot be table-driven from here: run() and main() are unexported members
// of a package main, so each entrypoint calls test/helpers/hookcheck from its own
// package. Coverage is then one file per directory, and a fifth runtime that
// forgets the file is uncovered in silence rather than red. This is the check that
// makes it red.
func TestEveryEntrypointDrivesTheSharedContracts(t *testing.T) {
	for _, a := range Agents {
		t.Run(a.Label, func(t *testing.T) {
			path := filepath.Join("cmd", a.Label+"-on-event", "hookcheck_test.go")
			body, err := os.ReadFile(abs(t, path))
			require.NoError(t, err,
				"%s is missing, so nothing asserts that this entrypoint fails open or "+
					"that a configured token reaches the wire", path)

			// Named individually: a file that only routes TestMain would satisfy a
			// FileExists, and each of these is a separate contract.
			for _, contract := range []string{
				"hookcheck.FailOpen", "hookcheck.Credentials",
				"hookcheck.Dash0AuthTokenIsNotHonoured",
			} {
				assert.Contains(t, string(body), contract+"(t, hookcheck."+capitalize(a.Label),
					"%s does not drive %s for this runtime", path, contract)
			}
		})
	}
}

// capitalize maps a runtime label to its hookcheck.Spec name.
func capitalize(label string) string {
	return strings.ToUpper(label[:1]) + label[1:]
}

// TestRuntimePackagesShipNoCaptureHarness keeps the dev-only capture harness out
// of the directories that ship.
//
// Copilot is where this bites hardest: its plugin root is the copilot/ subtree,
// so a marketplace or subpath install copies that whole directory to every user's
// machine, capture scripts and recorded payloads included. The rule covers all
// four runtime directories, because the harness is per runtime and belongs under
// test/capture/<runtime>/, where the two that exist already live.
func TestRuntimePackagesShipNoCaptureHarness(t *testing.T) {
	for _, a := range Agents {
		t.Run(a.Label, func(t *testing.T) {
			// The runtime's own directory, derived rather than declared: it is
			// where the bootstrap lives, which every Agent already names.
			pkg := filepath.Dir(a.Bootstrap)

			for _, dir := range []string{"capture", "captured"} {
				_, err := os.Stat(abs(t, filepath.Join(pkg, dir)))
				assert.True(t, os.IsNotExist(err),
					"%s/%s must not exist; a capture harness belongs under test/capture/%s/, "+
						"not in the package that ships", pkg, dir, a.Label)
			}
		})
	}
}
