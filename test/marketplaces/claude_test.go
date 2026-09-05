// SPDX-FileCopyrightText: Copyright 2026 Dash0 Inc.
// SPDX-License-Identifier: Apache-2.0

//go:build marketplace

package marketplaces

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/dash0hq/dash0-agent-plugin/test/helpers/pluginrepo"
)

// TestClaudeMarketplaceInstall drives the marketplace install path against THIS
// checkout. Claude Code ships through dash0hq/claude-marketplace, so the repo
// holds no marketplace manifest to point at. The test stages a throwaway one
// listing the checkout as a local source, so the install covers this branch's
// plugin.json, metadata and layout rather than what is published.
//
// A local source means no clone and no auth, so this runs offline.
func TestClaudeMarketplaceInstall(t *testing.T) {
	run, home := agentCLI(t, "claude", "HOME", "npm install -g @anthropic-ai/claude-code")
	pluginDir := pluginrepo.Root(t)

	// A marketplace is a directory holding .claude-plugin/marketplace.json whose
	// plugin sources resolve relative to it, so the checkout is symlinked in
	// rather than copied.
	marketplace := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(marketplace, ".claude-plugin"), 0o755))
	require.NoError(t, os.Symlink(pluginDir, filepath.Join(marketplace, "dash0-agent-plugin")))
	require.NoError(t, os.WriteFile(
		filepath.Join(marketplace, ".claude-plugin", "marketplace.json"),
		[]byte(`{
  "name": "local",
  "owner": { "name": "marketplace-test" },
  "plugins": [ { "name": "dash0-agent-plugin", "source": "./dash0-agent-plugin" } ]
}`), 0o644))

	// 1. Register the staged marketplace. --scope user writes to the throwaway
	//    HOME rather than a project directory.
	out, err := run("plugin", "marketplace", "add", marketplace, "--scope", "user")
	require.NoError(t, err, "marketplace add failed:\n%s", out)

	// 2. Install must succeed via <plugin>@<marketplace>.
	out, err = run("plugin", "install", "dash0-agent-plugin@local", "--scope", "user")
	require.NoError(t, err, "plugin install failed:\n%s", out)

	// 3. The installed plugin must carry the manifest, hook registration and
	//    bootstrap. Claude nests the install under a version directory, so the
	//    version is globbed rather than spelled out.
	cache := filepath.Join(home, ".claude", "plugins", "cache", "local", "dash0-agent-plugin")
	matches, _ := filepath.Glob(filepath.Join(cache, "*"))
	require.NotEmpty(t, matches, "plugin cache dir not created under %s", cache)
	root := matches[0]
	for _, f := range []string{
		filepath.Join(".claude-plugin", "plugin.json"),
		filepath.Join("claude", "hooks.json"),
		filepath.Join("claude", "claude-on-event.sh"),
	} {
		_, statErr := os.Stat(filepath.Join(root, f))
		require.NoError(t, statErr, "installed plugin missing %s", f)
	}

	// The directory Claude nests under is the manifest version, which is how an
	// update is resolved. A mismatch means an install that reports the wrong
	// version to the user.
	version := pluginrepo.BootstrapVersion(t, pluginDir, "claude/claude-on-event.sh")
	require.Equal(t, version, filepath.Base(root),
		"install directory must be the plugin version (cache: %s)", cache)
}

