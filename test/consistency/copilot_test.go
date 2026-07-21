// SPDX-FileCopyrightText: Copyright 2026 Dash0 Inc.
// SPDX-License-Identifier: Apache-2.0

// Package consistency holds deterministic, no-auth checks that keep the shipped
// Copilot plugin package internally consistent. They run in `go test ./...`.
package consistency

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func repoRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	require.True(t, ok)
	dir := filepath.Dir(file)
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		require.NotEqual(t, parent, dir)
		dir = parent
	}
}

func readJSON(t *testing.T, path string) map[string]any {
	t.Helper()
	raw, err := os.ReadFile(path)
	require.NoError(t, err, "reading %s", path)
	var m map[string]any
	require.NoError(t, json.Unmarshal(raw, &m), "parsing %s", path)
	return m
}

func TestCopilotVersionParity(t *testing.T) {
	root := repoRoot(t)
	manifest := readJSON(t, filepath.Join(root, "copilot", "plugin.json"))
	version, _ := manifest["version"].(string)
	require.NotEmpty(t, version)

	script, err := os.ReadFile(filepath.Join(root, "copilot", "copilot-on-event.sh"))
	require.NoError(t, err)
	m := regexp.MustCompile(`(?m)^VERSION="([^"]+)"`).FindSubmatch(script)
	require.NotNil(t, m, "VERSION= in copilot-on-event.sh")
	assert.Equal(t, version, string(m[1]), "plugin.json version must match bootstrap VERSION=")
}

// TestCopilotHooksAreCamelCaseWithAgentStop guards the load-bearing facts:
// registration must be camelCase (so agentStop/userPromptSubmitted actually
// fire), must include agentStop (the per-turn trigger), and must NOT include
// the fail-closed preToolUse nor postToolUse/postToolUseFailure (tool spans are
// sourced from the native-OTel file, not hooks); each command must pass its
// event name as an argv.
func TestCopilotHooksAreCamelCaseWithAgentStop(t *testing.T) {
	root := repoRoot(t)
	hooks := readJSON(t, filepath.Join(root, "copilot", "hooks.json"))
	hookMap, ok := hooks["hooks"].(map[string]any)
	require.True(t, ok)

	keys := make([]string, 0, len(hookMap))
	for k := range hookMap {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	assert.Equal(t, []string{
		"agentStop", "sessionEnd", "sessionStart", "userPromptSubmitted",
	}, keys, "hooks.json must register exactly the lifecycle camelCase set incl. agentStop, excl. preToolUse/postToolUse*")

	for name, v := range hookMap {
		entries, ok := v.([]any)
		require.True(t, ok)
		require.Len(t, entries, 1, "%s", name)
		entry, _ := entries[0].(map[string]any)
		cmd, _ := entry["bash"].(string)
		assert.Contains(t, cmd, "${PLUGIN_ROOT}/copilot-on-event.sh", "%s command path", name)
		assert.Contains(t, cmd, " "+name, "%s must pass its event name as an argv", name)
	}
}

func TestCopilotManifestDeclaresHooksAndSkills(t *testing.T) {
	root := repoRoot(t)
	m := readJSON(t, filepath.Join(root, "copilot", "plugin.json"))
	assert.Equal(t, "hooks.json", m["hooks"])
	assert.Equal(t, "skills/", m["skills"])
	// Copilot CLI's plugin.json has no `userConfig` field — that is Claude Code's
	// mechanism, not part of Copilot's plugin entry fields. Credentials instead
	// flow via ~/.copilot/dash0-agent-plugin.local.md + the bootstrap (see
	// TestE2ECopilotCredentialContracts). Guard against re-introducing it.
	_, hasUserConfig := m["userConfig"]
	assert.False(t, hasUserConfig, "userConfig is not a valid Copilot plugin.json field")
}

// TestCopilotMarketplaceConsistency guards the self-hosted Copilot marketplace
// (.github/plugin/marketplace.json) against silent breakage. Copilot installs
// `dash0-agent-plugin@dash0` by resolving the plugin entry's `source` path to a
// directory carrying plugin.json; a mismatched name, a dangling source, or a
// version drift surfaces only as an install failure. The real-CLI install is
// covered by TestE2ECopilotMarketplaceInstall — this is the no-CLI guard that
// runs everywhere in `go test ./...`.
func TestCopilotMarketplaceConsistency(t *testing.T) {
	root := repoRoot(t)
	mp := readJSON(t, filepath.Join(root, ".github", "plugin", "marketplace.json"))

	// The marketplace name is the `@dash0` install suffix.
	assert.Equal(t, "dash0", mp["name"], "marketplace name is the `@<name>` install suffix")

	plugins, ok := mp["plugins"].([]any)
	require.True(t, ok, "marketplace.json must have a plugins array")
	require.Len(t, plugins, 1, "expected exactly one plugin entry")
	entry, ok := plugins[0].(map[string]any)
	require.True(t, ok)

	manifest := readJSON(t, filepath.Join(root, "copilot", "plugin.json"))
	manifestName, _ := manifest["name"].(string)
	manifestVersion, _ := manifest["version"].(string)

	assert.Equal(t, manifestName, entry["name"],
		"marketplace plugin name must match copilot/plugin.json name (the install id)")
	assert.Equal(t, manifestVersion, entry["version"],
		"marketplace plugin version must match copilot/plugin.json (release.sh keeps these in sync)")

	meta, _ := mp["metadata"].(map[string]any)
	assert.Equal(t, manifestVersion, meta["version"],
		"marketplace metadata.version must match the released plugin version")

	// source must be a relative path string resolving to the plugin manifest.
	src, ok := entry["source"].(string)
	require.True(t, ok, "source must be a relative path string (marketplace is co-located with the plugin)")
	_, statErr := os.Stat(filepath.Join(root, src, "plugin.json"))
	require.NoError(t, statErr, "source %q has no plugin.json", src)
}

// TestCopilotShippedPackageExcludesDevOnlyDirs locks in that the dev-only capture
// harness lives OUTSIDE the shipped copilot/ package. A marketplace/subpath install
// copies the whole copilot/ tree, so anything left here ships to every user.
func TestCopilotShippedPackageExcludesDevOnlyDirs(t *testing.T) {
	root := repoRoot(t)
	for _, dir := range []string{"capture", "captured"} {
		_, err := os.Stat(filepath.Join(root, "copilot", dir))
		assert.True(t, os.IsNotExist(err),
			"copilot/%s must not exist — dev-only assets live under test/capture/copilot/, not the shipped package", dir)
	}
}

func TestCopilotBootstrapValidAndForwardsArgs(t *testing.T) {
	root := repoRoot(t)
	script := filepath.Join(root, "copilot", "copilot-on-event.sh")
	if _, err := exec.LookPath("bash"); err == nil {
		out, err := exec.Command("bash", "-n", script).CombinedOutput()
		assert.NoError(t, err, "bash -n: %s", out)
	}
	body, err := os.ReadFile(script)
	require.NoError(t, err)
	// Must forward the event-name argv (and stdin) to the binary.
	assert.Contains(t, string(body), `exec "$BINARY" "$@"`, "bootstrap must forward args to the binary")
	info, err := os.Stat(script)
	require.NoError(t, err)
	assert.NotZero(t, info.Mode()&0o111, "bootstrap must be executable")
}
