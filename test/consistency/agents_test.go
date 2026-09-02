// SPDX-FileCopyrightText: Copyright 2026 Dash0 Inc.
// SPDX-License-Identifier: Apache-2.0

// Package consistency holds deterministic, no-auth checks over the shipped plugin
// packages. They need no CLI, no network and no credentials, so they run in
// `go test ./...` and fail on a laptop instead of in a release.
//
// Most ask the same question of every runtime and are table-driven over Agents
// below, so adding a runtime means adding one Agent literal. A check for a subset
// filters Agents and asserts that subset's size (see agentsWith), so a new runtime
// cannot slip past by leaving a field empty.
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

// pluginName is the install id every runtime's manifest declares. The
// marketplaces resolve a plugin by this name, so it is one value, not four.
const pluginName = "dash0-agent-plugin"

// WindowsHookRoute names how a runtime reaches its bootstrap on Windows.
type WindowsHookRoute string

const (
	// routePerCommandKey: the hooks file carries a second command under
	// HookWindowsCommandKey and the runtime picks one by platform.
	routePerCommandKey WindowsHookRoute = "per-command-key"
	// routeGitBash: the runtime runs the single registered command through Git
	// Bash, so the POSIX bootstrap serves Windows too and no .ps1 is shipped.
	routeGitBash WindowsHookRoute = "git-bash"
	// routeInstallerRewrites: the hooks file is a template and the installer
	// substitutes a per-platform command into it, so the shipped file names only
	// the POSIX bootstrap.
	routeInstallerRewrites WindowsHookRoute = "installer-rewrites"
)

// Agent describes one shipped runtime package: where its files live and the schema
// facts its runtime enforces.
type Agent struct {
	// Label names the runtime and the subtest.
	Label string
	// Harness is the constant this runtime's entrypoint uses.
	Harness harness.Harness

	// Manifest is the repo-relative plugin manifest.
	Manifest string
	// PluginRoot is what the manifest's paths resolve against: "." for the
	// runtimes that install the whole repo, "copilot" for Copilot, which ships
	// only that subtree.
	PluginRoot string
	// Bootstrap is the repo-relative hook entry script.
	Bootstrap string
	// WindowsBootstrap is the PowerShell hook entry script, empty for Claude:
	// Claude Code runs hook commands through Git Bash, so Bootstrap serves both.
	WindowsBootstrap string
	// SharesBootstrapBody is true for the three runtimes whose bootstraps carry one
	// implementation inside the shared markers. False only for Claude, whose body
	// cannot be identical: it uses `set -euo pipefail`, sets `shopt -s execfail`
	// so a failed exec reaches its own fail_open, and keeps the unprefixed legacy
	// cache name.
	//
	// The name is about the shared body, not about failing open. Claude is the
	// one bootstrap that always exits 0; the three that share a body end in a
	// bare `exec`, which is what TestAnUnrunnableCachedBinaryIsKept records.
	SharesBootstrapBody bool
	// AssetStem is the stem of the release asset the bootstrap fetches, before
	// the platform. It must match what .goreleaser.yaml publishes; see
	// TestBootstrapsFetchTheAssetTheReleasePublishes.
	AssetStem string
	// CacheStem is the stem of the file the bootstrap caches, before the version
	// and the platform. It equals AssetStem everywhere except Claude, which
	// fetches claude-on-event-… and caches it as on-event-….
	CacheStem string
	// Hooks is the hooks file the runtime or the installer reads. Set even when
	// the manifest does not declare it; see ManifestDeclaresHooks.
	Hooks string
	// Marketplace is the repo-relative marketplace manifest, empty for a runtime
	// that installs from an external marketplace.
	Marketplace string
	// OptionDocs is the README whose table must document every declared
	// userConfig option, empty for a runtime with no userConfig.
	OptionDocs string

	// ManifestDeclaresHooks is false only for Cursor, which silently ignores
	// `hooks` in local-plugin manifests; install-cursor.sh reads Hooks directly.
	ManifestDeclaresHooks bool
	// ManifestSkills is the expected `skills` value, empty when the manifest must
	// not declare one.
	ManifestSkills string
	// ManifestCommandsDeclared requires the `commands` key. Cursor and Claude
	// auto-discover a root commands/ unless the manifest overrides it.
	ManifestCommandsDeclared bool
	// ManifestUserConfig is true only for Claude Code: userConfig is its
	// credential mechanism and not a valid field for the other runtimes.
	ManifestUserConfig bool

	// HookCommandKey is the key holding a hook's command: "command" for Claude,
	// Cursor and Codex, "bash" for Copilot.
	HookCommandKey string
	// HookWindowsCommandKey is the sibling key holding the Windows command:
	// "commandWindows" for Codex, "powershell" for Copilot. Empty means one
	// command for every platform; see WindowsHookRoute.
	HookWindowsCommandKey string
	// WindowsHookRoute records how a runtime with no HookWindowsCommandKey reaches
	// its Windows bootstrap. Documentation with an assertion attached: an empty key
	// reads the same whether deliberate or forgotten, and forgetting it means the
	// hooks silently do nothing on Windows.
	WindowsHookRoute WindowsHookRoute
	// HookEntriesNested is true when an event's entries wrap a further "hooks"
	// array (Claude, Codex) rather than carrying the command directly.
	HookEntriesNested bool
	// HookRootPrefix is the prefix every hook command starts with, which the
	// runtime expands to PluginRoot.
	HookRootPrefix string
	// HookPassesEvent is true when the command appends its event name as an
	// argv, because the runtime does not put it in the payload.
	HookPassesEvent bool
	// HookEvents is exactly what this runtime's hooks file registers. Spelled out
	// rather than read from the file because both directions cost something:
	// dropping Stop silently ends every chat span, and Copilot's preToolUse is
	// fail-closed, so registering it puts this plugin between the user and their
	// tool calls.
	HookEvents []string

	// MarketplaceName is the `@<name>` suffix an install resolves.
	MarketplaceName string
	// MarketplaceSourceObject is true when a plugin entry's `source` is an object
	// ({source, path}, as Codex requires) rather than a relative path string.
	MarketplaceSourceObject bool
	// MarketplacePinsVersion is true when the marketplace repeats the plugin
	// version and must therefore be bumped with it.
	MarketplacePinsVersion bool
}

