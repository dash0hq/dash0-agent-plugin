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

// The process environment with every variable that could redirect the plugin's
// configuration or state removed, then extra appended. Use it instead of
// os.Environ() for any child process that runs plugin code.
//
// The prefixed option form is the one that matters: harness.PluginOption prefers
// it over every DASH0_* value, so blanking only the DASH0_* name leaves the winner
// in place. A developer with CODEX_PLUGIN_OPTION_OTLP_URL exported then beats the
// endpoint the test set up, and since the connectivity check is advisory the test
// reports success while a span leaves the machine.
//
// The CLAUDE_CODE_ family matters because these tests are written from inside a
// coding agent. Claude Code exports CLAUDE_CODE_CHILD_SESSION and friends to
// everything it spawns, and a Claude Code started underneath reads them as proof
// it is a nested child and disables transcript saving, which is where the plugin
// reads a turn's content. CLAUDE_CODE_OAUTH_TOKEN goes with them, callers needing
// a credential appending it explicitly.
//
// extra is appended last, os/exec keeping the final occurrence of a duplicate key,
// so a caller can still override a value it means to test.
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

// Clean with a throwaway home under every variable that resolves to one. Prefer it
// wherever a child process has a home: naming only one of them is not a failure,
// the child reading the developer's real home and the test passing against config
// it never wrote.
func CleanHome(home string, extra ...string) []string {
	return Clean(append(Home(home), extra...)...)
}

// Whether a name can change where the plugin reads its configuration or writes its
// state.
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

// One throwaway home under every variable that resolves to the user's home. HOME
// alone is not enough on Windows, where os.UserHomeDir, PowerShell's $HOME and the
// .ps1 bootstraps read USERPROFILE. Both are always set, the extra one being inert
// on POSIX.
func Home(dir string) []string {
	return []string{"HOME=" + dir, "USERPROFILE=" + dir}
}
