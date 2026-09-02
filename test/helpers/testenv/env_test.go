// SPDX-FileCopyrightText: Copyright 2026 Dash0 Inc.
// SPDX-License-Identifier: Apache-2.0

package testenv

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestCleanEnv pins which variables Clean removes. Every test that runs plugin
// code in a child process depends on this, so a name that slips through is a test
// suite reading configuration out of the developer's shell.
func TestCleanEnv(t *testing.T) {
	// One per family, plus the prefixed option form for each supported agent,
	// because that is the form harness.PluginOption prefers.
	dropped := []string{
		"CLAUDE_PLUGIN_OPTION_OTLP_URL",
		"CURSOR_PLUGIN_OPTION_OTLP_URL",
		"CODEX_PLUGIN_OPTION_OTLP_URL",
		"COPILOT_PLUGIN_OPTION_OTLP_URL",
		"CLAUDE_PLUGIN_OPTION_AUTH_TOKEN",
		"DASH0_OTLP_URL",
		"DASH0_DATASET",
		"DASH0_COPILOT_OTEL_DIR",
		"DASH0_PLUGIN_DATA",
		"CLAUDE_PLUGIN_DATA",
		"COPILOT_PLUGIN_DATA",
		// Session markers an enclosing Claude Code exports. Leaking these makes a
		// nested Claude Code disable transcript saving, which silently costs the
		// plugin the turn content it reports on.
		"CLAUDE_CODE_CHILD_SESSION",
		"CLAUDE_CODE_SESSION_ID",
		"CLAUDE_CODE_MESSAGING_SOCKET",
		"CLAUDE_CODE_ENTRYPOINT",
		// Dropped with the prefix; callers re-add the credential explicitly.
		"CLAUDE_CODE_OAUTH_TOKEN",
		// Not covered by any prefix above (CLAUDE_CONFIG_, not CLAUDE_CODE_), and
		// it outranks the temp HOME: the install, the onboarding state and the
		// credential file would come from the developer's own ~/.claude.
		"CLAUDE_CONFIG_DIR",
	}
	// Variables a child process legitimately needs, or that name a home rather
	// than plugin configuration. Removing these would break the agent CLIs.
	kept := []string{"PATH", "HOME", "CODEX_HOME", "COPILOT_HOME", "XDG_STATE_HOME"}

	for _, name := range append(append([]string{}, dropped...), kept...) {
		t.Setenv(name, "from-the-shell")
	}

	env := Clean()
	names := map[string]bool{}
	for _, kv := range env {
		name, _, ok := strings.Cut(kv, "=")
		require.True(t, ok, "malformed entry %q", kv)
		names[name] = true
	}

	for _, name := range dropped {
		assert.False(t, names[name],
			"%s must be dropped: it can redirect the plugin's config or state", name)
	}
	for _, name := range kept {
		assert.True(t, names[name], "%s must survive: a child process needs it", name)
	}
}

// TestCleanEnvExtraWins checks that a caller can still set a value Clean
// strips. os/exec keeps the last occurrence of a duplicate key, and the installer
// tests rely on that to pass DASH0_OTLP_URL and DASH0_VERSION deliberately.
func TestCleanEnvExtraWins(t *testing.T) {
	t.Setenv("DASH0_OTLP_URL", "from-the-shell")

	env := Clean("DASH0_OTLP_URL=http://intended", "HOME=/tmp/somewhere")

	assert.Equal(t, "DASH0_OTLP_URL=http://intended", env[len(env)-2],
		"the caller's value must be appended, and last")
	for _, kv := range env[:len(env)-2] {
		assert.NotEqual(t, "DASH0_OTLP_URL=from-the-shell", kv,
			"the shell's value must not survive alongside the caller's")
	}
}
