// SPDX-FileCopyrightText: Copyright 2026 Dash0 Inc.
// SPDX-License-Identifier: Apache-2.0

package harness

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/dash0hq/dash0-agent-plugin/internal/config"
)

// TestMain points the home directory and the working directory at an empty temp
// directory for every test in this package.
//
// Config lookups now fall back to <ConfigDir>/dash0-agent-plugin.local.md, in
// the project and in the user's home. Without this isolation a developer who has
// the plugin configured runs these tests against their own OTLP endpoint and
// token: assertions about an unset option read whatever their real file says,
// and an exporter test could ship to their live dataset.
func TestMain(m *testing.M) {
	home, err := os.MkdirTemp("", "harness-test-home")
	if err != nil {
		panic(err)
	}
	// os.UserHomeDir reads HOME on Unix and USERPROFILE on Windows.
	os.Setenv("HOME", home)
	os.Setenv("USERPROFILE", home)
	if err := os.Chdir(home); err != nil {
		panic(err)
	}

	code := m.Run()
	_ = os.RemoveAll(home)
	os.Exit(code)
}

func TestDataDirPrecedence(t *testing.T) {
	t.Run("agent prefix wins over everything", func(t *testing.T) {
		t.Setenv("CODEX_PLUGIN_DATA", "/from/agent")
		t.Setenv("DASH0_PLUGIN_DATA", "/from/dash0")
		t.Setenv("XDG_STATE_HOME", "/from/xdg")

		got, err := Codex.DataDir()
		require.NoError(t, err)
		assert.Equal(t, "/from/agent", got)
	})

	t.Run("prefix is per agent, so another agent's var is ignored", func(t *testing.T) {
		t.Setenv("CODEX_PLUGIN_DATA", "/from/codex")
		t.Setenv("XDG_STATE_HOME", "/from/xdg")

		got, err := Copilot.DataDir()
		require.NoError(t, err)
		assert.Equal(t, filepath.Join("/from/xdg", "dash0-agent-plugin", "copilot"), got)
	})

	t.Run("DASH0_PLUGIN_DATA wins over XDG", func(t *testing.T) {
		t.Setenv("DASH0_PLUGIN_DATA", "/from/dash0")
		t.Setenv("XDG_STATE_HOME", "/from/xdg")

		got, err := Codex.DataDir()
		require.NoError(t, err)
		assert.Equal(t, "/from/dash0", got)
	})

	t.Run("XDG root gets the agent subdirectory", func(t *testing.T) {
		t.Setenv("CODEX_PLUGIN_DATA", "")
		t.Setenv("DASH0_PLUGIN_DATA", "")
		t.Setenv("XDG_STATE_HOME", "/from/xdg")

		got, err := Codex.DataDir()
		require.NoError(t, err)
		assert.Equal(t, filepath.Join("/from/xdg", "dash0-agent-plugin", "codex"), got)
	})

	t.Run("falls back to ~/.local/state", func(t *testing.T) {
		t.Setenv("CODEX_PLUGIN_DATA", "")
		t.Setenv("DASH0_PLUGIN_DATA", "")
		t.Setenv("XDG_STATE_HOME", "")
		// os.UserHomeDir reads HOME on Unix and USERPROFILE on Windows, so both
		// have to move or this asserts against the real home on one platform.
		t.Setenv("HOME", "/home/somebody")
		t.Setenv("USERPROFILE", "/home/somebody")

		got, err := Codex.DataDir()
		require.NoError(t, err)
		assert.Equal(t, filepath.Join("/home/somebody", ".local", "state", "dash0-agent-plugin", "codex"), got)
	})

	t.Run("agents keep separate subdirectories", func(t *testing.T) {
		t.Setenv("CODEX_PLUGIN_DATA", "")
		t.Setenv("COPILOT_PLUGIN_DATA", "")
		t.Setenv("DASH0_PLUGIN_DATA", "")
		t.Setenv("XDG_STATE_HOME", "/state")

		codexDir, err := Codex.DataDir()
		require.NoError(t, err)
		copilotDir, err := Copilot.DataDir()
		require.NoError(t, err)
		assert.NotEqual(t, codexDir, copilotDir)
	})
}

