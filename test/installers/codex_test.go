// SPDX-FileCopyrightText: Copyright 2026 Dash0 Inc.
// SPDX-License-Identifier: Apache-2.0

package installers

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/BurntSushi/toml"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/dash0hq/dash0-agent-plugin/internal/source/codex"
	"github.com/dash0hq/dash0-agent-plugin/test/helpers/pluginrepo"
)

// codexHookEvents is how many hook events the plugin registers, and so how many
// pre-trust entries the managed block must carry. Codex refuses to run a hook
// without a persisted trusted_hash, so a missing entry means a /hooks prompt on
// the user's first session. internal/source/codex.HookEvents is the source of
// truth; TestHookEventsCoversTen pins the count there.
const codexHookEvents = 10

// codexConfig is the subset of ~/.codex/config.toml these contracts assert on.
// Unmarshalling rather than grepping proves the file still PARSES after the
// installer's strip and append. Broken TOML satisfies every substring check and
// still leaves Codex unable to read its own config.
type codexConfig struct {
	Model string `toml:"model"`
	Hooks struct {
		PreToolUse []struct {
			Matcher string `toml:"matcher"`
			Hooks   []struct {
				Type    string `toml:"type"`
				Command string `toml:"command"`
			} `toml:"hooks"`
		} `toml:"PreToolUse"`
		State map[string]struct {
			TrustedHash string `toml:"trusted_hash"`
		} `toml:"state"`
	} `toml:"hooks"`
}

// preToolUseCommands flattens every PreToolUse hook command in the parsed config.
func (c codexConfig) preToolUseCommands() []string {
	var out []string
	for _, group := range c.Hooks.PreToolUse {
		for _, h := range group.Hooks {
			out = append(out, h.Command)
		}
	}
	return out
}

// TestCodexInstallUninstall drives this platform's codex installer and then its
// uninstaller over a config.toml that already holds a user setting and a
// user-authored hook, and asserts both survive the round trip.
//
// This is the only test of the installers' managed-block handling. The block's
// CONTENT comes from the binary (`emit-codex-hooks`, covered by
// internal/source/codex), but the strip that keeps a re-install from stacking
// blocks is awk in install-codex.sh and a line filter in install-codex.ps1, with
// a second copy in each uninstaller. Four pieces of script, no Go caller.
//
// Install and uninstall share one test because the uninstall contract is "put the
// file back the way it was", which needs the installed state as its input.
func TestCodexInstallUninstall(t *testing.T) {
	pluginDir := pluginrepo.Root(t)
	// Read from the .sh even on Windows: both bootstraps pin the same release, and
	// the .sh is the one test/consistency keeps in step with plugin.json.
	version := pluginrepo.BootstrapVersion(t, pluginDir, "codex/codex-on-event.sh")

	home := t.TempDir()
	state := t.TempDir()
	stateBase := filepath.Join(state, "dash0-agent-plugin", "codex")
	require.NoError(t, os.MkdirAll(filepath.Join(stateBase, "bin"), 0o755))
	require.NoError(t, os.MkdirAll(filepath.Join(home, ".codex"), 0o755))

	// Pre-stage the binary at the version-pinned path the installer resolves so it
	// skips the download, and point DASH0_SOURCE_DIR at this checkout so the
	// bootstrap comes from the branch. The build happens here, with the real HOME,
	// because a Go toolchain resolved through $HOME (asdf, mise) cannot run once
	// the installer has a temp one.
	pluginrepo.CopyExecutable(t,
		pluginrepo.BuildBinary(t, pluginDir, "./cmd/codex-on-event"),
		filepath.Join(stateBase, "bin", pluginrepo.CachedBinary(t, "codex-on-event", version)))

	bootstrapPath := filepath.Join(stateBase, bootstrapName("codex"))
	require.NoError(t, os.WriteFile(bootstrapPath, []byte(staleBootstrap()), 0o755))

	// Seed config.toml with an unrelated setting AND a user-authored hook. Both
	// must survive the round trip: the managed block is appended after the user's
	// content, and the installer counts existing hook groups to index its own.
	configPath := filepath.Join(home, ".codex", "config.toml")
	require.NoError(t, os.WriteFile(configPath, []byte(`model = "gpt-5.5"

[[hooks.PreToolUse]]
matcher = "*"
[[hooks.PreToolUse.hooks]]
type = "command"
command = 'echo user-hook'
`), 0o644))

	env := installerEnv(home, state, version, "DASH0_SOURCE_DIR="+pluginDir)

	// --- install ---------------------------------------------------------------
	runInstall(t, pluginDir, "install-codex", env)

	requireConfigCarriesTheCredentials(t,
		filepath.Join(home, ".codex", "dash0-agent-plugin.local.md"))

	// The bootstrap resolves the binary it execs from the VERSION it declares.
	assert.Contains(t, readFile(t, bootstrapPath), versionLine(version),
		"the installer reused the stale bootstrap, so an upgrade would keep running the old binary")

	installed := readFile(t, configPath)
	assert.Contains(t, installed, codex.ManagedBlockBegin, "managed block not appended to config.toml")
	assert.Contains(t, installed, codex.ManagedBlockEnd, "managed block has no end marker")

	var cfg codexConfig
	_, err := toml.Decode(installed, &cfg)
	require.NoError(t, err, "config.toml is not valid TOML after install:\n%s", installed)

	assert.Equal(t, "gpt-5.5", cfg.Model, "installer lost the user's own setting")
	cmds := cfg.preToolUseCommands()
	assert.Contains(t, cmds, "echo user-hook", "installer lost the user-authored PreToolUse hook")
	assert.True(t, anyContains(cmds, bootstrapName("codex")),
		"no Dash0 PreToolUse hook registered, got %v", cmds)
	assert.Len(t, cfg.Hooks.State, codexHookEvents,
		"every registered hook needs a pre-trust entry or Codex prompts via /hooks")

	// --- uninstall -------------------------------------------------------------
	runUninstall(t, pluginDir, "uninstall-codex", env)

	requireNotExists(t, filepath.Join(home, ".codex", "dash0-agent-plugin.local.md"))
	requireNotExists(t, stateBase)

	stripped := readFile(t, configPath)
	assert.NotContains(t, stripped, codex.ManagedBlockBegin, "managed block survived uninstall")
	assert.NotContains(t, stripped, bootstrapName("codex"), "Dash0 hook command survived uninstall")

	var after codexConfig
	_, err = toml.Decode(stripped, &after)
	require.NoError(t, err, "config.toml is not valid TOML after uninstall:\n%s", stripped)

	assert.Equal(t, "gpt-5.5", after.Model, "uninstaller lost the user's own setting")
	assert.Equal(t, []string{"echo user-hook"}, after.preToolUseCommands(),
		"the user's hook must be the only one left, exactly as it was")
	assert.Empty(t, after.Hooks.State, "trust state survived uninstall")
}