// TestClaudeSettingsAloneDoesNotInstall pins the behaviour the fleet rollout docs
// depend on: extraKnownMarketplaces plus enabledPlugins in settings.json does NOT
// install the plugin. A managed settings push alone leaves every machine without
// it. If this ever starts installing, the guidance in .claude-plugin/README.md is
// wrong.
//
// This one needs network. It names the published dash0hq/claude-marketplace, and
// the assertion only means something if that marketplace resolves: a failed clone
// would leave the cache empty and pass for the wrong reason.
func TestClaudeSettingsAloneDoesNotInstall(t *testing.T) {
	run, home := agentCLI(t, "claude", "HOME", "npm install -g @anthropic-ai/claude-code")

	// Rewrite git@github.com to HTTPS so the marketplace can be cloned without an
	// SSH key, which CI runners do not have. This writes into the throwaway HOME,
	// never the developer's real ~/.gitconfig.
	require.NoError(t, os.WriteFile(filepath.Join(home, ".gitconfig"),
		[]byte("[url \"https://github.com/\"]\n\tinsteadOf = git@github.com:\n"), 0o644))

	require.NoError(t, os.MkdirAll(filepath.Join(home, ".claude"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(home, ".claude", "settings.json"), []byte(`{
  "extraKnownMarketplaces": {
    "dash0": { "source": { "source": "github", "repo": "dash0hq/claude-marketplace" } }
  },
  "enabledPlugins": { "dash0-agent-plugin@dash0": true }
}`), 0o644))

	// The exit status is not the contract; the cache is. `plugin list` is here to
	// give the CLI the chance to act on the settings it just read.
	out, err := run("plugin", "list")
	t.Logf("claude plugin list (err=%v):\n%s", err, out)

	// The repository has to be reachable, or the assertion below is satisfied by
	// the clone never happening: no network, no git, the insteadOf rewrite not
	// taking, or the repository renamed.
	//
	// git ls-remote rather than `plugin marketplace list`. The listing reads
	// known_marketplaces.json, which only `plugin marketplace add` writes, so an
	// extraKnownMarketplaces entry never appears in it. ls-remote goes through the
	// same throwaway HOME and the same .gitconfig the clone would.
	ls := exec.Command("git", "ls-remote", "--exit-code", "git@github.com:dash0hq/claude-marketplace.git", "HEAD")
	ls.Env = append(os.Environ(), "HOME="+home)
	lsOut, lsErr := ls.CombinedOutput()
	require.NoError(t, lsErr,
		"dash0hq/claude-marketplace is unreachable from this HOME, so an empty plugin "+
			"cache proves nothing about what settings.json alone does:\n%s", lsOut)

	matches, globErr := filepath.Glob(filepath.Join(home, ".claude", "plugins", "cache", "*dash0*"))
	require.NoError(t, globErr)
	require.Empty(t, matches,
		"settings.json alone installed the plugin; the fleet rollout docs assume an explicit `plugin install` is required")
}

// TestClaudePublishedMarketplacesResolve installs from the two marketplaces the
// README actually tells users to add.
//
// TestClaudeMarketplaceInstall covers this branch's plugin files through a staged
// local marketplace, which is the route that catches a bad plugin.json before it
// ships. It cannot catch the other half: whether the PUBLISHED entries still
// resolve. Those live in two repositories this one does not control,
// dash0hq/claude-marketplace and anthropics/claude-plugins-official. A rename, a
// bad source, or a dropped entry there breaks the exact commands at
// .claude-plugin/README.md with a bare "plugin not found".
//
// Network and a clone, and no credentials, since both marketplaces are public.
// TestClaudeSettingsAloneDoesNotInstall above needs the network only to prove the
// repository is reachable, and the local-marketplace test at the top of this file
// needs neither.
func TestClaudePublishedMarketplacesResolve(t *testing.T) {
	for _, c := range []struct {
		name, repo, install string
	}{
		// The Dash0-owned marketplace. `plugin install` names the plugin as
		// <plugin>@<marketplace>, and the marketplace name comes from its own
		// manifest rather than from the repository.
		{
			name:    "dash0 marketplace",
			repo:    "dash0hq/claude-marketplace",
			install: "dash0-agent-plugin@dash0",
		},
		// Anthropic's official list, where this plugin is registered under a
		// different name. Both halves are worth pinning: the entry could be
		// removed, or renamed.
		{
			name:    "official marketplace",
			repo:    "anthropics/claude-plugins-official",
			install: "dash0@claude-plugins-official",
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			run, home := agentCLI(t, "claude", "HOME", "npm install -g @anthropic-ai/claude-code")

			// HTTPS instead of SSH, because CI runners carry no key. Written into the
			// throwaway HOME, so the developer's real ~/.gitconfig is untouched.
			require.NoError(t, os.WriteFile(filepath.Join(home, ".gitconfig"),
				[]byte("[url \"https://github.com/\"]\n\tinsteadOf = git@github.com:\n"), 0o644))

			out, err := run("plugin", "marketplace", "add", c.repo, "--scope", "user")
			require.NoError(t, err,
				"could not add %s; the marketplace the README tells users to add does "+
					"not resolve:\n%s", c.repo, out)

			out, err = run("plugin", "install", c.install, "--scope", "user")
			require.NoError(t, err,
				"`claude plugin install %s` failed. That is the command in "+
					".claude-plugin/README.md, so this is what a new user hits:\n%s",
				c.install, out)

			// Installed, not merely accepted: `plugin install` can report success
			// having resolved nothing, which is the failure mode that reached users.
			matches, globErr := filepath.Glob(
				filepath.Join(home, ".claude", "plugins", "cache", "*", "*"))
			require.NoError(t, globErr)
			require.NotEmpty(t, matches,
				"install reported success but left nothing in the plugin cache:\n%s", out)
			t.Logf("installed: %v", matches)
		})
	}
}

// TestClaudeValidatesThePlugin runs the CLI's own manifest validator over this
// checkout.
//
// test/consistency checks the fields this repo decided to care about, which by
// construction cannot catch a field the CLI rejects, a type it refuses, or a
// schema Anthropic changed under us. The validator is the only check that belongs
// to Claude Code rather than to us.
//
// Here rather than in test/consistency because it needs the claude CLI, which is
// what the marketplace build tag gates. Offline: it reads the checkout.
func TestClaudeValidatesThePlugin(t *testing.T) {
	run, _ := agentCLI(t, "claude", "HOME", "npm install -g @anthropic-ai/claude-code")

	out, err := run("plugin", "validate", pluginrepo.Root(t))
	require.NoError(t, err,
		"`claude plugin validate` rejected this checkout. This is Claude Code's own "+
			"schema, so test/consistency cannot see the problem:\n%s", out)
	t.Logf("claude plugin validate:\n%s", out)
}
