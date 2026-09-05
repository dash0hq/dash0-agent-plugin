// SPDX-FileCopyrightText: Copyright 2026 Dash0 Inc.
// SPDX-License-Identifier: Apache-2.0

//go:build marketplace

package marketplaces

import (
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/dash0hq/dash0-agent-plugin/test/helpers/pluginrepo"
)

// TestCopilotMarketplaceInstall drives the self-hosted Copilot marketplace:
// `copilot plugin marketplace add <repo>` plus `copilot plugin install
// dash0-agent-plugin@dash0` must index and install the plugin declared in
// .github/plugin/marketplace.json. test/consistency proves the JSON is well
// formed, but only the real copilot CLI proves it resolves the `source` path.
//
// The two verbs only fetch and copy, so this needs no COPILOT_GITHUB_TOKEN.
func TestCopilotMarketplaceInstall(t *testing.T) {
	run, copilotHome := agentCLI(t, "copilot", "COPILOT_HOME", "npm install -g @github/copilot")
	repoRoot := pluginrepo.Root(t) // holds .github/plugin/marketplace.json

	// 1. Register the repo as a marketplace (its .github/plugin/marketplace.json
	//    lists dash0-agent-plugin with source "./copilot").
	out, err := run("plugin", "marketplace", "add", repoRoot)
	require.NoError(t, err, "marketplace add failed:\n%s", out)

	// 2. Install must succeed via <plugin>@<marketplace>.
	out, err = run("plugin", "install", "dash0-agent-plugin@dash0")
	require.NoError(t, err, "plugin install failed:\n%s", out)

	// 3. The installed plugin must carry the manifest, camelCase hooks, and
	// bootstrap, wherever the CLI serves it from. It either copies the package
	// into <COPILOT_HOME>/installed-plugins, or (since 1.0.81, for a marketplace
	// whose source is a local directory) loads it live and copies nothing. A
	// fixed layout here turned that CLI change into a red canary.
	root := installedPluginPath(t, copilotHome, "dash0", "dash0-agent-plugin")
	for _, f := range []string{"plugin.json", "hooks.json", "copilot-on-event.sh"} {
		_, statErr := os.Stat(filepath.Join(root, f))
		require.NoError(t, statErr, "installed plugin missing %s", f)
	}
}

// installedPluginPath returns the directory the copilot CLI serves the installed
// plugin from. A copied install records a `cache_path` in
// <copilotHome>/config.json. A live install (local-directory marketplace, CLI
// 1.0.81 and later) records only the marketplace path and the enablement in
// settings.json, so the path is the plugin's declared `source` inside that
// marketplace. When neither record exists it dumps both candidate homes: an
// install that exits 0 and writes nothing is otherwise silent.
func installedPluginPath(t *testing.T, copilotHome, marketplace, name string) string {
	t.Helper()

	if raw, err := os.ReadFile(filepath.Join(copilotHome, "config.json")); err == nil {
		var config struct {
			InstalledPlugins []struct {
				Name      string `json:"name"`
				CachePath string `json:"cache_path"`
			} `json:"installedPlugins"`
		}
		decodeCopilotJSON(t, raw, &config)
		for _, p := range config.InstalledPlugins {
			if p.Name == name && p.CachePath != "" {
				return p.CachePath
			}
		}
	}

	settingsPath := filepath.Join(copilotHome, "settings.json")
	raw, err := os.ReadFile(settingsPath)
	if err != nil {
		dumpCopilotHomes(t, copilotHome)
		t.Fatalf("copilot recorded no install of %s: %v", name, err)
	}
	var settings struct {
		ExtraKnownMarketplaces map[string]struct {
			Source struct {
				Path string `json:"path"`
			} `json:"source"`
		} `json:"extraKnownMarketplaces"`
		EnabledPlugins map[string]bool `json:"enabledPlugins"`
	}
	decodeCopilotJSON(t, raw, &settings)

	ref := name + "@" + marketplace
	if !settings.EnabledPlugins[ref] || settings.ExtraKnownMarketplaces[marketplace].Source.Path == "" {
		dumpCopilotHomes(t, copilotHome)
		t.Fatalf("%s records no live install of %s:\n%s", settingsPath, ref, raw)
	}

	// A live install serves the plugin out of the marketplace, so the path is the
	// `source` it declares. Resolving it here proves that source is real.
	marketplaceRoot := settings.ExtraKnownMarketplaces[marketplace].Source.Path
	manifest, err := os.ReadFile(filepath.Join(marketplaceRoot, ".github", "plugin", "marketplace.json"))
	require.NoError(t, err, "live install points at %s, which holds no marketplace.json", marketplaceRoot)
	var index struct {
		Plugins []struct {
			Name   string `json:"name"`
			Source string `json:"source"`
		} `json:"plugins"`
	}
	decodeCopilotJSON(t, manifest, &index)
	for _, p := range index.Plugins {
		if p.Name == name {
			require.NotEmpty(t, p.Source, "marketplace.json declares no source for %s", name)
			return filepath.Join(marketplaceRoot, p.Source)
		}
	}
	t.Fatalf("marketplace.json at %s lists no plugin %s", marketplaceRoot, name)
	return ""
}

// decodeCopilotJSON unmarshals a file the copilot CLI wrote. config.json carries a
// leading `//` banner, which is JSONC and not JSON, so comment lines are dropped.
func decodeCopilotJSON(t *testing.T, raw []byte, into any) {
	t.Helper()
	var lines []string
	for _, line := range strings.Split(string(raw), "\n") {
		if !strings.HasPrefix(strings.TrimSpace(line), "//") {
			lines = append(lines, line)
		}
	}
	require.NoError(t, json.Unmarshal([]byte(strings.Join(lines, "\n")), into),
		"could not parse copilot JSON:\n%s", raw)
}

// dumpCopilotHomes lists the file names under the hermetic COPILOT_HOME and the
// real ~/.copilot, which separates "the CLI wrote nothing" from "the CLI wrote
// somewhere else". It prints contents only for the two install records, and only
// from the hermetic home: the real one holds auth state and MCP credentials,
// which must never reach a CI log.
func dumpCopilotHomes(t *testing.T, copilotHome string) {
	t.Helper()
	homes := []string{copilotHome}
	if home, err := os.UserHomeDir(); err == nil {
		homes = append(homes, filepath.Join(home, ".copilot"))
	}
	for _, dir := range homes {
		var paths []string
		_ = filepath.WalkDir(dir, func(path string, _ fs.DirEntry, err error) error {
			if err != nil {
				return nil
			}
			if len(paths) >= 200 {
				return fs.SkipAll
			}
			paths = append(paths, path)
			return nil
		})
		t.Logf("contents of %s (max 200 entries):\n%s", dir, strings.Join(paths, "\n"))
	}

	for _, name := range []string{"config.json", "settings.json"} {
		p := filepath.Join(copilotHome, name)
		if body, err := os.ReadFile(p); err == nil {
			t.Logf("%s:\n%s", p, body)
		}
	}
}
