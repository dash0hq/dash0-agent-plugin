// SPDX-FileCopyrightText: Copyright 2026 Dash0 Inc.
// SPDX-License-Identifier: Apache-2.0

package consistency

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// hookCommands reads one runtime's hooks file as event name -> commands.
//
// It reads the shape strictly, per the descriptor, rather than accepting
// whichever of the four shapes parses. The runtimes disagree on a nested "hooks"
// array and on "command" against "bash", and each ignores an entry it does not
// recognise. A permissive reader would parse a hooks.json that had drifted to
// another runtime's shape while that runtime registered nothing at all.
func (a Agent) hookCommands(t *testing.T) map[string][]string {
	t.Helper()
	return a.hookCommandsUnder(t, a.HookCommandKey)
}

// hookCommandsWindows is hookCommands for the sibling key a runtime selects on
// Windows. Only for a runtime that has one; see HookWindowsCommandKey.
func (a Agent) hookCommandsWindows(t *testing.T) map[string][]string {
	t.Helper()
	require.NotEmpty(t, a.HookWindowsCommandKey, "%s registers no Windows command key", a.Label)
	return a.hookCommandsUnder(t, a.HookWindowsCommandKey)
}

func (a Agent) hookCommandsUnder(t *testing.T, commandKey string) map[string][]string {
	t.Helper()

	events, ok := readJSON(t, abs(t, a.Hooks))["hooks"].(map[string]any)
	require.True(t, ok, "%s must hold a hooks object", a.Hooks)
	require.NotEmpty(t, events, "%s registers no events", a.Hooks)

	out := make(map[string][]string, len(events))
	for event, raw := range events {
		entries, ok := raw.([]any)
		require.True(t, ok, "%s: %s must hold a list", a.Hooks, event)
		require.NotEmpty(t, entries, "%s: %s has no entries", a.Hooks, event)

		for _, raw := range entries {
			entry, ok := raw.(map[string]any)
			require.True(t, ok, "%s: %s entries must be objects", a.Hooks, event)

			for _, holder := range a.commandHolders(t, event, entry) {
				cmd, ok := holder[commandKey].(string)
				require.True(t, ok, "%s: %s entry must carry a %q string", a.Hooks, event, commandKey)
				out[event] = append(out[event], cmd)
			}
		}
	}
	return out
}

// commandHolders unwraps the one structural difference between the runtimes:
// Claude and Codex nest a further "hooks" array under each matcher entry, while
// Cursor and Copilot put the command on the entry itself.
func (a Agent) commandHolders(t *testing.T, event string, entry map[string]any) []map[string]any {
	t.Helper()

	if !a.HookEntriesNested {
		_, nested := entry["hooks"]
		require.False(t, nested, "%s: %s entry must not nest a hooks array", a.Hooks, event)
		return []map[string]any{entry}
	}

	inner, ok := entry["hooks"].([]any)
	require.True(t, ok, "%s: %s entry must nest a hooks array", a.Hooks, event)
	require.NotEmpty(t, inner, "%s: %s nested hooks array is empty", a.Hooks, event)

	holders := make([]map[string]any, 0, len(inner))
	for _, raw := range inner {
		holder, ok := raw.(map[string]any)
		require.True(t, ok, "%s: %s nested entries must be objects", a.Hooks, event)
		holders = append(holders, holder)
	}
	return holders
}

// TestHookCommandsResolveToTheBootstrap checks every registered event invokes
// this runtime's bootstrap, at a path that resolves against the plugin root the
// runtime expands. A command that does not resolve fails at fork time, per
// event, with no error the user sees.
func TestHookCommandsResolveToTheBootstrap(t *testing.T) {
	for _, a := range Agents {
		t.Run(a.Label, func(t *testing.T) {
			want := filepath.Clean(abs(t, a.Bootstrap))

			for event, commands := range a.hookCommands(t) {
				assert.NotEmpty(t, event, "%s registers an empty event name", a.Hooks)

				for _, cmd := range commands {
					require.True(t, strings.HasPrefix(cmd, a.HookRootPrefix),
						"%s: %s command %q must start with %q so the runtime resolves it against the plugin root",
						a.Hooks, event, cmd, a.HookRootPrefix)

					fields := strings.Fields(strings.TrimPrefix(cmd, a.HookRootPrefix))
					require.NotEmpty(t, fields, "%s: %s command is empty", a.Hooks, event)

					target := filepath.Clean(a.pkgPath(t, fields[0]))
					assert.Equal(t, want, target,
						"%s: %s must invoke %s", a.Hooks, event, a.Bootstrap)

					requireExecutable(t, target, fmt.Sprintf("%s: %s target", a.Hooks, event))
				}
			}
		})
	}
}

