// SPDX-FileCopyrightText: Copyright 2026 Dash0 Inc.
// SPDX-License-Identifier: Apache-2.0

package consistency

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestManifestVersionParity locks each manifest to the release its bootstrap
// pins. The bootstrap downloads the binary for VERSION=, so a bumped manifest
// with a stale bootstrap ships old code under a new version number.
// scripts/version.sh bumps both; this catches a hand-edit that drifts.
func TestManifestVersionParity(t *testing.T) {
	for _, a := range Agents {
		t.Run(a.Label, func(t *testing.T) {
			assert.Equal(t, a.manifestVersion(t), a.bootstrapVersion(t),
				"%s version must match VERSION= in %s", a.Manifest, a.Bootstrap)
		})
	}
}

// TestManifestName pins the install id. Both marketplaces resolve a plugin by
// this name, so a rename in one manifest breaks that runtime's install with a
// bare "plugin not found".
func TestManifestName(t *testing.T) {
	for _, a := range Agents {
		t.Run(a.Label, func(t *testing.T) {
			assert.Equal(t, pluginName, a.manifest(t)["name"], "%s name", a.Manifest)
		})
	}
}

// TestManifestVersionsAgree keeps the four manifests on one version. They are
// released together from one tag, so a per-runtime version would make the
// release assets ambiguous.
func TestManifestVersionsAgree(t *testing.T) {
	want := Agents[0].manifestVersion(t)
	for _, a := range Agents[1:] {
		assert.Equal(t, want, a.manifestVersion(t),
			"%s must carry the same version as %s", a.Manifest, Agents[0].Manifest)
	}
}

// TestManifestDeclaresHooks checks the manifest points at the hooks file the
// runtime actually reads. Cursor is the deliberate exception: it silently ignores
// `hooks` in a local-plugin manifest, so the field must stay absent and
// install-cursor.sh reads the file directly. Either way the file must exist,
// because it is the source of truth for which events get instrumented.
func TestManifestDeclaresHooks(t *testing.T) {
	for _, a := range Agents {
		t.Run(a.Label, func(t *testing.T) {
			declared, ok := a.manifest(t)["hooks"].(string)

			if !a.ManifestDeclaresHooks {
				assert.False(t, ok,
					"%s must NOT declare hooks; this runtime ignores the field for local plugins and the installer reads %s directly",
					a.Manifest, a.Hooks)
			} else {
				require.True(t, ok, "%s must declare hooks", a.Manifest)
				assert.Equal(t, filepath.Clean(abs(t, a.Hooks)), filepath.Clean(a.pkgPath(t, declared)),
					"%s hooks (%q) must resolve to %s", a.Manifest, declared, a.Hooks)
			}

			assert.FileExists(t, abs(t, a.Hooks))
		})
	}
}

// TestManifestDeclaresSkills pins the skills path. Claude and Cursor
// auto-discover a skills/ directory at the plugin root when the manifest does
// not override it, which would ship the other runtime's assets, so the override
// is load-bearing rather than cosmetic.
func TestManifestDeclaresSkills(t *testing.T) {
	for _, a := range Agents {
		t.Run(a.Label, func(t *testing.T) {
			declared, ok := a.manifest(t)["skills"].(string)

			if a.ManifestSkills == "" {
				assert.False(t, ok, "%s declares skills %q but the descriptor expects none", a.Manifest, declared)
				return
			}

			require.True(t, ok, "%s must declare skills", a.Manifest)
			assert.Equal(t, a.ManifestSkills, declared, "%s skills", a.Manifest)
			assert.DirExists(t, a.pkgPath(t, declared),
				"%s skills references a missing directory", a.Manifest)
		})
	}
}

// TestManifestDeclaresCommands guards the same auto-discovery trap as skills.
// Cursor declares an empty list purely to block it, so "declared" is the
// assertion and the list may legitimately be empty.
func TestManifestDeclaresCommands(t *testing.T) {
	for _, a := range Agents {
		t.Run(a.Label, func(t *testing.T) {
			declared, ok := a.manifest(t)["commands"]

			if !a.ManifestCommandsDeclared {
				assert.False(t, ok, "%s declares commands but the descriptor expects none", a.Manifest)
				return
			}

			require.True(t, ok,
				"%s must declare commands (use []) to block auto-discovery of a root commands/ directory", a.Manifest)
			entries, ok := declared.([]any)
			require.True(t, ok, "%s commands must be a list", a.Manifest)
			for _, e := range entries {
				path, ok := e.(string)
				require.True(t, ok, "%s commands entries must be strings", a.Manifest)
				assert.FileExists(t, a.pkgPath(t, path), "%s commands entry %q", a.Manifest, path)
			}
		})
	}
}

// TestManifestUserConfig keeps Claude Code's credential mechanism out of the
// other manifests. userConfig is a Claude Code plugin field; declaring it
// elsewhere reads as a supported config path that the runtime never populates.
// Those runtimes deliver credentials through the bootstrap instead, which
// internal/harness and the live turns in test/e2e cover.
func TestManifestUserConfig(t *testing.T) {
	withUserConfig := agentsWith(t, 1, func(a Agent) bool { return a.ManifestUserConfig })
	assert.Equal(t, "claude", withUserConfig[0].Label)

	for _, a := range Agents {
		t.Run(a.Label, func(t *testing.T) {
			options, ok := a.manifest(t)["userConfig"].(map[string]any)
			if !a.ManifestUserConfig {
				assert.False(t, ok, "userConfig is not a valid %s plugin.json field", a.Label)
				return
			}
			require.True(t, ok, "%s must declare userConfig", a.Manifest)
			assert.NotEmpty(t, options)
		})
	}
}
