// SPDX-FileCopyrightText: Copyright 2026 Dash0 Inc.
// SPDX-License-Identifier: Apache-2.0

//go:build e2e

// The Claude Code canary. The package doc is in main_test.go, next to the
// timeouts and TestMain every canary here shares.
package e2e

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/dash0hq/dash0-agent-plugin/test/helpers/agentterm"
	"github.com/dash0hq/dash0-agent-plugin/test/helpers/gitrepo"
	"github.com/dash0hq/dash0-agent-plugin/test/helpers/otlpcapture"
	"github.com/dash0hq/dash0-agent-plugin/test/helpers/pluginrepo"
	"github.com/dash0hq/dash0-agent-plugin/test/helpers/testenv"
)

// claudeMarketplace names the throwaway marketplace this test stages. Claude
// derives the plugin's data directory as "<plugin>-<marketplace>", and the
// bootstrap resolves its binary from there.
const claudeMarketplace = "local"

// claudePluginName is what this test installs the checkout under, and it is NOT
// the shipped "dash0-agent-plugin".
//
// An organization's Claude Code remote settings can enable the real plugin per
// account. Those settings live outside HOME, so temp-directory isolation cannot
// keep them out, and they appear only in a session, never in the `claude plugin`
// subcommands this install asserts with. Two installs sharing one plugin NAME then
// break each other: Claude reports "running stop hooks…0/2" and this checkout's
// Stop hook goes uninvoked after the first turn.
const claudePluginName = "dash0-agent-plugin-e2e"

// TestE2EFullFlowWithClaude runs a live Claude Code turn with the plugin
// installed the way a user installs it, and asserts spans reach the endpoint.
//
// The install goes through a throwaway marketplace listing this checkout rather
// than --plugin-dir. For a --plugin-dir load Claude computes CLAUDE_PLUGIN_DATA
// under the REAL home and ignores any preset, so the binary would have to be
// staged inside the developer's own ~/.claude where a concurrent session could
// exec it. A marketplace install honours HOME.
//
// Auth: ANTHROPIC_API_KEY, then CLAUDE_CODE_OAUTH_TOKEN (`claude setup-token`),
// then t.Fatal. A skipped canary reports green while proving nothing.
func TestE2EFullFlowWithClaude(t *testing.T) {
	claudeBin, err := pluginrepo.LookAgent(t, "claude")
	if err != nil {
		t.Fatal("claude CLI not found in PATH; install with: npm install -g @anthropic-ai/claude-code")
	}
	auth := claudeAuthEnv(t)

	pluginDir := pluginrepo.Root(t)
	cap, srv := otlpcapture.New(t)
	defer srv.Close()

	home := t.TempDir()
	// Named once, so the install and the assertion cannot drift apart.
	const authToken = "e2e-test-token"
	binDir := installClaude(t, claudeBin, pluginDir, home, srv.URL, authToken)

	// Checked at the end rather than after the turns: when a turn times out the
	// test stops there, and the staging fault is the likeliest cause. A second
	// file means the bootstrap derived a cache name the staging did not and
	// fetched the published release, so every span came from shipped code.
	t.Cleanup(func() {
		entries, err := os.ReadDir(binDir)
		if !assert.NoError(t, err) {
			return
		}
		assert.Len(t, entries, 1,
			"the bootstrap downloaded a release instead of running the staged binary: %v", entries)
	})

	// A git repo, like the other two canaries. The identity the plugin stamps on
	// every span resolves from the repo first and falls back to the OS account, so
	// without this a live run wrote the developer's real name into the capture and
	// exercised a different code path from the other two.
	workDir := t.TempDir()
	gitrepo.Init(t, workDir)
	skill := pluginrepo.StageSkill(t, workDir)

	// One interactive session, two typed prompts, so both turns belong to one session
	// by construction with no resume flag to be wrong about.
	//
	// Tools are allow-listed rather than permission checking bypassed:
	// --permission-mode bypassPermissions raises a sandbox warning preselected on
	// "No, exit", so answering it with Enter quits the agent.
	cmd := exec.Command(claudeBin,
		"--model", "haiku",
		"--allowed-tools", "Write",
		"--allowed-tools", "Bash",
		"--allowed-tools", "Skill",
	)
	cmd.Dir = workDir
	cmd.Env = append(claudeEnv(home), auth)

	ctx, cancel := context.WithTimeout(t.Context(), sessionTimeout)
	defer cancel()

	session := agentterm.Start(t, ctx, cmd)
	defer session.Close()

	// The workspace-trust check stands between startup and the prompt. The entry is
	// chosen by text: this dialog opens on "No, exit", so a bare Enter quits the
	// session and the only symptom is every later barrier timing out.
	session.SelectOption("Quick safety check", "Yes, I trust this folder", onboardingTimeout)

	// A second dialog, and only on this credential: Claude Code notices an
	// ANTHROPIC_API_KEY in the environment and asks whether to use it, defaulting to
	// "No (recommended)". A developer on a subscription authenticates with
	// CLAUDE_CODE_OAUTH_TOKEN and never sees it, which is why this test passed by hand
	// and hung on a runner. Left unanswered it swallows the first prompt, and the only
	// symptom is every span barrier timing out.
	//
	// Keyed on the credential rather than probed for, so a release that stops asking
	// fails here instead of quietly skipping a step.
	if strings.HasPrefix(auth, "ANTHROPIC_API_KEY=") {
		session.SelectOption("Do you want to use this API key", "Yes", onboardingTimeout)
	}

	// Wait for the composer before typing into it. The dialog closing and the input
	// mounting are about 250ms apart, and a keystroke in between is dropped with no
	// echo and no error. The status line is the readiness marker.
	session.Expect("for shortcuts", onboardingTimeout)

	session.Send("Create a file named e2e.txt whose contents are exactly: hi")
	cap.WaitForChatSpans(t, 1, turnTimeout)

	session.Send("Use the " + skill + " skill.")
	cap.WaitForChatSpans(t, 2, turnTimeout)

	requests := cap.Requests()
	t.Logf("requests received: %d", len(requests))
	for _, r := range requests {
		t.Logf("  %s %s auth=%q (%d bytes)", r.Method, r.Path, r.Auth, len(r.Body))
	}
	require.NotEmpty(t, requests, "expected at least one OTLP request from the Claude session with the plugin installed")

	spans := cap.Spans(t)
	require.NotEmpty(t, spans, "requests arrived but carried no spans")
	otlpcapture.LogSpanTree(t, spans)
	cap.AssertAuthToken(t, authToken)

	assert.GreaterOrEqual(t, len(otlpcapture.ChatSpans(spans)), 2,
		"a two-turn session must close two turns, one chat span each; got %s", otlpcapture.DescribeTurns(spans))
	assert.NotEmpty(t, otlpcapture.ToolSpans(spans),
		"the file-writing turn must produce a tool span; got %s", otlpcapture.DescribeTurns(spans))
	assert.Contains(t, otlpcapture.SkillNames(spans), skill,
		"the skill turn must produce a tool span carrying dash0.gen_ai.tool.skill.name=%s; got %s",
		skill, otlpcapture.DescribeTurns(spans))

	otlpcapture.AssertRepoIdentity(t, spans)
}