func TestAgentName(t *testing.T) {
	t.Run("defaults to the harness name", func(t *testing.T) {
		t.Setenv("DASH0_AGENT_NAME", "")
		assert.Equal(t, "codex", Codex.AgentName())
		// Copilot's reported name is not its data subdirectory.
		assert.Equal(t, "github-copilot-cli", Copilot.AgentName())
	})

	t.Run("DASH0_AGENT_NAME overrides", func(t *testing.T) {
		t.Setenv("DASH0_AGENT_NAME", "custom")
		assert.Equal(t, "custom", Codex.AgentName())
	})

	// AGENT_NAME is a declared plugin option for Claude, so the prefixed form
	// must win — that is the /plugin → Configure value.
	t.Run("the prefixed plugin option wins", func(t *testing.T) {
		t.Setenv("CLAUDE_PLUGIN_OPTION_AGENT_NAME", "from-plugin-ui")
		t.Setenv("DASH0_AGENT_NAME", "from-env")
		assert.Equal(t, "from-plugin-ui", Claude.AgentName())
	})

	t.Run("claude falls back to claude-code", func(t *testing.T) {
		t.Setenv("CLAUDE_PLUGIN_OPTION_AGENT_NAME", "")
		t.Setenv("DASH0_AGENT_NAME", "")
		assert.Equal(t, "claude-code", Claude.AgentName())
	})
}

func TestPluginOptionSecure(t *testing.T) {
	t.Setenv("CODEX_PLUGIN_OPTION_AUTH_TOKEN", "codex-token")
	t.Setenv("COPILOT_PLUGIN_OPTION_AUTH_TOKEN", "copilot-token")

	assert.Equal(t, "codex-token", Codex.PluginOptionSecure("AUTH_TOKEN"))
	assert.Equal(t, "copilot-token", Copilot.PluginOptionSecure("AUTH_TOKEN"))
	assert.Empty(t, Codex.PluginOptionSecure("MISSING"))
}

// The secure lookup must never read a DASH0_* variable: those are visible to
// tool subprocesses the agent spawns.
func TestPluginOptionSecureIgnoresDash0Env(t *testing.T) {
	t.Setenv("CODEX_PLUGIN_OPTION_AUTH_TOKEN", "")
	t.Setenv("DASH0_AUTH_TOKEN", "leaked")

	assert.Empty(t, Codex.PluginOptionSecure("AUTH_TOKEN"))
}