// TestHookEventArgv covers the runtimes whose payload omits the event name. The
// registered command has to carry it, and it has to be the event it is
// registered under: a copy-paste that leaves the wrong name there mislabels
// every span of that event.
func TestHookEventArgv(t *testing.T) {
	for _, a := range Agents {
		t.Run(a.Label, func(t *testing.T) {
			for event, commands := range a.hookCommands(t) {
				for _, cmd := range commands {
					fields := strings.Fields(strings.TrimPrefix(cmd, a.HookRootPrefix))

					if !a.HookPassesEvent {
						assert.Len(t, fields, 1,
							"%s: %s passes an argv, but this runtime names the event in the payload", a.Hooks, event)
						continue
					}

					require.Len(t, fields, 2,
						"%s: %s must pass exactly its event name as an argv", a.Hooks, event)
					assert.Equal(t, event, fields[1],
						"%s: %s passes the wrong event name", a.Hooks, event)
				}
			}
		})
	}
}

// TestWindowsHookCommandsResolveToTheBootstrap covers the runtimes that register
// a second command for Windows. A bash-only registration raises no error: the
// runtime selects the variant for the platform, finds none, and the hook does
// nothing. The plugin goes inert on Windows while every other check here passes.
//
// The event set must match the POSIX one exactly. Variants added by hand, event
// by event, are how one event ends up covered on one platform only.
func TestWindowsHookCommandsResolveToTheBootstrap(t *testing.T) {
	for _, a := range agentsWith(t, 2, func(a Agent) bool { return a.HookWindowsCommandKey != "" }) {
		t.Run(a.Label, func(t *testing.T) {
			posix := a.hookCommands(t)
			windows := a.hookCommandsWindows(t)

			require.Len(t, windows, len(posix),
				"%s registers %d events for POSIX and %d for Windows; every event needs both",
				a.Hooks, len(posix), len(windows))

			want := filepath.Clean(abs(t, a.WindowsBootstrap))
			for event, commands := range windows {
				require.Contains(t, posix, event, "%s: %s is registered for Windows only", a.Hooks, event)
				require.Len(t, commands, len(posix[event]),
					"%s: %s registers a different number of commands per platform", a.Hooks, event)

				for _, cmd := range commands {
					// The Windows command is a PowerShell invocation rather than a bare
					// path, so the plugin-root reference sits inside it.
					ref := windowsScriptRef(t, a, event, cmd)
					assert.Equal(t, want, filepath.Clean(a.pkgPath(t, ref)),
						"%s: %s must invoke %s on Windows", a.Hooks, event, a.WindowsBootstrap)
					require.FileExists(t, want)

					if a.HookPassesEvent {
						assert.True(t, strings.HasSuffix(strings.TrimSpace(cmd), " "+event),
							"%s: %s must pass its own event name as the Windows argv, got %q", a.Hooks, event, cmd)
					}
				}
			}
		})
	}
}

// windowsScriptRef extracts the ${PLUGIN_ROOT}-relative script path from a
// PowerShell hook command, which wraps it in quotes and either a `&` call
// operator or a `-File` argument.
func windowsScriptRef(t *testing.T, a Agent, event, cmd string) string {
	t.Helper()

	i := strings.Index(cmd, a.HookRootPrefix)
	require.NotEqual(t, -1, i,
		"%s: %s Windows command %q must reference %q so the runtime resolves it against the plugin root",
		a.Hooks, event, cmd, a.HookRootPrefix)

	rest := cmd[i+len(a.HookRootPrefix):]
	end := strings.IndexAny(rest, "\" '")
	require.NotEqual(t, -1, end, "%s: %s Windows command %q leaves the script path unterminated", a.Hooks, event, cmd)
	return rest[:end]
}

