// SPDX-FileCopyrightText: Copyright 2026 Dash0 Inc.
// SPDX-License-Identifier: Apache-2.0

// Package consistency checks the shipped plugin packages. No CLI, no network, no
// credentials, so these run in `go test ./...`.
//
// Every check is table-driven over Agents, so adding a runtime means adding one
// Agent literal. A check for a subset goes through agentsWith, which asserts the
// subset's size, so a runtime cannot slip past by leaving a field empty.
package consistency

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/dash0hq/dash0-agent-plugin/internal/harness"
	"github.com/dash0hq/dash0-agent-plugin/test/helpers/pluginrepo"
)

// The install id both marketplaces resolve, so it is one value, not four.
const pluginName = "dash0-agent-plugin"

// WindowsHookRoute is how a runtime with no HookWindowsCommandKey reaches its
// bootstrap on Windows.
type WindowsHookRoute string

const (
	// A second command under HookWindowsCommandKey, picked by platform.
	routePerCommandKey WindowsHookRoute = "per-command-key"
	// The runtime runs the one registered command through Git Bash, so the POSIX
	// bootstrap serves Windows too and no .ps1 ships.
	routeGitBash WindowsHookRoute = "git-bash"
	// The hooks file is a template; the installer substitutes a per-platform
	// command, so the shipped file names only the POSIX bootstrap.
	routeInstallerRewrites WindowsHookRoute = "installer-rewrites"
)

// Agent describes one shipped runtime package.
type Agent struct {
	Label   string
	Harness harness.Harness

	Manifest string
	// What the manifest's own paths resolve against: "." for the runtimes that
	// install the whole repo, "copilot" for Copilot, which ships only that subtree.
	PluginRoot string
	Bootstrap  string
	// Empty for Claude, which runs hook commands through Git Bash.
	WindowsBootstrap string
	// One implementation inside the shared markers. False only for Claude, whose
	// body cannot be identical: `set -euo pipefail`, `shopt -s execfail` so a
	// failed exec reaches its own fail_open, and the unprefixed legacy cache name.
	SharesBootstrapBody bool
	// The release asset the bootstrap fetches, before the platform.
	AssetStem string
	// The file the bootstrap caches, before the version and the platform. Equal to
	// AssetStem except for Claude, which fetches claude-on-event-… and caches it as
	// on-event-….
	CacheStem string
	// The hooks file the runtime or the installer reads. Set even where the
	// manifest does not declare it.
	Hooks string
	// Empty for a runtime that installs from an external marketplace.
	Marketplace string
	// The README whose table documents every declared userConfig option.
	OptionDocs string

	// False only for Cursor, which silently ignores `hooks` in local-plugin
	// manifests; install-cursor.sh reads Hooks directly.
	ManifestDeclaresHooks bool
	// The expected `skills` value, empty when the manifest must not declare one.
	ManifestSkills string
	// Claude and Cursor auto-discover a root commands/ unless the manifest
	// overrides it.
	ManifestCommandsDeclared bool
	// True only for Claude Code: userConfig is its credential mechanism and not a
	// valid field elsewhere.
	ManifestUserConfig bool

	// The key holding a hook's command: "command", or "bash" for Copilot.
	HookCommandKey string
	// The sibling key holding the Windows command. Empty means one command for
	// every platform, and then WindowsHookRoute says how Windows is reached.
	HookWindowsCommandKey string
	WindowsHookRoute      WindowsHookRoute
	// An event's entries wrap a further "hooks" array rather than carrying the
	// command directly.
	HookEntriesNested bool
	// The prefix every hook command starts with, which the runtime expands to
	// PluginRoot.
	HookRootPrefix string
	// The command appends its event name as an argv, the runtime having left it out
	// of the payload.
	HookPassesEvent bool
	// Exactly what this runtime's hooks file registers. Spelled out rather than
	// read from it, because both directions cost something: dropping Stop silently
	// ends every chat span, and registering Copilot's fail-closed preToolUse puts
	// this plugin between the user and their tool calls.
	HookEvents []string

	// The `@<name>` suffix an install resolves.
	MarketplaceName string
	// A plugin entry's `source` is an object ({source, path}, as Codex requires)
	// rather than a relative path.
	MarketplaceSourceObject bool
	// The marketplace repeats the plugin version and is bumped with it.
	MarketplacePinsVersion bool
}

