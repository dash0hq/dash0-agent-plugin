// SPDX-FileCopyrightText: Copyright 2026 Dash0 Inc.
// SPDX-License-Identifier: Apache-2.0

//go:build e2e

package e2e

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/dash0hq/dash0-agent-plugin/test/helpers/agentterm"
	"github.com/dash0hq/dash0-agent-plugin/test/helpers/gitrepo"
	"github.com/dash0hq/dash0-agent-plugin/test/helpers/otlpcapture"
	"github.com/dash0hq/dash0-agent-plugin/test/helpers/pluginrepo"
	"github.com/dash0hq/dash0-agent-plugin/test/helpers/testenv"
)

// TestE2EFullFlowWithCodex is the Codex drift canary: the real CLI, hooks
// installed the way a customer installs them (registered in config.toml and
// PRE-TRUSTED, no --dangerously-bypass-hook-trust), asserting a live session
// produces telemetry in the shape the plugin parses.
//
// Where the golden test replays frozen fixtures, this catches what a new Codex
// version can change: payload and event renames, hook contract drift, and
// hook-trust serialization changes that make the reproduced trusted_hash stop
// matching. Codex then skips the hooks silently, so no spans arrive.
//
// FAILS rather than skips when the CLI or auth is missing. Auth is OPENAI_API_KEY
// via `codex login --with-api-key` (CI, a service-account key), else a local
// ~/.codex/auth.json copied into the temp CODEX_HOME, else t.Fatal.
func TestE2EFullFlowWithCodex(t *testing.T) {
	codexBin, err := pluginrepo.LookAgent(t, "codex")
	if err != nil {
		t.Fatal("codex CLI not found in PATH — install with: npm install -g @openai/codex")
	}

	pluginDir := pluginrepo.Root(t)

	// Hermetic HOME + state so install-codex.sh writes to a throwaway ~/.codex and
	// never touches the developer's real config.
	home := t.TempDir()
	state := t.TempDir()
	codexHome := filepath.Join(home, ".codex")
	require.NoError(t, os.MkdirAll(codexHome, 0o755))
	if !authenticateCodex(t, codexBin, codexHome) {
		t.Fatal("no Codex auth available — set OPENAI_API_KEY (CI: a service-account key) or run `codex login` (local)")
	}

	cap, srv := otlpcapture.New(t)
	defer srv.Close()

	// Install exactly as a customer would. That drives the whole path in one test,
	// from installer through config.toml and live Codex to OTLP, so a break in the
	// config merge, the trust hash or the hook contract fails here.
	// Named once, so the install and the assertion cannot drift apart.
	const authToken = "e2e-codex-token"
	installCodex(t, pluginDir, home, state, srv.URL, authToken)

	// Work in a throwaway git repo so the agent has somewhere to write.
	workDir := t.TempDir()
	gitrepo.Init(t, workDir)

	ctx, cancel := context.WithTimeout(context.Background(), sessionTimeout)
	defer cancel()

	// No --dangerously-bypass-hook-trust: the installer pre-trusted the hooks, exactly
	// as a real install leaves them.
	//
	// One interactive session, two typed prompts. `codex` with no subcommand opens the
	// TUI, so both turns share one session by construction. No skill turn, because
	// Codex has no skill concept.
	cmd := exec.Command(codexBin,
		"-s", "workspace-write",
		"-c", "approval_policy=\"never\"",
		"-C", workDir,
	)
	// The hook inherits this env: the home locates the creds config, XDG_STATE_HOME
	// the installed binary, CODEX_HOME the config.toml. CleanHome drops anything in
	// the developer's shell that would redirect this live turn's telemetry; without it
	// an exported CODEX_PLUGIN_OPTION_OTLP_URL beats the installed config.
	cmd.Env = testenv.CleanHome(home, "XDG_STATE_HOME="+state, "CODEX_HOME="+codexHome)

	session := agentterm.Start(t, ctx, cmd)
	defer session.Close()

	// Codex asks about the working directory before it runs anything, and the dialog
	// appears AFTER the banner. A prompt typed before it is answered lands in the
	// composer and stays there: the text echoes and nothing submits.
	session.SelectOption("Do you trust the contents of this directory", "Yes, continue", onboardingTimeout)

	// Windows only, and it blocks startup: Codex has no sandbox of its own there, so
	// it asks which one to install before it will accept a prompt. The entry it opens
	// on needs Administrator, which no test can promise, and "Quit" is two moves
	// away — so the non-admin sandbox is chosen by name, like every other dialog here.
	if runtime.GOOS == "windows" {
		session.SelectOption("Set up the Codex agent sandbox",
			"Use non-admin sandbox (higher risk if prompt injected)", onboardingTimeout)

		// Answering the dialog only starts the install. Codex disables its composer
		// for the duration ("Input disabled until setup completes") and a prompt typed
		// into it is dropped with no echo, so the turn never runs and the barrier below
		// blames the plugin for exporting nothing.
		//
		// This has to be its own barrier, because the status line waited for next is
		// drawn DURING the install: the observed order is "Setting up sandbox",
		// "Input disabled until setup completes", "default ·", "Sandbox ready",
		// "default ·". "Sandbox ready" is the first marker that means input is live.
		session.Expect("Sandbox ready", sandboxTimeout)
	}

	// Wait for the session to come up. Until trust is granted the status line under
	// the composer does not exist; once it does it reads "<model> default · <cwd>",
	// which is the only string here that cannot appear before the dialog is answered.
	//
	// Re-probe if this ever times out. The wording belongs to the Codex TUI, and
	// "default" is its approval-mode label rather than something this test sets.
	session.Expect("default ·", onboardingTimeout)

	// Codex offers a cheaper model when the account nears its rate limit, mid-turn.
	// Checked on every poll of both barriers rather than between them, because the
	// barrier is what hangs. It fails rather than answers; see failOnRateLimitDialog.
	dismiss := failOnRateLimitDialog(t, session)
	approve := approveCodexActions(t, session)

	session.Send("Create a file hello.txt containing exactly the text 'hi from codex', then run the shell command 'cat hello.txt'. Keep it brief.")
	cap.WaitForChatSpans(t, 1, turnTimeout, dismiss, approve)

	session.Send("Read hello.txt with the shell and tell me its contents. Keep it brief.")
	cap.WaitForChatSpans(t, 2, turnTimeout, dismiss, approve)

	// Both turns have closed. This only covers a tool span still in flight behind the
	// chat span that released the barrier.
	time.Sleep(500 * time.Millisecond)

	spans := cap.Spans(t)
	require.NotEmpty(t, spans,
		"no spans from a live Codex session with pre-trusted hooks (no bypass flag). "+
			"If trust_test.go still passes, Codex likely changed hook payloads/events; if it "+
			"fails too, the reproduced trusted_hash no longer matches — see internal/source/codex/trust.go")
	otlpcapture.LogSpanTree(t, spans)
	cap.AssertAuthToken(t, authToken)

	var (
		harnessCodex bool
		toolSpan     bool
		chatSpan     bool
		chatHasUsage bool
	)
	for _, s := range spans {
		for _, a := range s.Attributes {
			if a.Key == "gen_ai.harness.name" && a.Value.StringValue != nil && *a.Value.StringValue == "codex" {
				harnessCodex = true
			}
		}
		switch {
		case strings.HasPrefix(s.Name, "execute_tool"):
			toolSpan = true
		case strings.HasPrefix(s.Name, "chat"):
			chatSpan = true
			if otlpcapture.SpanHasPositiveTokenUsage(s) {
				chatHasUsage = true
			}
		}
	}

	assert.True(t, harnessCodex, "expected a span tagged gen_ai.harness.name=codex")
	assert.True(t, toolSpan, "expected at least one execute_tool span (the agent should run a tool)")
	assert.True(t, chatSpan, "expected a chat span (the turn should close with Stop)")
	assert.GreaterOrEqual(t, len(otlpcapture.ChatSpans(spans)), 2,
		"two turns ran, so two turns must close, one chat span each; got %s", otlpcapture.DescribeTurns(spans))
	assert.Empty(t, otlpcapture.SkillNames(spans),
		"Codex has no skill concept, so a skill attribute here means the extractor is firing on something else; got %s",
		otlpcapture.DescribeTurns(spans))
	// Token usage comes from the session's rollout file (internal/source/codex/rollout.go).
	// Doubles as a compression canary: a Codex that writes .jsonl.zst rollouts produces
	// no usage, and this goes red.
	assert.True(t, chatHasUsage, "expected the chat span to carry gen_ai.usage.*_tokens > 0 "+
		"(no usage may mean Codex now writes compressed .jsonl.zst rollouts — see rollout.go)")
	otlpcapture.AssertRepoIdentity(t, spans)

	t.Logf("live Codex e2e: %d spans, harness=codex=%v tool=%v chat=%v chatUsage=%v",
		len(spans), harnessCodex, toolSpan, chatSpan, chatHasUsage)
}

