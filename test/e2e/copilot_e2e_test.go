// SPDX-FileCopyrightText: Copyright 2026 Dash0 Inc.
// SPDX-License-Identifier: Apache-2.0

//go:build e2e

package e2e

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
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

// copilotMarketplace and copilotPlugin are what .github/plugin/marketplace.json
// declares. Copilot lays out both the install and the plugin's data directory as
// <name>/<plugin> under COPILOT_HOME, so these decide where the bootstrap looks.
const (
	copilotMarketplace = "dash0"
	copilotPlugin      = "dash0-agent-plugin"
)

// TestE2EFullFlowWithCopilot is the Copilot drift canary. It installs the plugin
// the way a customer does, through `plugin marketplace add` and `plugin install`,
// then runs a live turn and asserts the chat span carries per-turn token usage.
//
// The whole shipped path is under test: Copilot reads copilot/hooks.json, invokes
// copilot/copilot-on-event.sh, and the bootstrap resolves its credentials from the
// config file. An earlier version registered a hand-written hook pointing at a
// wrapper script, so neither shipped file was ever executed by a real agent.
//
// FAILS rather than skips when the CLI or the token is missing.
func TestE2EFullFlowWithCopilot(t *testing.T) {
	token := os.Getenv("COPILOT_GITHUB_TOKEN")
	if token == "" {
		t.Fatal("COPILOT_GITHUB_TOKEN not set — required for e2e test")
	}
	copilotBin, err := pluginrepo.LookAgent(t, "copilot")
	if err != nil {
		t.Fatal("copilot CLI not found — install with: npm install -g @github/copilot")
	}

	pluginDir := pluginrepo.Root(t)
	cap, srv := otlpcapture.New(t)
	defer srv.Close()

	// Two throwaway homes: the first carries the credentials file, the plugin's
	// state and the native-OTel directory; COPILOT_HOME carries the install.
	home := t.TempDir()
	copilotHome := t.TempDir()
	// Named once, so the install and the assertion cannot drift apart.
	const authToken = "e2e-copilot-token"
	binDir := installCopilot(t, copilotBin, pluginDir, home, copilotHome, srv.URL, authToken)

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

	// The launcher and the hook agree on a fixed path under the home rather than
	// an env var, because Copilot does not pass the launch environment to hooks;
	// see copilot.OtelDir.
	otelDir := filepath.Join(home, ".local", "state", "dash0-agent-plugin", "copilot", "otel")
	require.NoError(t, os.MkdirAll(otelDir, 0o755))

	workDir := t.TempDir()
	gitrepo.Init(t, workDir)
	skill := pluginrepo.StageSkill(t, workDir)

	ctx, cancel := context.WithTimeout(context.Background(), sessionTimeout)
	defer cancel()

	env := append(copilotEnv(home, copilotHome),
		"COPILOT_GITHUB_TOKEN="+token,
		"COPILOT_OTEL_ENABLED=true",
		"COPILOT_OTEL_FILE_EXPORTER_PATH="+filepath.Join(otelDir, "otel.jsonl"),
	)

	// One interactive session, two typed prompts, so both turns share one session by
	// construction with no --continue to be wrong about. Copilot discovers
	// .claude/skills/ as a project source, which is where StageSkill puts it.
	//
	// The pty gives the child its own session and controlling terminal, so Copilot's
	// exit-time cleanup can no longer signal the test binary's process group.
	cmd := exec.Command(copilotBin, "--allow-all-tools", "-C", workDir)
	cmd.Env = env

	session := agentterm.Start(t, ctx, cmd)
	defer session.Close()

	// Copilot asks about the working directory before it runs anything, and
	// --allow-all-tools does not cover it. Chosen by text, because a default is not a
	// contract; see the Claude canary, where that order flipped to the destructive
	// answer. "Yes" and not "Yes, and remember this folder", which would record a temp
	// directory in the developer's real config.
	session.SelectOption("Do you trust the files in this folder", "Yes", onboardingTimeout)

	// Wait for the composer: the dialog closing and the input mounting are not the
	// same moment, and a keystroke in between is dropped silently. Copilot's status
	// line reads "open sidebar" where Claude's reads "for shortcuts".
	session.Expect("open sidebar", onboardingTimeout)

	session.Send("Create a file named e2e.txt whose contents are exactly: hi")
	cap.WaitForChatSpans(t, 1, turnTimeout)

	session.Send("Use the " + skill + " skill.")
	cap.WaitForChatSpans(t, 2, turnTimeout)

	// The chat span is not the last thing this turn exports. cmd/copilot-on-event
	// sends it from pipeline.Process and only then follows with the turn's tool
	// spans from copilot.EmitTurn, so the barrier above can release while the skill
	// span is still on the wire — a race the assertions below lost on a Windows
	// runner while passing on Linux and on the run before it.
	cap.WaitForSkillSpan(t, skill, spanTimeout)

	spans := cap.Spans(t)
	require.NotEmpty(t, spans, "no spans from a live Copilot session; the plugin's hooks did not fire")
	otlpcapture.LogSpanTree(t, spans)
	cap.AssertAuthToken(t, authToken)

	chatWithUsage := false
	for _, s := range spans {
		if strings.HasPrefix(s.Name, "chat") && otlpcapture.SpanHasPositiveTokenUsage(s) {
			chatWithUsage = true
		}
	}
	assert.True(t, chatWithUsage,
		"expected a canonical chat span carrying per-turn gen_ai.usage.*_tokens sourced from the native-OTel file")

	assert.GreaterOrEqual(t, len(otlpcapture.ChatSpans(spans)), 2,
		"a two-turn session must close two turns, one chat span each; got %s", otlpcapture.DescribeTurns(spans))
	assert.NotEmpty(t, otlpcapture.ToolSpans(spans),
		"the file-writing turn must produce a tool span; got %s", otlpcapture.DescribeTurns(spans))
	assert.Contains(t, otlpcapture.SkillNames(spans), skill,
		"the skill turn must produce a tool span carrying dash0.gen_ai.tool.skill.name=%s; got %s",
		skill, otlpcapture.DescribeTurns(spans))

	otlpcapture.AssertRepoIdentity(t, spans)
}

