// SPDX-FileCopyrightText: Copyright 2026 Dash0 Inc.
// SPDX-License-Identifier: Apache-2.0

//go:build e2e

package e2e

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/dash0hq/dash0-agent-plugin/internal/config"
	"github.com/dash0hq/dash0-agent-plugin/internal/otlp"
)

// Amp is the one runtime with no shell hook and no bootstrap: Amp loads
// amp/index.ts in-process and the bridge spawns the helper itself. `make
// test-amp` covers that bridge, but it needs Bun and only ever runs on Linux,
// so until this file existed nothing in the Amp path was exercised on Windows
// — the OS where an extensionless binary cannot be executed at all and where
// the bridge resolves ./amp-on-event.exe instead.

const ampEnvelope = `{"thread_id":"T-e2e","id":"M-prompt","start":"2026-09-14T12:00:00Z","end":"2026-09-14T12:00:10Z","status":"done","executor":"local","assistant_ids":["M-a","M-b"],"prompt":"the ask","response":"the answer","tools":[{"id":"call","name":"shell_command","start":"2026-09-14T12:00:01Z","end":"2026-09-14T12:00:03Z","status":"done","input":"{\"command\":\"ls\"}","output":"listing"}]}`

const ampExport = `{"id":"T-e2e","messages":[` +
	`{"messageId":2,"protocolMessageID":"M-a","role":"assistant","state":{"type":"complete"},"usage":{"model":"gpt-6-astra","totalInputTokens":41,"outputTokens":13}},` +
	`{"messageId":4,"protocolMessageID":"M-b","role":"assistant","state":{"type":"complete"},"usage":{"model":"claude-sonnet-4-6","totalInputTokens":71,"outputTokens":19}}]}`

// fakeAmp writes an `amp` onto PATH that answers `amp threads export <id>` with
// a fixture. The helper shells out by bare name, so this is also what proves it
// resolves the command through PATH rather than a hardcoded path.
//
// Both scripts print a file rather than embedding the JSON. cmd.exe has no
// heredoc and quoting a JSON document through `echo` means escaping five
// metacharacters by hand; the first version of this did that, split the fixture
// to fit on `echo` lines, and silently emitted invalid JSON — so Windows
// exported a turn with no usage and the only sign was a missing model name.
func fakeAmp(t *testing.T, export string) string {
	t.Helper()
	dir := t.TempDir()
	fixture := filepath.Join(dir, "export.json")
	require.NoError(t, os.WriteFile(fixture, []byte(export), 0o600))
	name, body := "amp", "#!/bin/sh\nexec cat \""+fixture+"\"\n"
	if runtime.GOOS == "windows" {
		// .cmd, not .exe: exec.Command resolves PATHEXT, and a batch file is the
		// only script Windows runs without an interpreter on PATH.
		name, body = "amp.cmd", "@echo off\r\ntype \""+fixture+"\"\r\n"
	}
	require.NoError(t, os.WriteFile(filepath.Join(dir, name), []byte(body), 0o700))
	return dir
}

func buildAmpHelper(t *testing.T) string {
	t.Helper()
	name := "amp-on-event"
	if runtime.GOOS == "windows" {
		// Same reason as the other runtimes: go build honors -o verbatim and
		// Windows refuses to exec a file without the extension.
		name += ".exe"
	}
	bin := filepath.Join(t.TempDir(), name)
	build := exec.Command("go", "build", "-o", bin, "./cmd/amp-on-event")
	build.Dir = repoRootDir(t)
	out, err := build.CombinedOutput()
	require.NoError(t, err, "building amp-on-event: %s", out)
	return bin
}

func repoRootDir(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd() // test/e2e
	require.NoError(t, err)
	return filepath.Dir(filepath.Dir(dir))
}