// TestRuntimesWithoutAWindowsHookKeyDeclareTheirRoute pins the two runtimes that
// register one command for every platform, because an absent Windows key looks
// identical whether it was reasoned about or forgotten.
func TestRuntimesWithoutAWindowsHookKeyDeclareTheirRoute(t *testing.T) {
	for _, a := range agentsWith(t, 2, func(a Agent) bool { return a.HookWindowsCommandKey == "" }) {
		t.Run(a.Label, func(t *testing.T) {
			switch a.WindowsHookRoute {
			case routeGitBash:
				// Claude Code runs a hook command through Git Bash on Windows, so the
				// POSIX bootstrap is the Windows one and a .ps1 would be a second
				// copy nothing executes.
				assert.Empty(t, a.WindowsBootstrap,
					"%s takes the Git Bash route, so it must not ship a PowerShell bootstrap", a.Label)
				assert.NoFileExists(t, abs(t, strings.TrimSuffix(a.Bootstrap, ".sh")+".ps1"))

			case routeInstallerRewrites:
				// Cursor fires only the hooks in ~/.cursor/hooks.json, which the
				// installer writes from the shipped file as a template, replacing the
				// command per platform. So the .ps1 must exist and the PowerShell
				// installer must name it.
				require.NotEmpty(t, a.WindowsBootstrap,
					"%s takes the installer-rewrites route, so it must ship a PowerShell bootstrap", a.Label)
				require.FileExists(t, abs(t, a.WindowsBootstrap))

				installer := abs(t, "install-"+a.Label+".ps1")
				require.FileExists(t, installer)
				// Code lines only. The installer's section banners name the file
				// they are about, so grepping the raw text is satisfied by a
				// comment and holds with both real references renamed.
				named := false
				for _, line := range powerShellCodeLines(t, installer) {
					if strings.Contains(line, filepath.Base(a.WindowsBootstrap)) {
						named = true
						break
					}
				}
				assert.True(t, named,
					"%s must install %s, or a Windows install registers a command pointing at nothing",
					filepath.Base(installer), a.WindowsBootstrap)

			default:
				t.Fatalf("%s declares no WindowsHookRoute; record how its hooks reach Windows", a.Label)
			}
		})
	}
}

// One command per event, everywhere.
//
// A second entry for the same event runs the bootstrap twice, so every span of
// that event is emitted twice, and nothing else here notices: the event set is
// unchanged, each command still resolves, each still carries the right argv.
// Duplication is what a careless merge of two hooks files produces.
//
// All four runtimes register exactly one today. If one ever needs two, this is
// the check that makes that a decision.
func TestEachEventRegistersOneCommand(t *testing.T) {
	for _, a := range Agents {
		t.Run(a.Label, func(t *testing.T) {
			for event, commands := range a.hookCommands(t) {
				assert.Len(t, commands, 1,
					"%s registers %d commands for %s, so every span of that event is "+
						"emitted once per command", a.Hooks, len(commands), event)
			}
		})
	}
}

// TestHooksRegisterExactlyTheDeclaredEvents pins the event set each runtime
// registers, in both directions.
//
// Every other check here asks whether a registered event is wired correctly, so
// all of them pass on a file that registers the wrong events correctly. A
// dropped event is silent: the only symptom is a span that stops appearing.
// Adding one can be worse; see HookEvents on the descriptor for Copilot's
// fail-closed preToolUse.
//
// TestWindowsHookCommandsResolveToTheBootstrap checks the Windows commands
// against the same set, event for event.
func TestHooksRegisterExactlyTheDeclaredEvents(t *testing.T) {
	for _, a := range Agents {
		t.Run(a.Label, func(t *testing.T) {
			require.NotEmpty(t, a.HookEvents, "%s declares no HookEvents; record what it registers", a.Label)

			registered := make([]string, 0, len(a.HookEvents))
			for event := range a.hookCommands(t) {
				registered = append(registered, event)
			}
			sort.Strings(registered)

			want := append([]string(nil), a.HookEvents...)
			sort.Strings(want)

			assert.Equal(t, want, registered,
				"%s registers a different event set than the descriptor declares; "+
					"update both, and say in the commit why the event is added or dropped", a.Hooks)
		})
	}
}
