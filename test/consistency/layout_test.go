// SPDX-FileCopyrightText: Copyright 2026 Dash0 Inc.
// SPDX-License-Identifier: Apache-2.0

package consistency

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
)

// Claude and Cursor auto-discover commands/, skills/ and hooks/ at the plugin
// root, which is the repo root for both, so a directory of either name would load
// one runtime's assets into another. They live under the per-agent directories,
// declared by the manifests (TestManifestDeclaresSkills).
func TestNoAutoDiscoveredRootDirs(t *testing.T) {
	for _, dir := range []string{"commands", "skills", "hooks"} {
		t.Run(dir, func(t *testing.T) {
			_, err := os.Stat(abs(t, dir))
			assert.True(t, os.IsNotExist(err),
				"root %s/ must not exist; runtime assets belong under the per-agent directories", dir)
		})
	}
}

// The runtime directories ship as-is: Copilot's plugin root is the copilot/
// subtree, so an install copies the whole thing to the user's machine. A capture
// harness left there goes with it, recorded payloads included.
func TestRuntimePackagesShipNoCaptureHarness(t *testing.T) {
	for _, a := range Agents {
		t.Run(a.Label, func(t *testing.T) {
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