// HarnessName is the platform constant. AgentName may be overridden; this must
// not be, or a renamed service could disguise which tool produced the spans.
func TestConfig(t *testing.T) {
	t.Run("reads every field from the environment", func(t *testing.T) {
		t.Setenv("DASH0_OTLP_URL", "https://ingress.example.com")
		t.Setenv("CODEX_PLUGIN_OPTION_AUTH_TOKEN", "secret")
		t.Setenv("DASH0_DATASET", "ds")
		t.Setenv("DASH0_TEAM_NAME", "team")
		t.Setenv("DASH0_AGENT_NAME", "")
		t.Setenv("DASH0_OMIT_USER_INFO", "true")
		t.Setenv("DASH0_OMIT_IO", "false")
		t.Setenv("DASH0_DEBUG", "true")
		t.Setenv("DASH0_DEBUG_FILE", "/tmp/d.log")

		cfg := Codex.Config()
		assert.Equal(t, "https://ingress.example.com", cfg.OTLPUrl)
		assert.Equal(t, "secret", cfg.AuthToken)
		assert.Equal(t, "ds", cfg.Dataset)
		assert.Equal(t, "team", cfg.TeamName)
		assert.Equal(t, "codex", cfg.AgentName)
		assert.Equal(t, "codex", cfg.HarnessName)
		assert.Equal(t, "openai", cfg.Provider)
		assert.True(t, cfg.OmitUserInfo)
		assert.False(t, cfg.OmitIO)
		assert.True(t, cfg.Debug)
		assert.Equal(t, "/tmp/d.log", cfg.DebugFile)
	})

	// OMIT_IO is the one default that is true, so an unset value must not turn
	// prompt and tool content on.
	t.Run("OMIT_IO defaults to true", func(t *testing.T) {
		t.Setenv("CODEX_PLUGIN_OPTION_OMIT_IO", "")
		t.Setenv("DASH0_OMIT_IO", "")
		assert.True(t, Codex.Config().OmitIO)
	})

	// The secret has no DASH0_ fallback, so it must stay empty here.
	t.Run("the token never comes from DASH0_", func(t *testing.T) {
		t.Setenv("CODEX_PLUGIN_OPTION_AUTH_TOKEN", "")
		t.Setenv("DASH0_AUTH_TOKEN", "leaked")
		assert.Empty(t, Codex.Config().AuthToken)
	})

	// Claude gets its options from the plugin UI as CLAUDE_PLUGIN_OPTION_<KEY>.
	t.Run("claude reads the prefixed options", func(t *testing.T) {
		t.Setenv("CLAUDE_PLUGIN_OPTION_DATASET", "from-plugin-ui")
		t.Setenv("DASH0_DATASET", "from-env")
		assert.Equal(t, "from-plugin-ui", Claude.Config().Dataset)
	})

	// The behavior change for the three agents without a plugin UI: their
	// prefixed form is now read too. Nothing sets it today, so this is capability
	// rather than a change to any existing install.
	t.Run("the other agents also read a prefixed option", func(t *testing.T) {
		t.Setenv("CODEX_PLUGIN_OPTION_DATASET", "from-prefixed")
		t.Setenv("DASH0_DATASET", "from-env")
		assert.Equal(t, "from-prefixed", Codex.Config().Dataset)
	})

	// Config validates before it returns, so no caller can forget to do it and
	// then export to a malformed endpoint.
	t.Run("a malformed endpoint is cleared", func(t *testing.T) {
		t.Setenv("CODEX_PLUGIN_OPTION_OTLP_URL", "")
		t.Setenv("DASH0_OTLP_URL", "ingress.example.com:4318") // no scheme
		assert.Empty(t, Codex.Config().OTLPUrl)
	})

	t.Run("a valid endpoint survives", func(t *testing.T) {
		t.Setenv("CODEX_PLUGIN_OPTION_OTLP_URL", "")
		t.Setenv("DASH0_OTLP_URL", "https://ingress.example.com")
		assert.Equal(t, "https://ingress.example.com", Codex.Config().OTLPUrl)
	})

	t.Run("each agent reports its own identity", func(t *testing.T) {
		t.Setenv("DASH0_AGENT_NAME", "")
		t.Setenv("CLAUDE_PLUGIN_OPTION_AGENT_NAME", "")
		assert.Equal(t, "github-copilot-cli", Copilot.Config().HarnessName)
		assert.Empty(t, Copilot.Config().Provider)
		assert.Equal(t, "anthropic", Claude.Config().Provider)
	})
}

func TestHarnessNameIsNotConfigurable(t *testing.T) {
	t.Setenv("DASH0_AGENT_NAME", "my-team-agent")
	t.Setenv("CLAUDE_PLUGIN_OPTION_AGENT_NAME", "from-plugin-ui")

	assert.Equal(t, "claude-code", Claude.HarnessName())
	assert.Equal(t, "github-copilot-cli", Copilot.HarnessName())

	// The two coincide only by default; an override separates them.
	assert.Equal(t, "from-plugin-ui", Claude.AgentName())
	assert.NotEqual(t, Claude.AgentName(), Claude.HarnessName())
}

