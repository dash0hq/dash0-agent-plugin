// SPDX-FileCopyrightText: Copyright 2026 Dash0 Inc.
// SPDX-License-Identifier: Apache-2.0

package consistency

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
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