// installCodex runs the real installer against a hermetic home and XDG_STATE_HOME,
// so it appends hooks and reproduced trust to ~/.codex/config.toml and writes the
// creds config. Only the version-pinned binary is pre-staged, to skip the release
// download; the merge, the trust emission and the creds file are the real thing.
//
// Which installer depends on the platform. install-codex.ps1 is a separate
// implementation of the same merge, and its hook runs codex-on-event.ps1, so the
// shell installer under Git Bash would exercise neither.
func installCodex(t *testing.T, pluginDir, home, state, otlpURL, token string) {
	t.Helper()
	ver := pluginrepo.BootstrapVersion(t, pluginDir, "codex/codex-on-event.sh")

	codexState := filepath.Join(state, "dash0-agent-plugin", "codex")
	require.NoError(t, os.MkdirAll(filepath.Join(codexState, "bin"), 0o755))
	binPath := filepath.Join(codexState, "bin", pluginrepo.CachedBinary(t, "codex-on-event", ver))
	pluginrepo.CopyExecutable(t, pluginrepo.BuildBinary(t, pluginDir, "./cmd/codex-on-event"), binPath)

	// The bootstrap's path carries no version, so the installer always writes it.
	// DASH0_SOURCE_DIR makes that write come from this checkout; without it the
	// installer fetches the last release and the canary runs shipped code.
	script := "install-codex.sh"
	argv := []string{filepath.Join(pluginDir, script)}
	interpreter := "bash"
	if runtime.GOOS == "windows" {
		script = "install-codex.ps1"
		interpreter = "powershell"
		argv = []string{"-NoProfile", "-ExecutionPolicy", "Bypass",
			"-File", filepath.Join(pluginDir, script)}
	}

	cmd := exec.Command(interpreter, argv...)
	cmd.Env = testenv.CleanHome(home,
		"XDG_STATE_HOME="+state,
		"DASH0_SOURCE_DIR="+pluginDir,
		"DASH0_VERSION="+ver, "DASH0_OTLP_URL="+otlpURL,
		"DASH0_AUTH_TOKEN="+token, "DASH0_DATASET=default",
		// Supplied so the installer never reaches its interactive prompt.
		"DASH0_TEAM_NAME=e2e",
	)
	out, err := cmd.CombinedOutput()
	t.Logf("%s output:\n%s", script, string(out))
	require.NoError(t, err, "%s failed", script)
}