// Every runtime this repo ships. Keep it complete: the checks derive their
// coverage from it.
var Agents = []Agent{
	{
		Label:                    "claude",
		Harness:                  harness.Claude,
		Manifest:                 ".claude-plugin/plugin.json",
		PluginRoot:               ".",
		Bootstrap:                "claude/claude-on-event.sh",
		AssetStem:                "claude-on-event",
		CacheStem:                "on-event",
		Hooks:                    "claude/hooks.json",
		OptionDocs:               ".claude-plugin/README.md",
		ManifestDeclaresHooks:    true,
		ManifestSkills:           "./claude/skills/",
		ManifestCommandsDeclared: true,
		ManifestUserConfig:       true,
		HookCommandKey:           "command",
		HookEntriesNested:        true,
		HookRootPrefix:           "${CLAUDE_PLUGIN_ROOT}/",
		WindowsHookRoute:         routeGitBash,
		HookEvents: []string{
			"ConfigChange", "CwdChanged", "Elicitation", "ElicitationResult", "FileChanged",
			"InstructionsLoaded", "Notification", "PermissionDenied", "PermissionRequest",
			"PostCompact", "PostToolUse", "PostToolUseFailure", "PreCompact", "PreToolUse",
			"SessionEnd", "SessionStart", "Stop", "StopFailure", "SubagentStart", "SubagentStop",
			"TaskCompleted", "TaskCreated", "TeammateIdle", "UserPromptSubmit",
		},
	},
	{
		Label:                    "cursor",
		Harness:                  harness.Cursor,
		Manifest:                 ".cursor-plugin/plugin.json",
		PluginRoot:               ".",
		Bootstrap:                "cursor/cursor-on-event.sh",
		WindowsBootstrap:         "cursor/cursor-on-event.ps1",
		SharesBootstrapBody:      true,
		AssetStem:                "cursor-on-event",
		CacheStem:                "cursor-on-event",
		Hooks:                    "cursor/hooks.json",
		ManifestDeclaresHooks:    false,
		ManifestSkills:           "./cursor/skills/",
		ManifestCommandsDeclared: true,
		HookCommandKey:           "command",
		HookRootPrefix:           "./",
		WindowsHookRoute:         routeInstallerRewrites,
		HookEvents: []string{
			"afterAgentResponse", "beforeSubmitPrompt", "postToolUse", "postToolUseFailure",
			"preToolUse", "sessionEnd", "sessionStart", "subagentStart", "subagentStop",
		},
	},
	{
		Label:                 "codex",
		Harness:               harness.Codex,
		Manifest:              ".codex-plugin/plugin.json",
		PluginRoot:            ".",
		Bootstrap:             "codex/codex-on-event.sh",
		WindowsBootstrap:      "codex/codex-on-event.ps1",
		SharesBootstrapBody:   true,
		AssetStem:             "codex-on-event",
		CacheStem:             "codex-on-event",
		Hooks:                 "codex/hooks.json",
		Marketplace:           ".agents/plugins/marketplace.json",
		ManifestDeclaresHooks: true,
		HookCommandKey:        "command",
		HookEntriesNested:     true,
		HookRootPrefix:        "${PLUGIN_ROOT}/",
		HookWindowsCommandKey: "commandWindows",
		WindowsHookRoute:      routePerCommandKey,
		HookEvents: []string{
			"PermissionRequest", "PostCompact", "PostToolUse", "PreCompact", "PreToolUse",
			"SessionStart", "Stop", "SubagentStart", "SubagentStop", "UserPromptSubmit",
		},

		MarketplaceName:         "dash0",
		MarketplaceSourceObject: true,
	},
	{
		Label:                 "copilot",
		Harness:               harness.Copilot,
		Manifest:              "copilot/plugin.json",
		PluginRoot:            "copilot",
		Bootstrap:             "copilot/copilot-on-event.sh",
		WindowsBootstrap:      "copilot/copilot-on-event.ps1",
		SharesBootstrapBody:   true,
		AssetStem:             "copilot-on-event",
		CacheStem:             "copilot-on-event",
		Hooks:                 "copilot/hooks.json",
		Marketplace:           ".github/plugin/marketplace.json",
		ManifestDeclaresHooks: true,
		ManifestSkills:        "skills/",
		HookCommandKey:        "bash",
		HookRootPrefix:        "${PLUGIN_ROOT}/",
		HookPassesEvent:       true,
		HookWindowsCommandKey: "powershell",
		WindowsHookRoute:      routePerCommandKey,
		// Lifecycle only. Tool spans come from the native-OTel file, so postToolUse
		// would add nothing, and preToolUse is the one this plugin must never
		// register: Copilot reads a non-zero exit from it as a block.
		HookEvents: []string{"agentStop", "sessionEnd", "sessionStart", "userPromptSubmitted"},

		MarketplaceName:        "dash0",
		MarketplacePinsVersion: true,
	},
}