// Agents is every runtime this repo ships. Keep it complete: the table-driven
// checks derive their coverage from it.
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
// them match. The count is the point: a silently empty subset reports green while
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

// failOpenBootstraps are the runtimes whose bootstraps carry one implementation
// inside the shared markers; see SharesBootstrapBody. For the checks that compare
// those bodies to each other.
//
// The name is historical and slightly wrong: it is the shared body that defines
// the set, not failing open. Claude is excluded here and is the bootstrap that
// fails open most completely.
func failOpenBootstraps(t *testing.T) []Agent {
	t.Helper()
	return agentsWith(t, 3, func(a Agent) bool { return a.SharesBootstrapBody })
}

// windowsBootstraps are the runtimes that ship a .ps1, which is what every
// PowerShell check is actually about.
//
// The same three as failOpenBootstraps today, and deliberately not the same
// predicate. A runtime with its own bootstrap body and a .ps1 would drop out of
// every PowerShell contract while agentsWith still counted three and stayed
// green, which is exactly the silent gap the count is supposed to prevent.
func windowsBootstraps(t *testing.T) []Agent {
	t.Helper()
	return agentsWith(t, 3, func(a Agent) bool { return a.WindowsBootstrap != "" })
}

// abs resolves a repo-relative path.
func abs(t *testing.T, rel string) string {
	t.Helper()
	return filepath.Join(pluginrepo.Root(t), rel)
}

// pkgPath resolves a path the manifest or a hook command states relative to this
// runtime's plugin root.
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

// manifestVersion is the version the runtime's manifest declares.
func (a Agent) manifestVersion(t *testing.T) string {
	t.Helper()
	v, _ := a.manifest(t)["version"].(string)
	require.NotEmpty(t, v, "%s declares no version", a.Manifest)
	return v
}

// bootstrapBody is the POSIX bootstrap's text.
func (a Agent) bootstrapBody(t *testing.T) string {
	t.Helper()
	body, err := os.ReadFile(abs(t, a.Bootstrap))
	require.NoError(t, err)
	return string(body)
}

// windowsBootstrapBody is the PowerShell bootstrap's text.
func (a Agent) windowsBootstrapBody(t *testing.T) string {
	t.Helper()
	require.NotEmpty(t, a.WindowsBootstrap, "%s ships no PowerShell bootstrap", a.Label)
	body, err := os.ReadFile(abs(t, a.WindowsBootstrap))
	require.NoError(t, err)
	return string(body)
}

// The two version pins, side by side because they must agree and their syntax
// differs; TestBootstrapVersionsMatchAcrossPlatforms is what holds them together.
var (
	versionPin   = regexp.MustCompile(`(?m)^VERSION="([^"]+)"$`)
	psVersionPin = regexp.MustCompile(`(?m)^\$Version = '([^']+)'$`)
)

// bootstrapVersion is the release the bootstrap pins, which decides both the
// binary it downloads and the name it caches it under.
func (a Agent) bootstrapVersion(t *testing.T) string {
	t.Helper()
	m := versionPin.FindStringSubmatch(a.bootstrapBody(t))
	require.Len(t, m, 2, `no VERSION="..." in %s`, a.Bootstrap)
	return m[1]
}

// powerShellVersion is the release the PowerShell bootstrap pins.
func (a Agent) powerShellVersion(t *testing.T) string {
	t.Helper()
	m := psVersionPin.FindStringSubmatch(a.windowsBootstrapBody(t))
	require.Len(t, m, 2, "no $Version in %s", a.WindowsBootstrap)
	return m[1]
}

// agentByLabel returns one runtime's descriptor, for a check genuinely specific to
// one runtime, so the paths still come from one place.
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