// authenticateCodex sets up auth inside a hermetic CODEX_HOME. Returns false
// when no auth source is available (the caller then fails).
func authenticateCodex(t *testing.T, codexBin, codexHome string) bool {
	t.Helper()
	if key := os.Getenv("OPENAI_API_KEY"); key != "" {
		cmd := exec.Command(codexBin, "login", "--with-api-key")
		cmd.Env = testenv.Clean("CODEX_HOME=" + codexHome)
		cmd.Stdin = stringReader(key)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Logf("codex login --with-api-key failed: %v\n%s", err, string(out))
			return false
		}
		return true
	}
	// Dev fallback: reuse an existing local login.
	home, err := os.UserHomeDir()
	if err != nil {
		return false
	}
	src := filepath.Join(home, ".codex", "auth.json")
	data, err := os.ReadFile(src)
	if err != nil {
		return false
	}
	return os.WriteFile(filepath.Join(codexHome, "auth.json"), data, 0o600) == nil
}

// failOnRateLimitDialog returns a poll handler that fails the test if Codex
// raises its "approaching rate limits" model-switch offer.
//
// It fails rather than answers. Both answers are bad: accepting switches the model
// mid-turn, and a turn that switches never closes (measured: three tool spans, zero
// chat spans), while "keep the current model" fared no better once the account was
// deep enough into its limit. A canary nursed through an exhausted quota measures
// the billing period, not the plugin, and a named failure in seconds beats a
// three-minute timeout whose message blames the plugin.
//
// CI never hits this: a budget-capped service-account key is never offered a switch.
//
// Scoped to a mark, because the transcript is cumulative and an unscoped check would
// match a dialog from an earlier turn.
func failOnRateLimitDialog(t *testing.T, session *agentterm.Session) func() {
	t.Helper()

	mark := session.Mark()
	return func() {
		if !session.SeenSince(mark, "Approaching rate limits") {
			return
		}
		t.Fatalf("Codex raised its rate-limit model-switch dialog mid-turn, so this turn will " +
			"never close and the barrier would time out blaming the plugin.\n" +
			"This account is at or near its Codex limit; run `codex` and check /status. " +
			"Nothing here is wrong with the plugin; re-run when the quota resets, or point " +
			"OPENAI_API_KEY at a service-account key, which is what CI uses and which is " +
			"never offered a model switch.")
	}
}