// TestE2EAmpInstallerRerun preserves the public configuration contract on both
// platforms. Windows uses stock PowerShell 5.1, not pwsh, so a default ANSI
// read of the installer's BOM-less UTF-8 output cannot hide behind PS 7.
func TestE2EAmpInstallerRerun(t *testing.T) {
	home, source := t.TempDir(), t.TempDir()
	name := "amp-on-event"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	// The installer copies these files; it never executes the helper.
	require.NoError(t, os.WriteFile(filepath.Join(source, name), []byte("unused"), 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(source, "index.ts"), []byte("export {}"), 0o600))
	path := filepath.Join(home, ".amp", config.Name)
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o700))
	const body = "Notes stay outside config.\r\nexport_usage: true\r\nauth_token: body-only"
	require.NoError(t, os.WriteFile(path, []byte("---\r\n"+
		"otlp_url: https://example.invalid\r\nauth_token: rerun-fixture-token\r\n"+
		"dataset: Prüfung\r\nteam_name: Plattform-Übersicht\r\nexport_usage: false\r\n"+
		"enabled: false\r\nauth_token_keychain_service: fixture-service\r\n---\r\n"+body), 0o600))
	root := repoRootDir(t)
	for run := 0; run < 2; run++ {
		cmd := exec.Command("bash", filepath.Join(root, "install-amp.sh"))
		if runtime.GOOS == "windows" {
			// iex executes in the caller's session. Check no token-bearing
			// installer variables survive, not just the primary $Token.
			cmd = exec.Command("powershell", "-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass", "-Command", `
Invoke-Expression ([IO.File]::ReadAllText($env:AMP_INSTALLER, [Text.Encoding]::UTF8))
if (Get-Variable Token,lines,existing,parts,ExistingConfig,ExtraConfig -ErrorAction SilentlyContinue) {
  throw 'token-bearing installer variables survived iex'
}`)
		}
		cmd.Dir = t.TempDir()
		cmd.Stdin = strings.NewReader("")
		cmd.Env = append(os.Environ(), "HOME="+home, "USERPROFILE="+home,
			"DASH0_SOURCE_DIR="+source, "DASH0_OTLP_URL=", "DASH0_AUTH_TOKEN=", "DASH0_DATASET=", "DASH0_TEAM_NAME=",
			"AMP_INSTALLER="+filepath.Join(root, "install-amp.ps1"))
		detachTerminal(cmd)
		out, err := cmd.CombinedOutput()
		require.NoError(t, err, "installer run %d: %s", run, out)
		assert.NotContains(t, string(out), "rerun-fixture-token")
		loaded := config.Load(path)
		for key, value := range map[string]string{
			"otlp_url": "https://example.invalid", "auth_token": "rerun-fixture-token",
			"dataset": "Prüfung", "team_name": "Plattform-Übersicht", "export_usage": "false",
			"enabled": "false", "auth_token_keychain_service": "fixture-service",
		} {
			assert.Equal(t, value, loaded.Get(key), "run %d key %s", run, key)
		}
		content, err := os.ReadFile(path)
		require.NoError(t, err)
		parts := strings.SplitN(string(content), "---", 3)
		require.Len(t, parts, 3)
		assert.Equal(t, "\n"+body, parts[2], "Markdown body, including CRLF and missing final newline, is unchanged")
	}
}

// TestE2EAmpHelperDeliversACompletedTurn runs the helper exactly as the bridge
// does: a real process, one completed-turn envelope on stdin, nothing else.
func TestE2EAmpHelperDeliversACompletedTurn(t *testing.T) {
	capture, server := newOTLPCapture(t)
	defer server.Close()

	bin := buildAmpHelper(t)
	home := t.TempDir()
	cmd := exec.Command(bin)
	cmd.Dir = t.TempDir()
	cmd.Stdin = strings.NewReader(ampEnvelope)
	cmd.Env = append(os.Environ(),
		"PATH="+fakeAmp(t, ampExport)+string(os.PathListSeparator)+os.Getenv("PATH"),
		"HOME="+home,
		"USERPROFILE="+home,
		"AMP_PLUGIN_OPTION_OTLP_URL="+server.URL,
		"AMP_PLUGIN_OPTION_AUTH_TOKEN=e2e-token",
		"AMP_PLUGIN_OPTION_DATASET=e2e-dataset",
		"AMP_PLUGIN_OPTION_OMIT_IO=false",
		"AMP_PLUGIN_OPTION_OMIT_IDENTITY_FALLBACK=true",
		"AMP_PLUGIN_OPTION_EXPORT_USAGE=true",
	)
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "helper failed: %s", out)

	bodies, auths := capture.snapshot()
	require.Len(t, bodies, 1, "one turn is one export")
	assert.Equal(t, []string{"Bearer e2e-token"}, auths)
	spans := collectSpans(t, bodies)
	logSpanTree(t, spans)

	byName := map[string]otlp.Span{}
	for _, s := range spans {
		byName[s.Name] = s
	}
	// Checked before the span names, because every name below depends on the
	// usage read having worked: a fake `amp` that prints nothing usable still
	// produces a healthy-looking turn, just an unnamed one.
	for _, s := range spans {
		if s.ParentSpanID == "" {
			require.Equal(t, "matched", attributesOfSpan(s)["dash0.amp.usage.status"],
				"the fake amp on PATH did not return a usable export")
		}
	}
	// The answering model names the root; the earlier call becomes a child.
	require.Contains(t, byName, "chat claude-sonnet-4-6")
	require.Contains(t, byName, "chat gpt-6-astra")
	require.Contains(t, byName, "execute_tool shell_command")
	root := byName["chat claude-sonnet-4-6"]
	assert.Empty(t, root.ParentSpanID, "the turn root has no parent")
	for _, child := range []string{"chat gpt-6-astra", "execute_tool shell_command"} {
		assert.Equal(t, root.SpanID, byName[child].ParentSpanID, "%s hangs off the root", child)
	}
	attrs := attributesOfSpan(root)
	assert.Equal(t, "matched", attrs["dash0.amp.usage.status"])
	assert.Equal(t, "claude-sonnet-4-6", attrs["gen_ai.request.model"])
	assert.Equal(t, "amp", attrs["gen_ai.harness.name"])
	// Only set when content was shed, and this turn is tiny.
	assert.NotContains(t, attrs, "dash0.amp.truncated")
}