// claudeAuthEnv resolves the credential this session runs on and returns the whole
// NAME=value entry, because the two sources use different variable names.
//
// Belt and braces: Clean strips only the plugin's own configuration families, so
// an exported credential is inherited anyway. Passing it explicitly makes the choice
// visible in the failure output.
func claudeAuthEnv(t *testing.T) string {
	t.Helper()

	if v := os.Getenv("ANTHROPIC_API_KEY"); v != "" {
		t.Log("authenticating with ANTHROPIC_API_KEY")
		return "ANTHROPIC_API_KEY=" + v
	}
	if v := os.Getenv("CLAUDE_CODE_OAUTH_TOKEN"); v != "" {
		t.Log("authenticating with CLAUDE_CODE_OAUTH_TOKEN")
		return "CLAUDE_CODE_OAUTH_TOKEN=" + v
	}
	t.Fatal("no Claude credential: set ANTHROPIC_API_KEY, or CLAUDE_CODE_OAUTH_TOKEN " +
		"(mint one with `claude setup-token`); required for the Claude e2e test")
	return ""
}

// claudeEnv is the environment every Claude child process gets. CleanHome drops
// everything in the developer's shell that could redirect the plugin's telemetry or
// state, CLAUDE_PLUGIN_DATA included; Claude computes that per session anyway.
func claudeEnv(home string) []string {
	return testenv.CleanHome(home)
}