// codexApprovalEntry is the entry Codex preselects on every approval dialog it
// raises, and the one this canary confirms.
//
// The dialogs differ above the list — "Apply proposed file edits" names a
// destination, "Would you like to run the following command?" names a command —
// but the three entries below it are the same, and the plain yes is the first. The
// other two are no use: "don't ask again" quotes the command back inside its own
// label, and the third refuses.
const codexApprovalEntry = "Yes, proceed (y)"

// approveCodexActions returns a poll handler that answers Codex's approval
// dialogs, so a turn that writes a file and runs a command can finish.
//
// Windows only in practice. There Codex runs under the sandbox it installs at
// startup and asks before applying an edit AND before running a command, where the
// Linux sandbox does both and never asks; measured on one runner, same flags and
// the same prompts on both. `-s workspace-write` with approval_policy=never covers
// neither. Left unanswered the turn stops with the work already computed, and the
// barrier times out blaming the plugin for exporting nothing.
//
// Answered from a poll handler rather than between the turns, because the dialogs
// are raised in the MIDDLE of one and the barrier is what waits.
//
// Keyed on the entry rather than on either dialog's own text. Anchoring per dialog
// does not work here: highlighted() reads the newest marker AFTER its anchor line,
// so once the edit dialog has been answered its anchor still resolves — onto the
// command dialog's marker — and the canary would answer one dialog with the other's
// entry. The entry is still READ before Enter is pressed; what a TUI preselects is
// not a contract, and the canary that learned that quit its session by trusting one.
func approveCodexActions(t *testing.T, session *agentterm.Session) func() {
	t.Helper()

	return func() {
		if !session.SelectionIs(codexApprovalEntry) {
			return
		}
		session.Send("") // Enter, on the entry just read

		// Let the answered dialog close before returning, so the next poll cannot
		// confirm the same frame twice — a second Enter lands in the composer. Bounded
		// and not fatal: when Codex raises two dialogs back to back the selection never
		// leaves this entry, and answering the second is the next poll's job.
		deadline := time.Now().Add(3 * time.Second)
		for time.Now().Before(deadline) && session.SelectionIs(codexApprovalEntry) {
			time.Sleep(100 * time.Millisecond)
		}
	}
}

// stringReader turns a string into an *os.File, for feeding a hook payload to a
// child process on stdin.
func stringReader(s string) *os.File {
	r, w, _ := os.Pipe()
	go func() {
		_, _ = w.Write([]byte(s))
		_ = w.Close()
	}()
	return r
}