// TestE2EAmpLiveTurnReachesTheCollector is the live canary: a real `amp` turn
// with the real plugin installed, which is the only thing that proves Amp still
// calls the handlers this integration is built on. It costs credits, so it runs
// only where `amp` is authenticated — CI passes AMP_API_KEY.
func TestE2EAmpLiveTurnReachesTheCollector(t *testing.T) {
	if os.Getenv("AMP_API_KEY") == "" && os.Getenv("DASH0_AMP_LIVE") == "" {
		t.Skip("set AMP_API_KEY (CI) or DASH0_AMP_LIVE=1 (a logged-in machine) to run the live Amp canary")
	}
	if _, err := exec.LookPath("amp"); err != nil {
		t.Fatal("amp CLI not found — install it before running the live canary")
	}
	capture, server := newOTLPCapture(t)
	defer server.Close()

	// A project-scoped plugin: Amp loads .amp/plugins/* from the working
	// directory, so the canary needs no writable HOME and leaves nothing behind.
	workspace := t.TempDir()
	pluginDir := filepath.Join(workspace, ".amp", "plugins", "dash0")
	require.NoError(t, os.MkdirAll(pluginDir, 0o755))
	root := repoRootDir(t)
	index, err := os.ReadFile(filepath.Join(root, "amp", "index.ts"))
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(pluginDir, "index.ts"), index, 0o644))
	helper := filepath.Join(pluginDir, "amp-on-event")
	if runtime.GOOS == "windows" {
		helper += ".exe"
	}
	build := exec.Command("go", "build", "-o", helper, "./cmd/amp-on-event")
	build.Dir = root
	out, err := build.CombinedOutput()
	require.NoError(t, err, "building the helper into the plugin dir: %s", out)

	// --plugin-ready-timeout: Amp waits for plugins at turn start, and without
	// it a fast turn can finish before the bridge has registered its handlers.
	// The flag takes seconds as a bare number; "30s" is rejected.
	cmd := exec.Command("amp", "--plugin-ready-timeout", "30", "-x",
		"Reply with the single word: pong. Do not use any tools.")
	cmd.Dir = workspace
	cmd.Env = append(os.Environ(),
		"AMP_PLUGIN_OPTION_OTLP_URL="+server.URL,
		"AMP_PLUGIN_OPTION_AUTH_TOKEN=e2e-token",
		"AMP_PLUGIN_OPTION_DATASET=e2e-dataset",
		"AMP_PLUGIN_OPTION_OMIT_IDENTITY_FALLBACK=true",
	)
	out, err = cmd.CombinedOutput()
	require.NoError(t, err, "amp turn failed: %s", out)

	// The helper is detached and polls `amp threads export` for up to twenty
	// seconds, so spans legitimately arrive after `amp` has exited.
	require.Eventually(t, func() bool {
		bodies, _ := capture.snapshot()
		return len(bodies) > 0
	}, 40*time.Second, 250*time.Millisecond, "no spans from a live Amp turn")

	bodies, auths := capture.snapshot()
	assert.Equal(t, "Bearer e2e-token", auths[0])
	spans := collectSpans(t, bodies)
	logSpanTree(t, spans)
	var roots int
	for _, s := range spans {
		if s.ParentSpanID == "" {
			roots++
			attrs := attributesOfSpan(s)
			assert.Equal(t, "amp", attrs["gen_ai.harness.name"])
			assert.True(t, strings.HasPrefix(s.Name, "chat"), "root is a chat span, got %q", s.Name)
			assert.NotEmpty(t, attrs["gen_ai.conversation.id"], "the root carries its thread id")
			// Content defaults to redacted; the point here is that the field
			// exists at all, which is what a broken bridge would lose.
			assert.Contains(t, attrs, "gen_ai.input.messages")
		}
	}
	assert.Equal(t, 1, roots, "one turn is one root")
}

// attributesOfSpan flattens a span's attributes to their string form. Every
// value the Amp path emits is a string, an int, or a bool rendered as a string.
func attributesOfSpan(span otlp.Span) map[string]string {
	attrs := map[string]string{}
	for _, a := range span.Attributes {
		switch {
		case a.Value.StringValue != nil:
			attrs[a.Key] = *a.Value.StringValue
		case a.Value.IntValue != nil:
			// OTLP carries int64 as a JSON string on the wire.
			attrs[a.Key] = *a.Value.IntValue
		}
	}
	return attrs
}