// Provider is a plain constant: set for single-vendor agents, empty for the ones
// that proxy several so the provider is resolved per event.
func TestProvider(t *testing.T) {
	assert.Equal(t, "anthropic", Claude.Provider)
	assert.Equal(t, "openai", Codex.Provider)
	assert.Empty(t, Copilot.Provider)
}

func TestPluginOption(t *testing.T) {
	t.Run("prefers the prefixed value", func(t *testing.T) {
		t.Setenv("CLAUDE_PLUGIN_OPTION_DATASET", "from-plugin-ui")
		t.Setenv("DASH0_DATASET", "from-env")
		assert.Equal(t, "from-plugin-ui", Claude.PluginOption("DATASET"))
	})

	// The three agents without a plugin-option mechanism rely on this fallback:
	// nothing sets their prefixed variables except AUTH_TOKEN.
	t.Run("falls back to DASH0_", func(t *testing.T) {
		t.Setenv("CODEX_PLUGIN_OPTION_DATASET", "")
		t.Setenv("DASH0_DATASET", "from-env")
		assert.Equal(t, "from-env", Codex.PluginOption("DATASET"))
	})

	t.Run("empty when neither is set", func(t *testing.T) {
		t.Setenv("CLAUDE_PLUGIN_OPTION_NOPE", "")
		t.Setenv("DASH0_NOPE", "")
		assert.Empty(t, Claude.PluginOption("NOPE"))
	})

	// A blank prefixed value must not shadow a real DASH0_ value.
	t.Run("blank prefixed value does not shadow the fallback", func(t *testing.T) {
		t.Setenv("CLAUDE_PLUGIN_OPTION_DATASET", "")
		t.Setenv("DASH0_DATASET", "from-env")
		assert.Equal(t, "from-env", Claude.PluginOption("DATASET"))
	})
}

func TestPluginOptionBoolVariants(t *testing.T) {
	t.Run("bool defaults to false when unset", func(t *testing.T) {
		t.Setenv("CLAUDE_PLUGIN_OPTION_DEBUG", "")
		t.Setenv("DASH0_DEBUG", "")
		assert.False(t, Claude.PluginOptionBool("DEBUG"))
	})

	t.Run("bool reads either source", func(t *testing.T) {
		t.Setenv("CLAUDE_PLUGIN_OPTION_DEBUG", "true")
		assert.True(t, Claude.PluginOptionBool("DEBUG"))
		t.Setenv("CLAUDE_PLUGIN_OPTION_DEBUG", "")
		t.Setenv("DASH0_DEBUG", "1")
		assert.True(t, Claude.PluginOptionBool("DEBUG"))
	})

	// The prefixed value wins outright rather than being combined with the
	// fallback, so a "true" from the plugin UI beats a "false" in the env.
	t.Run("prefixed value wins a conflict", func(t *testing.T) {
		t.Setenv("CLAUDE_PLUGIN_OPTION_DEBUG", "true")
		t.Setenv("DASH0_DEBUG", "false")
		assert.True(t, Claude.PluginOptionBool("DEBUG"))
	})

	// OMIT_IO defaults to true, so the default must survive an unset option.
	// Both defaults are checked: a false default must not be reported as true.
	t.Run("default is returned only when unset", func(t *testing.T) {
		t.Setenv("CLAUDE_PLUGIN_OPTION_OMIT_IO", "")
		t.Setenv("DASH0_OMIT_IO", "")
		assert.True(t, Claude.PluginOptionBoolDefault("OMIT_IO", true))
		assert.False(t, Claude.PluginOptionBoolDefault("OMIT_IO", false))
	})

	t.Run("an explicit value overrides either default", func(t *testing.T) {
		t.Setenv("CLAUDE_PLUGIN_OPTION_OMIT_IO", "false")
		assert.False(t, Claude.PluginOptionBoolDefault("OMIT_IO", true))

		t.Setenv("CLAUDE_PLUGIN_OPTION_OMIT_IO", "true")
		assert.True(t, Claude.PluginOptionBoolDefault("OMIT_IO", false))
	})
}