// installClaude installs the plugin from a throwaway marketplace listing this
// checkout, then stages what a fresh install cannot supply itself.
//
// Claude Code ships through dash0hq/claude-marketplace, so there is no manifest in
// this repo to install from and a local one is staged. Credentials go in the config
// file the bootstrap parses rather than `plugin install --config`, which stores the
// token in the OS keychain, outside HOME and left behind after the test.
// installClaude returns the plugin-data bin directory it staged the binary in, so
// the canary can check after the session that the bootstrap ran that binary rather
// than downloading a release.
func installClaude(t *testing.T, claudeBin, pluginDir, home, otlpURL, token string) string {
	t.Helper()

	marketplace := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(marketplace, ".claude-plugin"), 0o755))
	stagePluginUnderTestName(t, pluginDir, filepath.Join(marketplace, claudePluginName))
	require.NoError(t, os.WriteFile(
		filepath.Join(marketplace, ".claude-plugin", "marketplace.json"),
		[]byte(fmt.Sprintf(`{
  "name": %q,
  "owner": { "name": "e2e" },
  "plugins": [ { "name": %q, "source": "./%s" } ]
}`, claudeMarketplace, claudePluginName, claudePluginName)), 0o644))

	require.NoError(t, os.MkdirAll(filepath.Join(home, ".claude"), 0o755))

	// A fresh HOME opens on first-run onboarding, which swallows the first typed
	// prompt. These two files are what a completed onboarding leaves behind. They do
	// not cover the folder-trust dialog, which the session answers explicitly.
	require.NoError(t, os.WriteFile(filepath.Join(home, ".claude", "settings.json"),
		[]byte(`{"theme":"dark"}`), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(home, ".claude.json"),
		[]byte(`{"hasCompletedOnboarding":true,"migrationVersion":13}`), 0o644))

	cfg := fmt.Sprintf("---\notlp_url: %q\nauth_token: %q\n---\n", otlpURL, token)
	require.NoError(t, os.WriteFile(
		filepath.Join(home, ".claude", "dash0-agent-plugin.local.md"), []byte(cfg), 0o600))

	run := func(args ...string) string {
		t.Helper()
		cmd := exec.Command(claudeBin, args...)
		cmd.Env = claudeEnv(home)
		out, err := cmd.CombinedOutput()
		require.NoError(t, err, "claude %v failed:\n%s", args, out)
		return string(out)
	}
	run("plugin", "marketplace", "add", marketplace, "--scope", "user")
	run("plugin", "install", claudePluginName+"@"+claudeMarketplace, "--scope", "user")

	// A successful install is not a loaded plugin. Claude Code installs one whose
	// manifest it cannot resolve and reports "failed to load" on this listing only,
	// registering no hooks; the session then runs a whole turn and exports nothing,
	// three minutes before the span barrier says so. Asserted here, where the message
	// names the path it rejected.
	listed := run("plugin", "list")
	require.Contains(t, listed, "enabled",
		"the plugin installed but did not load, so no hooks are registered:\n%s", listed)

	// Claude derives the data directory from the plugin and marketplace names, and the
	// bootstrap resolves its binary from bin/ underneath, so staging it there is what
	// makes the session run THIS checkout. The cache name is unprefixed on purpose;
	// see .goreleaser.yaml.
	version := pluginrepo.BootstrapVersion(t, pluginDir, "claude/claude-on-event.sh")
	binDir := filepath.Join(home, ".claude", "plugins", "data",
		claudePluginName+"-"+claudeMarketplace, "bin")
	require.NoError(t, os.MkdirAll(binDir, 0o755))
	pluginrepo.CopyExecutable(t,
		pluginrepo.BuildBinary(t, pluginDir, "./cmd/claude-on-event"),
		filepath.Join(binDir, pluginrepo.CachedBinary(t, "on-event", version)))

	return binDir
}

// stagePluginUnderTestName assembles a plugin directory at dest that is this
// checkout under claudePluginName.
//
// A rewritten manifest plus a symlink, not a copy. Everything the manifest points
// at lives under claude/, so linking that directory keeps the code identical to the
// working tree and an edit is picked up by the next run.
func stagePluginUnderTestName(t *testing.T, pluginDir, dest string) {
	t.Helper()

	require.NoError(t, os.MkdirAll(filepath.Join(dest, ".claude-plugin"), 0o755))
	// Copied, not symlinked. Claude Code resolves every manifest path and refuses one
	// that leaves the marketplace directory, so a symlink into the checkout loads
	// nothing: "Path escapes plugin directory: ./claude/hooks.json". The plugin then
	// installs and reports "failed to load", no hooks are registered, and the only
	// symptom is a turn that completes and exports no span. Windows never had the
	// choice anyway — it grants symlink creation only to an elevated process or with
	// Developer Mode on, which a runner promises neither of.
	pluginrepo.CopyDir(t, filepath.Join(pluginDir, "claude"), filepath.Join(dest, "claude"))

	raw, err := os.ReadFile(filepath.Join(pluginDir, ".claude-plugin", "plugin.json"))
	require.NoError(t, err, "reading the plugin manifest to rename")

	var manifest map[string]any
	require.NoError(t, json.Unmarshal(raw, &manifest), "the plugin manifest must be valid JSON")

	shipped, _ := manifest["name"].(string)
	require.Equal(t, "dash0-agent-plugin", shipped,
		"the shipped plugin name changed; claudePluginName must stay distinct from it")
	manifest["name"] = claudePluginName

	renamed, err := json.MarshalIndent(manifest, "", "  ")
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(
		filepath.Join(dest, ".claude-plugin", "plugin.json"), renamed, 0o644))
}
