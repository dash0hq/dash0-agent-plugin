// SPDX-FileCopyrightText: Copyright 2026 Dash0 Inc.
// SPDX-License-Identifier: Apache-2.0

// Package testenv builds the environment a child process under test runs in:
// the developer's own plugin configuration subtracted out, and a throwaway home
// named under every variable that resolves to one.
package testenv

import (
	"os"
	"strings"
)

// Clean returns the process environment with every variable that could
// redirect the plugin's configuration or state removed, then appends extra.
// Use it instead of os.Environ() for any child process that runs plugin code.
//
// These are dropped:
//
//   - <PREFIX>_PLUGIN_OPTION_<KEY>, what an agent injects from its own settings.
//   - DASH0_*, the cross-agent fallback, which also covers DASH0_PLUGIN_DATA and
//     DASH0_COPILOT_OTEL_DIR.
//   - <PREFIX>_PLUGIN_DATA, the per-agent state root.
//   - CLAUDE_CODE_*, the markers Claude Code exports into its own subprocesses.
//   - CLAUDE_CONFIG_DIR, which relocates the whole ~/.claude tree and so outranks
//     the temp HOME a caller sets.
//
// The prefixed option form is the one that matters. harness.PluginOption prefers it
// over every DASH0_* value, so blanking only the DASH0_* name leaves the winning
// form in place: a developer with CODEX_PLUGIN_OPTION_OTLP_URL exported beats the
// endpoint the test set up, and the installers' connectivity check is advisory, so
// the test reports success while a span leaves the machine.
//
// The CLAUDE_CODE_ family matters when a test runs from inside a coding agent, which
// is normal while developing this plugin. Claude Code exports CLAUDE_CODE_CHILD_SESSION
// and friends to everything it spawns; a Claude Code started underneath reads them as
// proof it is a nested child and disables transcript saving, which is where the plugin
// reads a turn's content. That also drops CLAUDE_CODE_OAUTH_TOKEN, which is safe
// because the callers needing a credential append it explicitly afterwards.
//
// extra is appended last: os/exec keeps the final occurrence of a duplicate key, so a
// caller can still override a value it means to test.
func Clean(extra ...string) []string {
	env := os.Environ()
	out := make([]string, 0, len(env)+len(extra))
	for _, kv := range env {
		name, _, ok := strings.Cut(kv, "=")
		if ok && redirectsPlugin(name) {
			continue
		}
		out = append(out, kv)
	}
	return append(out, extra...)
}

// CleanHome is Clean with a throwaway home named under every variable that
// resolves to one; see Home for why that is more than HOME.
//
// Prefer it wherever a child process has a home. Forgetting one of the two names is
// not a failure: the child reads the developer's real home and the test passes
// against config it never wrote.
func CleanHome(home string, extra ...string) []string {
	return Clean(append(Home(home), extra...)...)
}

// redirectsPlugin reports whether an environment variable name can change where
// the plugin reads its configuration or writes its state.
func redirectsPlugin(name string) bool {
	return strings.HasPrefix(name, "DASH0_") ||
		strings.HasPrefix(name, "CLAUDE_CODE_") ||
		strings.Contains(name, "_PLUGIN_OPTION_") ||
		strings.HasSuffix(name, "_PLUGIN_DATA") ||
		// CLAUDE_CONFIG_DIR moves the whole ~/.claude tree, so it outranks the
		// temp home every caller sets. It matches no prefix above, being
		// CLAUDE_CONFIG_ rather than CLAUDE_CODE_.
		name == "CLAUDE_CONFIG_DIR"
}

// Home names one throwaway home directory under every variable that resolves to
// the user's home.
//
// HOME alone is not enough on Windows: os.UserHomeDir, PowerShell's $HOME and the
// .ps1 bootstraps all read USERPROFILE, so a test that sets only HOME writes into
// the developer's real ~/.codex and reads a config it never wrote. Both names are
// always set, because the extra one is inert on POSIX.
func Home(dir string) []string {
	return []string{"HOME=" + dir, "USERPROFILE=" + dir}
}