// Secrets must never come from DASH0_*, even though PluginOption may.
func TestPluginOptionSecureHasNoFallbackUnlikePluginOption(t *testing.T) {
	t.Setenv("CLAUDE_PLUGIN_OPTION_AUTH_TOKEN", "")
	t.Setenv("DASH0_AUTH_TOKEN", "leaked")

	assert.Empty(t, Claude.PluginOptionSecure("AUTH_TOKEN"))
	assert.Equal(t, "leaked", Claude.PluginOption("AUTH_TOKEN"))
}

func TestDash0Env(t *testing.T) {
	t.Setenv("DASH0_OTLP_URL", "http://localhost:4318")
	assert.Equal(t, "http://localhost:4318", Codex.Dash0Env("OTLP_URL"))
	assert.Empty(t, Codex.Dash0Env("NOT_SET_ANYWHERE"))
}

// The parse rules every boolean option shares: blank means "use the default",
// the comparison is case-insensitive after trimming, and any other present value
// is false rather than an error. Reached here through the DASH0_ fallback.
func TestPluginOptionBoolDefaultParsing(t *testing.T) {
	for _, tc := range []struct {
		value      string
		defaultVal bool
		want       bool
	}{
		{"", true, true},
		{"", false, false},
		{"   ", true, true},
		{"true", false, true},
		{"TRUE", false, true},
		{" True ", false, true},
		{"1", false, true},
		{"false", true, false},
		{"0", true, false},
		{"yes", true, false},
		{"anything", true, false},
	} {
		t.Run("value="+tc.value, func(t *testing.T) {
			t.Setenv("DASH0_OMIT_IO", tc.value)
			assert.Equal(t, tc.want, Codex.PluginOptionBoolDefault("OMIT_IO", tc.defaultVal))
		})
	}
}

// Dash0Env is a method for uniform access, not because it depends on the agent:
// DASH0_* variables are shared, so every Harness must read the same value.
// Prefixed lookups are the ones that must differ.
func TestSharedLookupsIgnoreTheReceiver(t *testing.T) {
	t.Setenv("DASH0_DATASET", "shared")

	assert.Equal(t, Codex.Dash0Env("DATASET"), Copilot.Dash0Env("DATASET"))
}

// writeConfig puts a configuration file in dir/<ConfigDir>/ and returns dir.
func writeConfig(t *testing.T, dir string, h Harness, body string) string {
	t.Helper()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, h.ConfigDir), 0o700))
	require.NoError(t, os.WriteFile(
		filepath.Join(dir, h.ConfigDir, config.Name), []byte(body), 0o600))
	return dir
}

func TestConfigDirIsSetForEveryAgent(t *testing.T) {
	for label, h := range map[string]Harness{
		"Claude": Claude, "Cursor": Cursor, "Codex": Codex, "Copilot": Copilot,
	} {
		assert.Equal(t, "."+h.DataSubdir, h.ConfigDir, "%s.ConfigDir", label)
	}
}

func TestPluginOptionFallsBackToTheConfigFile(t *testing.T) {
	chdirTo(t, writeConfig(t, t.TempDir(), Claude, "---\ndataset: from-file\n---\n"))

	assert.Equal(t, "from-file", Claude.PluginOption("DATASET"))
}

