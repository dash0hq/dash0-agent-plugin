// SPDX-FileCopyrightText: Copyright 2026 Dash0 Inc.
// SPDX-License-Identifier: Apache-2.0

//go:build marketplace

package marketplaces

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/dash0hq/dash0-agent-plugin/test/helpers/pluginrepo"
)

// TestCodexMarketplaceInstall drives the self-hosted plugin marketplace: `codex
// plugin marketplace add <repo>` plus `codex plugin add` must INDEX and install
// the plugin declared in .agents/plugins/marketplace.json. test/consistency
// proves the JSON is well formed, but only the real codex CLI proves Codex
// indexes it. An external `github` plugin source once looked valid, was never
// indexed, and surfaced as a bare "plugin not found".
//
// `plugin add` only fetches and copies, so this runs without OPENAI_API_KEY.
func TestCodexMarketplaceInstall(t *testing.T) {
	run, codexHome := agentCLI(t, "codex", "CODEX_HOME", "npm install -g @openai/codex")
	pluginDir := pluginrepo.Root(t)

	// 1. Add the repo as a local marketplace (its .agents/plugins/marketplace.json
	//    lists the plugin at path ".").
	out, err := run("plugin", "marketplace", "add", pluginDir)
	require.NoError(t, err, "marketplace add failed:\n%s", out)

	// 2. The plugin must be INDEXED, which is what a github source failed.
	out, err = run("plugin", "list")
	require.NoError(t, err, "plugin list failed:\n%s", out)
	require.Contains(t, out, "dash0-agent-plugin@dash0",
		"plugin not indexed from marketplace.json (check source type/path):\n%s", out)

	// 3. Install must succeed.
	out, err = run("plugin", "add", "dash0-agent-plugin@dash0")
	require.NoError(t, err, "plugin add failed:\n%s", out)

	// 4. The installed plugin must carry the manifest, hook registration, and bootstrap.
	matches, _ := filepath.Glob(filepath.Join(codexHome, "plugins", "cache", "dash0", "dash0-agent-plugin", "*"))
	require.NotEmpty(t, matches, "plugin cache dir not created")
	root := matches[0]
	for _, f := range []string{
		filepath.Join(".codex-plugin", "plugin.json"),
		filepath.Join("codex", "hooks.json"),
		filepath.Join("codex", "codex-on-event.sh"),
	} {
		_, statErr := os.Stat(filepath.Join(root, f))
		require.NoError(t, statErr, "installed plugin missing %s", f)
	}
}
