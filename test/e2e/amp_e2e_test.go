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

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

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