// agentsWith returns the runtimes matching keep, and fails unless exactly want of
// them do. The count is the point: a silently empty subset reports green while
// testing nothing.
func agentsWith(t *testing.T, want int, keep func(Agent) bool) []Agent {
	t.Helper()
	var out []Agent
	for _, a := range Agents {
		if keep(a) {
			out = append(out, a)
		}
	}
	require.Len(t, out, want,
		"the set of runtimes this check applies to changed; update the expected count and confirm the new runtime is covered")
	return out
}

// The runtimes whose bootstraps carry one implementation inside the shared
// markers, for the checks that compare those bodies to each other.
func sharedBodyBootstraps(t *testing.T) []Agent {
	t.Helper()
	return agentsWith(t, 3, func(a Agent) bool { return a.SharesBootstrapBody })
}

// The runtimes that ship a .ps1. The same three as sharedBodyBootstraps today,
// and deliberately a different predicate: a runtime with its own body and a .ps1
// would otherwise drop out of every PowerShell check while the count still read
// three.
func windowsBootstraps(t *testing.T) []Agent {
	t.Helper()
	return agentsWith(t, 3, func(a Agent) bool { return a.WindowsBootstrap != "" })
}

func abs(t *testing.T, rel string) string {
	t.Helper()
	return filepath.Join(pluginrepo.Root(t), rel)
}

// pkgPath resolves a path stated relative to this runtime's plugin root.
func (a Agent) pkgPath(t *testing.T, rel string) string {
	t.Helper()
	return filepath.Join(pluginrepo.Root(t), a.PluginRoot, strings.TrimPrefix(rel, "./"))
}

func readJSON(t *testing.T, path string) map[string]any {
	t.Helper()
	raw, err := os.ReadFile(path)
	require.NoError(t, err, "reading %s", path)
	var m map[string]any
	require.NoError(t, json.Unmarshal(raw, &m), "parsing %s", path)
	return m
}

func (a Agent) manifest(t *testing.T) map[string]any {
	t.Helper()
	return readJSON(t, abs(t, a.Manifest))
}

func (a Agent) manifestVersion(t *testing.T) string {
	t.Helper()
	v, _ := a.manifest(t)["version"].(string)
	require.NotEmpty(t, v, "%s declares no version", a.Manifest)
	return v
}

func (a Agent) bootstrapBody(t *testing.T) string {
	t.Helper()
	body, err := os.ReadFile(abs(t, a.Bootstrap))
	require.NoError(t, err)
	return string(body)
}

func (a Agent) windowsBootstrapBody(t *testing.T) string {
	t.Helper()
	require.NotEmpty(t, a.WindowsBootstrap, "%s ships no PowerShell bootstrap", a.Label)
	body, err := os.ReadFile(abs(t, a.WindowsBootstrap))
	require.NoError(t, err)
	return string(body)
}

// The two pins agree (TestBootstrapVersionsMatchAcrossPlatforms) but their syntax
// differs.
var (
	versionPin   = regexp.MustCompile(`(?m)^VERSION="([^"]+)"$`)
	psVersionPin = regexp.MustCompile(`(?m)^\$Version = '([^']+)'$`)
)

// The release the bootstrap pins, which decides both the binary it downloads and
// the name it caches it under.
func (a Agent) bootstrapVersion(t *testing.T) string {
	t.Helper()
	m := versionPin.FindStringSubmatch(a.bootstrapBody(t))
	require.Len(t, m, 2, `no VERSION="..." in %s`, a.Bootstrap)
	return m[1]
}

func (a Agent) powerShellVersion(t *testing.T) string {
	t.Helper()
	m := psVersionPin.FindStringSubmatch(a.windowsBootstrapBody(t))
	require.Len(t, m, 2, "no $Version in %s", a.WindowsBootstrap)
	return m[1]
}

// For a check genuinely specific to one runtime, so its paths still come from
// the table.
func agentByLabel(t *testing.T, label string) Agent {
	t.Helper()
	for _, a := range Agents {
		if a.Label == label {
			return a
		}
	}
	require.FailNowf(t, "unknown agent", "no agent labelled %q in Agents", label)
	return Agent{}
}
