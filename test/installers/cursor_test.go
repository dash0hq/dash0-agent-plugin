// SPDX-FileCopyrightText: Copyright 2026 Dash0 Inc.
// SPDX-License-Identifier: Apache-2.0

package installers

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/dash0hq/dash0-agent-plugin/test/helpers/pluginrepo"
)

// userHook is a foreign hook command seeded into ~/.cursor/hooks.json before the
// install. That file is user-owned and can already hold entries from other tools,
// so the merge and the strip must both leave it alone. The path need not exist.
const userHook = "/tmp/user-owned-hook.sh"

// cursorPluginRoot is where both installers put the plugin, under the home the
// test hands them.
func cursorPluginRoot(home string) string {
	return filepath.Join(home, ".cursor", "plugins", "local", "dash0-agent-plugin")
}

// dash0HookCommand is the command the installer must register for every event.
// The flavours spell it differently and both are right: the shell installer
// writes a LITERAL $HOME that Cursor expands when it fires the hook, while
// PowerShell has no deferred expansion and writes the resolved path inside a call
// operator.
func dash0HookCommand(home string) string {
	if runtime.GOOS == "windows" {
		return `& "` + filepath.Join(cursorPluginRoot(home), "cursor", "cursor-on-event.ps1") + `"`
	}
	return "$HOME/.cursor/plugins/local/dash0-agent-plugin/cursor/cursor-on-event.sh"
}

// cursorHooks is the shape of both the shipped cursor/hooks.json manifest and the
// user-scope ~/.cursor/hooks.json the installer merges into.
type cursorHooks struct {
	Version int `json:"version"`
	Hooks   map[string][]struct {
		Command string `json:"command"`
	} `json:"hooks"`
}

func readCursorHooks(t *testing.T, path string) cursorHooks {
	t.Helper()
	var h cursorHooks
	require.NoError(t, json.Unmarshal([]byte(readFile(t, path)), &h), "parsing %s", path)
	return h
}

// commandsFor returns every registered command for one event.
func (h cursorHooks) commandsFor(event string) []string {
	var out []string
	for _, e := range h.Hooks[event] {
		out = append(out, e.Command)
	}
	return out
}

