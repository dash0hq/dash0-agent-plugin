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
// fire), must include agentStop (the per-turn trigger), must NOT include the
// fail-closed preToolUse, and each command must pass its event name as an argv.
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
		"agentStop", "postToolUse", "postToolUseFailure",
		"sessionEnd", "sessionStart", "userPromptSubmitted",
	}, keys, "hooks.json must register exactly the camelCase set incl. agentStop, excl. preToolUse")

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