// copilotEnv is the environment every Copilot child process gets: the two throwaway
// homes, with CleanHome dropping everything from the developer's shell that could
// redirect the plugin's state or telemetry. XDG_STATE_HOME is blanked separately
// because it is not plugin-specific; empty counts as unset in both the bootstrap and
// harness.DataDir.
func copilotEnv(home, copilotHome string) []string {
	return testenv.CleanHome(home,
		"COPILOT_HOME="+copilotHome,
		"XDG_STATE_HOME=",
	)
}

// installCopilot installs the plugin the customer way and stages what a fresh
// install cannot supply itself.
//
// The credentials go in the config file the bootstrap parses, not the environment,
// because Copilot does not pass the launch environment to hooks. The binary is
// pre-staged at the version-pinned path so the bootstrap runs THIS checkout; a wrong
// path silently tests the last published binary, which the file count guards against.
// installCopilot returns the plugin-data bin directory it staged the binary in,
// so the canary can check after the session that the bootstrap ran that binary
// rather than downloading a release.
func installCopilot(t *testing.T, copilotBin, pluginDir, home, copilotHome, otlpURL, token string) string {
	t.Helper()

	require.NoError(t, os.MkdirAll(filepath.Join(home, ".copilot"), 0o755))
	cfg := fmt.Sprintf("---\notlp_url: %q\nauth_token: %q\n---\n", otlpURL, token)
	require.NoError(t, os.WriteFile(
		filepath.Join(home, ".copilot", "dash0-agent-plugin.local.md"), []byte(cfg), 0o600))

	// Where the bootstrap actually looks. Copilot exports
	// COPILOT_PLUGIN_DATA=<COPILOT_HOME>/plugin-data/<marketplace>/<plugin> to the hooks
	// a plugin declares, and the bootstrap prefers it over the XDG fallback. Staging
	// under the XDG path is a silent miss: the bootstrap downloads the last release and
	// fails open, so the test reports a timeout with an empty capture.
	version := pluginrepo.BootstrapVersion(t, pluginDir, "copilot/copilot-on-event.sh")
	binDir := filepath.Join(copilotHome, "plugin-data", copilotMarketplace, copilotPlugin, "bin")
	require.NoError(t, os.MkdirAll(binDir, 0o755))
	pluginrepo.CopyExecutable(t,
		pluginrepo.BuildBinary(t, pluginDir, "./cmd/copilot-on-event"),
		filepath.Join(binDir, pluginrepo.CachedBinary(t, "copilot-on-event", version)))

	run := func(args ...string) string {
		t.Helper()
		cmd := exec.Command(copilotBin, args...)
		cmd.Env = copilotEnv(home, copilotHome)
		out, err := cmd.CombinedOutput()
		require.NoError(t, err, "copilot %v failed:\n%s", args, out)
		return string(out)
	}

	// The in-repo marketplace lists the plugin with source "./copilot". Install places
	// that tree and writes enabledPlugins into COPILOT_HOME/settings.json.
	run("plugin", "marketplace", "add", pluginDir)
	run("plugin", "install", copilotPlugin+"@"+copilotMarketplace)
	require.Contains(t, run("plugin", "list"), copilotPlugin+"@"+copilotMarketplace,
		"the plugin must be installed and enabled before a session can fire its hooks")

	// The hooks the session reads and the script they point at must both exist, or a
	// missing span later is ambiguous. Checked in the SOURCE tree: Copilot 1.0.81
	// stopped copying a directory-source plugin and now loads it live, so asserting on
	// the copy failed the canary at install time the day that shipped.
	//
	// The bootstrap for THIS platform: Copilot selects the "powershell" command
	// key on Windows, so naming the .sh there would assert a file the session
	// never invokes.
	bootstrap := "copilot-on-event.sh"
	if runtime.GOOS == "windows" {
		bootstrap = "copilot-on-event.ps1"
	}
	for _, f := range []string{"hooks.json", bootstrap} {
		require.FileExists(t, filepath.Join(pluginDir, "copilot", f))
	}

	return binDir
}
