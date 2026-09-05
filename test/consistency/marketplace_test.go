// SPDX-FileCopyrightText: Copyright 2026 Dash0 Inc.
// SPDX-License-Identifier: Apache-2.0

package consistency

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/dash0hq/dash0-agent-plugin/test/helpers/pluginrepo"
)

// marketplaceEntry is the single plugin entry a marketplace manifest must carry,
// plus the source path it resolves to.
func (a Agent) marketplaceEntry(t *testing.T) (mp, entry map[string]any, source string) {
	t.Helper()

	mp = readJSON(t, abs(t, a.Marketplace))
	assert.Equal(t, a.MarketplaceName, mp["name"],
		"%s name is the `@<name>` suffix an install resolves", a.Marketplace)

	plugins, ok := mp["plugins"].([]any)
	require.True(t, ok, "%s must hold a plugins array", a.Marketplace)
	require.Len(t, plugins, 1, "%s: expected exactly one plugin entry", a.Marketplace)
	entry, ok = plugins[0].(map[string]any)
	require.True(t, ok, "%s: plugin entry must be an object", a.Marketplace)

	if !a.MarketplaceSourceObject {
		source, ok = entry["source"].(string)
		require.True(t, ok,
			"%s: source must be a relative path string", a.Marketplace)
		return mp, entry, source
	}

	obj, ok := entry["source"].(map[string]any)
	require.True(t, ok, "%s: source must be an object", a.Marketplace)
	// An external source is not indexed, so `plugin add` fails with a bare
	// "plugin not found" and no other signal.
	assert.Equal(t, "local", obj["source"],
		"%s: source.source must be \"local\"; an external source is not indexed", a.Marketplace)
	source, ok = obj["path"].(string)
	require.True(t, ok, "%s: source.path must be a string", a.Marketplace)
	return mp, entry, source
}

// TestMarketplaceResolvesThePlugin guards the two self-hosted marketplaces. An
// install resolves the entry's name and follows its source to a directory
// carrying a plugin manifest; a mismatched name or a dangling source surfaces
// only as an install failure at a user's machine.
func TestMarketplaceResolvesThePlugin(t *testing.T) {
	for _, a := range agentsWith(t, 2, func(a Agent) bool { return a.Marketplace != "" }) {
		t.Run(a.Label, func(t *testing.T) {
			_, entry, source := a.marketplaceEntry(t)

			assert.Equal(t, pluginName, entry["name"],
				"%s: plugin name is the install id and must match %s", a.Marketplace, a.Manifest)

			// Both CLIs resolve the source against the checkout root, not against
			// the directory holding the marketplace manifest.
			resolved := filepath.Clean(filepath.Join(pluginrepo.Root(t), source))
			assert.Equal(t, filepath.Clean(abs(t, a.PluginRoot)), resolved,
				"%s: source %q must resolve to this runtime's plugin root (%s)", a.Marketplace, source, a.PluginRoot)

			// The manifest has to sit where this runtime looks for it inside that
			// directory, which is its path relative to the plugin root.
			inPackage, err := filepath.Rel(abs(t, a.PluginRoot), abs(t, a.Manifest))
			require.NoError(t, err)
			assert.FileExists(t, filepath.Join(resolved, inPackage),
				"%s: source %q has no %s", a.Marketplace, source, inPackage)
		})
	}
}

// TestMarketplacePinnedVersions covers the marketplace that repeats the plugin
// version. It must be bumped with the manifest, and scripts/version.sh does
// that; a hand-edit that drifts serves a stale version to every installer.
func TestMarketplacePinnedVersions(t *testing.T) {
	for _, a := range agentsWith(t, 1, func(a Agent) bool { return a.MarketplacePinsVersion }) {
		t.Run(a.Label, func(t *testing.T) {
			mp, entry, _ := a.marketplaceEntry(t)
			version := a.manifestVersion(t)

			assert.Equal(t, version, entry["version"], "%s: plugin entry version", a.Marketplace)

			meta, ok := mp["metadata"].(map[string]any)
			require.True(t, ok, "%s must hold a metadata object", a.Marketplace)
			assert.Equal(t, version, meta["version"], "%s: metadata.version", a.Marketplace)
		})
	}
}
