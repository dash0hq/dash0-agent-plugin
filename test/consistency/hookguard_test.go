// SPDX-FileCopyrightText: Copyright 2026 Dash0 Inc.
// SPDX-License-Identifier: Apache-2.0

package consistency

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Every host runs a hook command through a shell and shows the user a bare exit
// code when it is nonzero, without the stderr that would explain it. Codex is the
// strictest case: `$SHELL -lc "<command>"`, and for any code other than 0 or 2 it
// renders "hook exited with code N" and drops our stderr entirely.
//
// So the registered command string — not just the script it names — has to
// tolerate its own absence. That is not hypothetical: `codex plugin` installs
// into a version-scoped root and deletes the old version on update, while a live
// session keeps invoking the path it discovered at startup. Every hook in that
// session then failed with 127 until it was restarted.
//
// A guard inside the bootstrap cannot help, because the bootstrap is the thing
// that is gone. These tests pin the guard into the registrations themselves.
const (
	// The breadcrumb marker, present in the full Codex guard.
	guardBreadcrumb = "hook_script_missing"
	// The two accepted terminators: the full guard ends by forcing a clean exit,
	// the short form swallows a failed invocation.
	guardExit   = "exit 0"
	guardOrTrue = "|| true"
)

func assertGuarded(t *testing.T, source, command string) {
	t.Helper()
	trimmed := strings.TrimSpace(command)
	assert.True(t,
		strings.HasSuffix(trimmed, guardExit) || strings.HasSuffix(trimmed, guardOrTrue),
		"%s: hook command must end in %q or %q so a missing bootstrap cannot surface as a "+
			"nonzero exit in the user's UI; got %q", source, guardExit, guardOrTrue, command)
}

// TestHookManifestCommandsAreGuarded covers the marketplace install path for
// every runtime that registers its bootstrap by path.
func TestHookManifestCommandsAreGuarded(t *testing.T) {
	root := repoRoot(t)

	// Cursor is absent on purpose: its plugin directory
	// (~/.cursor/plugins/local/dash0-agent-plugin) is not version-scoped, so the
	// deleted-root failure cannot happen there, and install-cursor.sh rewrites
	// these commands with jq keyed on the script name.
	manifests := []struct {
		path string
		// key holds the command; Copilot calls it "bash", everyone else "command".
		key string
	}{
		{filepath.Join("codex", "hooks.json"), "command"},
		{filepath.Join("claude", "hooks.json"), "command"},
		{filepath.Join("copilot", "hooks.json"), "bash"},
	}

	for _, m := range manifests {
		t.Run(m.path, func(t *testing.T) {
			raw, err := os.ReadFile(filepath.Join(root, m.path))
			require.NoError(t, err)

			var doc struct {
				Hooks map[string][]json.RawMessage `json:"hooks"`
			}
			require.NoError(t, json.Unmarshal(raw, &doc))
			require.NotEmpty(t, doc.Hooks, "%s has no hooks object", m.path)

			seen := 0
			for event, entries := range doc.Hooks {
				for _, entry := range entries {
					for _, cmd := range commandsIn(t, entry, m.key) {
						assertGuarded(t, m.path+" "+event, cmd)
						seen++
					}
				}
			}
			assert.NotZero(t, seen, "%s: found no hook commands to check", m.path)
		})
	}
}

// TestCodexInstallerCommandIsGuarded covers the other Codex install path.
// install-codex.sh builds the command string itself and hands it to
// `emit-codex-hooks`, so the manifest test above cannot see it.
func TestCodexInstallerCommandIsGuarded(t *testing.T) {
	root := repoRoot(t)

	raw, err := os.ReadFile(filepath.Join(root, "install-codex.sh"))
	require.NoError(t, err)

	m := regexp.MustCompile(`(?m)^HOOK_CMD=(.*)$`).FindSubmatch(raw)
	require.NotNil(t, m, "install-codex.sh no longer assigns HOOK_CMD")

	// The assignment is a shell literal, so strip the quoting that wraps it —
	// the guard's terminator is the last thing inside, not outside.
	cmd := strings.Trim(strings.TrimSpace(string(m[1])), `'"`)
	assertGuarded(t, "install-codex.sh HOOK_CMD", cmd)
	assert.Contains(t, cmd, guardBreadcrumb,
		"install-codex.sh HOOK_CMD must record a breadcrumb when the bootstrap is missing: "+
			"nothing of ours runs in that state, so this is the only way we learn about it")
}

// TestBootstrapsGuardTheBinaryHandoff pins the second nonzero-exit path. A
// bootstrap reaches the binary only after an -x test, but the binary can be
// deleted or replaced in between — hooks run concurrently and every session on
// the machine shares the directory. Whatever the host sees then must still be 0.
//
// The assertion is on the invariant, not on one spelling: the handoff tolerates
// its own failure and the script ends by stating a clean exit. Two forms satisfy
// it, because one does not work everywhere — see the comment on the handoff in
// claude/claude-on-event.sh.
func TestBootstrapsGuardTheBinaryHandoff(t *testing.T) {
	root := repoRoot(t)

	for _, rel := range []string{
		filepath.Join("claude", "claude-on-event.sh"),
		filepath.Join("codex", "codex-on-event.sh"),
		filepath.Join("copilot", "copilot-on-event.sh"),
		filepath.Join("cursor", "cursor-on-event.sh"),
	} {
		t.Run(rel, func(t *testing.T) {
			raw, err := os.ReadFile(filepath.Join(root, rel))
			require.NoError(t, err)
			body := string(raw)

			var code []string
			for line := range strings.SplitSeq(body, "\n") {
				trimmed := strings.TrimSpace(line)
				if trimmed != "" && !strings.HasPrefix(trimmed, "#") {
					code = append(code, trimmed)
				}
			}
			require.NotEmpty(t, code, "%s: no code found", rel)

			handoff := ""
			for _, line := range code {
				if strings.Contains(line, `"$BINARY"`) {
					handoff = line
				}
			}
			require.NotEmpty(t, handoff, "%s: nothing invokes $BINARY", rel)

			assert.True(t, strings.HasSuffix(handoff, guardOrTrue),
				"%s: the handoff to the binary must end in %q so a failed exec or a "+
					"nonzero binary status cannot escape; got %q", rel, guardOrTrue, handoff)
			assert.Equal(t, guardExit, code[len(code)-1],
				"%s: the last statement must be %q", rel, guardExit)

			if strings.HasPrefix(handoff, "exec ") {
				assert.Contains(t, body, "shopt -s execfail",
					"%s: an exec handoff needs `shopt -s execfail`, or bash abandons the "+
						"shell with 126/127 before any guard runs", rel)
			}
		})
	}
}

// commandsIn pulls the command strings out of one hook entry, which is either a
// group holding a "hooks" array or a single handler.
func commandsIn(t *testing.T, entry json.RawMessage, key string) []string {
	t.Helper()

	var group struct {
		Hooks []map[string]any `json:"hooks"`
	}
	require.NoError(t, json.Unmarshal(entry, &group))

	handlers := group.Hooks
	if handlers == nil {
		var single map[string]any
		require.NoError(t, json.Unmarshal(entry, &single))
		handlers = []map[string]any{single}
	}

	var cmds []string
	for _, h := range handlers {
		if cmd, ok := h[key].(string); ok {
			cmds = append(cmds, cmd)
		}
	}
	return cmds
}