// The order every runtime README documents: the prefixed option, then the file,
// then DASH0_*. The wrappers produced it by exporting each file value into the
// matching DASH0_* variable unconditionally, so a file key beat whatever the
// environment already held.
func TestPluginOptionPrecedence(t *testing.T) {
	chdirTo(t, writeConfig(t, t.TempDir(), Claude, "---\ndataset: from-file\n---\n"))

	t.Run("the file wins over DASH0_", func(t *testing.T) {
		t.Setenv("DASH0_DATASET", "from-dash0")
		assert.Equal(t, "from-file", Claude.PluginOption("DATASET"))
	})

	t.Run("the prefixed option wins over both", func(t *testing.T) {
		t.Setenv("DASH0_DATASET", "from-dash0")
		t.Setenv("CLAUDE_PLUGIN_OPTION_DATASET", "from-option")
		assert.Equal(t, "from-option", Claude.PluginOption("DATASET"))
	})

	t.Run("DASH0_ still applies for a key the file omits", func(t *testing.T) {
		t.Setenv("DASH0_TEAM_NAME", "from-dash0")
		assert.Equal(t, "from-dash0", Claude.PluginOption("TEAM_NAME"))
	})
}

// The token is the one exception, and the only precedence this PR changes: the
// wrappers exported it into the prefixed secure variable rather than a DASH0_
// one, so the file used to outrank the UI and managed settings. It no longer
// does. There is still no DASH0_ fallback, so a token cannot be picked up from an
// environment the agent hands to tool subprocesses.
func TestAuthTokenPrecedence(t *testing.T) {
	chdirTo(t, writeConfig(t, t.TempDir(), Claude, "---\nauth_token: from-file\n---\n"))

	t.Run("the file applies on its own", func(t *testing.T) {
		assert.Equal(t, "from-file", Claude.Config().AuthToken)
	})

	t.Run("the prefixed option wins over the file", func(t *testing.T) {
		t.Setenv("CLAUDE_PLUGIN_OPTION_AUTH_TOKEN", "from-option")
		assert.Equal(t, "from-option", Claude.Config().AuthToken)
	})

	t.Run("DASH0_AUTH_TOKEN is never read", func(t *testing.T) {
		t.Setenv("DASH0_AUTH_TOKEN", "leaked")
		assert.Equal(t, "from-file", Claude.Config().AuthToken)
	})
}

func TestProjectConfigWinsOverHomeConfig(t *testing.T) {
	home := writeConfig(t, t.TempDir(), Claude, "---\ndataset: from-home\nteam_name: home-only\n---\n")
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	chdirTo(t, writeConfig(t, t.TempDir(), Claude, "---\ndataset: from-project\n---\n"))

	assert.Equal(t, "from-project", Claude.PluginOption("DATASET"))
	assert.Empty(t, Claude.PluginOption("TEAM_NAME"),
		"the project file wins whole; keys are not merged from the home file")
}

func TestHomeConfigIsUsedWithoutAProjectFile(t *testing.T) {
	home := writeConfig(t, t.TempDir(), Claude, "---\ndataset: from-home\n---\n")
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	chdirTo(t, t.TempDir())

	assert.Equal(t, "from-home", Claude.PluginOption("DATASET"))
}

func TestAuthTokenFromTheEnvironmentOutranksTheFile(t *testing.T) {
	chdirTo(t, writeConfig(t, t.TempDir(), Claude, "---\nauth_token: from-file\n---\n"))
	t.Setenv("CLAUDE_PLUGIN_OPTION_AUTH_TOKEN", "from-option")

	assert.Equal(t, "from-option", Claude.Config().AuthToken)
}

func TestAuthTokenFromTheFileWhenTheEnvironmentHasNone(t *testing.T) {
	chdirTo(t, writeConfig(t, t.TempDir(), Claude, "---\nauth_token: from-file\n---\n"))

	assert.Equal(t, "from-file", Claude.Config().AuthToken)
}

func TestAuthTokenFromTheEnvironmentWhenTheFileHasNone(t *testing.T) {
	chdirTo(t, writeConfig(t, t.TempDir(), Claude, "---\ndataset: d\n---\n"))
	t.Setenv("CLAUDE_PLUGIN_OPTION_AUTH_TOKEN", "from-option")

	assert.Equal(t, "from-option", Claude.Config().AuthToken)
}