// TestCursorInstallUninstall drives this platform's cursor installer and then its
// uninstaller over a ~/.cursor/hooks.json that already holds a foreign entry, and
// asserts the entry survives both.
//
// This is the only test of that merge and that strip. Neither has a Go
// counterpart: Cursor is the one runtime whose hook registration the installer
// edits rather than the binary emits.
//
// The events come from the shipped cursor/hooks.json, which is the installer's
// own input. A hard-coded list here would be a second copy to keep in sync, and a
// new event in the manifest would go unchecked.
func TestCursorInstallUninstall(t *testing.T) {
	if runtime.GOOS != "windows" {
		// Only the shell installer merges hooks.json with jq; the PowerShell one uses
		// ConvertFrom-Json, which ships with the platform.
		requireCommand(t, "jq", "brew install jq (macOS) or your distro's package manager")
	}

	pluginDir := pluginrepo.Root(t)
	// Read from the .sh even on Windows: both bootstraps pin the same release, and
	// the .sh is the one test/consistency keeps in step with plugin.json.
	version := pluginrepo.BootstrapVersion(t, pluginDir, "cursor/cursor-on-event.sh")

	// The events the plugin ships. Read from the manifest the installer installs,
	// so this cannot drift from what is actually registered.
	manifest := readCursorHooks(t, filepath.Join(pluginDir, "cursor", "hooks.json"))
	require.NotEmpty(t, manifest.Hooks, "cursor/hooks.json declares no events")

	home := t.TempDir()
	state := t.TempDir()
	stateBase := filepath.Join(state, "dash0-agent-plugin", "cursor")
	require.NoError(t, os.MkdirAll(filepath.Join(stateBase, "bin"), 0o755))
	require.NoError(t, os.MkdirAll(filepath.Join(home, ".cursor"), 0o755))

	// Pre-stage the binary at the version-pinned path so the installer skips the
	// download, and point DASH0_SOURCE_DIR at this checkout so the plugin files
	// come from the branch. The build happens here, with the real HOME, because a
	// Go toolchain resolved through $HOME (asdf, mise) cannot run once the
	// installer has a temp one.
	pluginrepo.CopyExecutable(t,
		pluginrepo.BuildBinary(t, pluginDir, "./cmd/cursor-on-event"),
		filepath.Join(stateBase, "bin", pluginrepo.CachedBinary(t, "cursor-on-event", version)))

	pluginRoot := cursorPluginRoot(home)
	bootstrapPath := filepath.Join(pluginRoot, "cursor", bootstrapName("cursor"))
	require.NoError(t, os.MkdirAll(filepath.Dir(bootstrapPath), 0o755))
	require.NoError(t, os.WriteFile(bootstrapPath, []byte(staleBootstrap()), 0o755))

	// Seed a foreign hook the installer must preserve.
	hooksPath := filepath.Join(home, ".cursor", "hooks.json")
	require.NoError(t, os.WriteFile(hooksPath, []byte(`{
  "version": 1,
  "hooks": {
    "beforeSubmitPrompt": [{"command": "`+userHook+`"}]
  }
}`), 0o644))

	env := installerEnv(home, state, version, "DASH0_SOURCE_DIR="+pluginDir)

	// --- install ---------------------------------------------------------------
	runInstall(t, pluginDir, "install-cursor", env)

	for _, rel := range []string{
		filepath.Join(".cursor-plugin", "plugin.json"),
		filepath.Join("cursor", "hooks.json"),
		filepath.Join("cursor", "skills", "dash0-configure", "SKILL.md"),
		filepath.Join("cursor", bootstrapName("cursor")),
	} {
		require.FileExists(t, filepath.Join(pluginRoot, rel), "installer did not create %s", rel)
	}
	requireConfigCarriesTheCredentials(t,
		filepath.Join(home, ".cursor", "dash0-agent-plugin.local.md"))

	if runtime.GOOS != "windows" {
		// The bootstrap is what Cursor execs, so a non-executable copy makes every
		// hook fire fail silently. Windows decides by extension and reports 0o666
		// for every file, so there is no bit to read.
		info, err := os.Stat(bootstrapPath)
		require.NoError(t, err)
		assert.NotZero(t, info.Mode().Perm()&0o111, "installed bootstrap is not executable")
	}

	// The bootstrap resolves the binary it execs from the version it declares.
	assert.Contains(t, readFile(t, bootstrapPath), versionLine(version),
		"the installed bootstrap must declare the version being installed")

	installed := readCursorHooks(t, hooksPath)
	for event := range manifest.Hooks {
		assert.Contains(t, installed.commandsFor(event), dash0HookCommand(home),
			"hooks.json has no Dash0 entry for %s", event)
	}
	assert.Contains(t, installed.commandsFor("beforeSubmitPrompt"), userHook,
		"installer removed the user-authored hook")

	// --- uninstall -------------------------------------------------------------
	runUninstall(t, pluginDir, "uninstall-cursor", env)

	requireNotExists(t, pluginRoot)
	requireNotExists(t, filepath.Join(home, ".cursor", "dash0-agent-plugin.local.md"))
	requireNotExists(t, stateBase)

	// hooks.json must survive, because a user-owned entry is still in it.
	require.FileExists(t, hooksPath,
		"uninstaller deleted ~/.cursor/hooks.json while a user-owned entry was present")

	remaining := readCursorHooks(t, hooksPath)
	for event, entries := range remaining.Hooks {
		for _, e := range entries {
			assert.NotContains(t, e.Command, bootstrapName("cursor"),
				"uninstaller left a Dash0 entry for %s", event)
		}
	}
	assert.Equal(t, []string{userHook}, remaining.commandsFor("beforeSubmitPrompt"),
		"the user's hook must be the only one left, exactly as it was")
}