func TestAuthTokenIgnoresDash0Env(t *testing.T) {
	chdirTo(t, t.TempDir())
	t.Setenv("DASH0_AUTH_TOKEN", "from-dash0")

	assert.Empty(t, Claude.Config().AuthToken,
		"a secret must never be picked up from an environment handed to tool subprocesses")
}

// A keychain reference that resolves to nothing must not shadow the other
// sources: the lookup fails on a machine with no such item, and returns empty on
// every platform that has no keychain at all.
func TestKeychainMissWithoutSwallowingTheOtherSources(t *testing.T) {
	chdirTo(t, writeConfig(t, t.TempDir(), Claude,
		"---\nauth_token: from-file\nauth_token_keychain_service: dash0-no-such-service\n---\n"))

	t.Run("falls through to the file", func(t *testing.T) {
		assert.Equal(t, "from-file", Claude.Config().AuthToken)
	})

	t.Run("falls through to the environment first", func(t *testing.T) {
		t.Setenv("CLAUDE_PLUGIN_OPTION_AUTH_TOKEN", "from-option")
		assert.Equal(t, "from-option", Claude.Config().AuthToken)
	})
}

func TestEnabled(t *testing.T) {
	t.Run("no file at all", func(t *testing.T) {
		chdirTo(t, t.TempDir())
		assert.True(t, Claude.Enabled(), "an unconfigured install is not a disabled one")
	})

	t.Run("explicitly disabled in the project file", func(t *testing.T) {
		chdirTo(t, writeConfig(t, t.TempDir(), Claude, "---\nenabled: false\n---\n"))
		assert.False(t, Claude.Enabled())
	})

	t.Run("a file without the key", func(t *testing.T) {
		chdirTo(t, writeConfig(t, t.TempDir(), Claude, "---\ndataset: d\n---\n"))
		assert.True(t, Claude.Enabled())
	})
}

func TestEveryAgentReadsItsOwnConfigDir(t *testing.T) {
	for _, h := range []Harness{Claude, Cursor, Codex, Copilot} {
		t.Run(h.Name, func(t *testing.T) {
			chdirTo(t, writeConfig(t, t.TempDir(), h, "---\ndataset: mine\n---\n"))
			assert.Equal(t, "mine", h.PluginOption("DATASET"))

			for _, other := range []Harness{Claude, Cursor, Codex, Copilot} {
				if other.ConfigDir == h.ConfigDir {
					continue
				}
				assert.Empty(t, other.PluginOption("DATASET"),
					"%s must not read %s's configuration", other.Name, h.Name)
			}
		})
	}
}

// chdirTo moves into dir and drops the memoized configuration, so the next
// lookup re-reads it. A hook process loads once and never moves first; a test
// process runs many scenarios, each with its own file.
func chdirTo(t *testing.T, dir string) {
	t.Helper()
	t.Chdir(dir)
	ResetConfig()
}

// The first load fixes every value for the life of the process, so a later chdir
// cannot change an answer. Codex, Copilot and Cursor chdir into the event's
// workspace after the opt-out check, and without this a project file's
// `enabled: false` was read from one directory and every other value from
// another.
func TestTheFirstLoadFixesTheConfiguration(t *testing.T) {
	spawnDir := writeConfig(t, t.TempDir(), Codex, "---\nenabled: false\ndataset: from-spawn-dir\n---\n")
	workspace := writeConfig(t, t.TempDir(), Codex, "---\ndataset: from-workspace\n---\n")

	chdirTo(t, spawnDir)
	require.False(t, Codex.Enabled(), "the file in the spawn directory disables the plugin")

	// What every entrypoint does next, for git detection.
	t.Chdir(workspace)

	assert.False(t, Codex.Enabled(), "the answer must not change under the chdir")
	assert.Equal(t, "from-spawn-dir", Codex.Config().Dataset,
		"every other value must come from the same file the opt-out was read from")
}
