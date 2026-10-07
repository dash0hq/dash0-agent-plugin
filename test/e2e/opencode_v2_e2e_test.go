// SPDX-FileCopyrightText: Copyright 2026 Dash0 Inc.
// SPDX-License-Identifier: Apache-2.0

//go:build e2e

package e2e

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/dash0hq/dash0-agent-plugin/internal/otlp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The actual bootstrap and binary must export before stdin closes. A one-shot
// wrapper that buffers until EOF works for hooks but silently breaks V2.
func TestE2EOpenCodeV2StreamingAndReload(t *testing.T) {
	capture, server := newOTLPCapture(t)
	defer server.Close()
	start := openCodeV2Consumer(t, server.URL, "OPENCODE_V2_PLUGIN_OPTION_OMIT_IO=false")
	write := func(stdin io.Writer, seq int, kind, fields string) {
		if fields != "" {
			fields = "," + fields
		}
		_, err := fmt.Fprintf(stdin, "{\"type\":%q,\"created\":%d,\"durable\":{\"seq\":%d},\"data\":{\"sessionID\":\"ses_v2_fixture\"%s}}\n", kind, 1000+seq*100, seq, fields)
		require.NoError(t, err)
	}
	cmd, stdin, stderr := start()
	_, err := io.WriteString(stdin, "malformed event\n")
	require.NoError(t, err)
	write(stdin, 0, "session.created", "")
	write(stdin, 1, "session.execution.started", "")
	write(stdin, 2, "session.step.started", `"assistantMessageID":"m1","model":{"id":"fledge-alpha-free","providerID":"opencode"}`)
	write(stdin, 3, "session.tool.input.started", `"assistantMessageID":"m1","id":"call0","name":"shell"`)
	write(stdin, 4, "session.tool.called", `"assistantMessageID":"m1","id":"call0","input":{"command":"printf café"}`)
	write(stdin, 5, "session.tool.success", `"assistantMessageID":"m1","id":"call0","content":[{"type":"text","text":"café"}]`)
	require.Eventually(t, func() bool { bodies, _ := capture.snapshot(); return len(bodies) == 1 }, 10*time.Second, 25*time.Millisecond)
	require.NoError(t, stdin.Close())
	require.NoError(t, cmd.Wait(), "%s", stderr)
	assert.Contains(t, stderr.String(), "invalid V2 event")
	cmd, stdin, stderr = start() // A plugin reload restarts only the consumer.
	write(stdin, 6, "session.step.ended", `"tokens":{"input":7,"output":11,"reasoning":3,"cache":{"read":5,"write":2}}`)
	write(stdin, 7, "session.text.ended", `"assistantMessageID":"m1","text":"réponse"`)
	write(stdin, 8, "session.execution.succeeded", "")
	require.NoError(t, stdin.Close())
	require.NoError(t, cmd.Wait(), "%s", stderr)
	bodies, auth := capture.snapshot()
	spans := collectSpans(t, bodies)
	require.Len(t, spans, 2)
	assert.Equal(t, []string{"Bearer e2e-token", "Bearer e2e-token"}, auth)
	tool, chat := spans[0], spans[1]
	assert.Equal(t, "execute_tool shell", tool.Name)
	assert.Equal(t, chat.TraceID, tool.TraceID)
	assert.Equal(t, chat.SpanID, tool.ParentSpanID)
	assert.Equal(t, "1400000000", tool.StartTimeUnixNano)
	assert.Equal(t, "1500000000", tool.EndTimeUnixNano)
	assertSpanAttr(t, tool, "gen_ai.tool.call.result", "café")
	assert.Equal(t, int64(14), spanIntAttr(t, chat, "gen_ai.usage.input_tokens"))
	assert.Equal(t, int64(14), spanIntAttr(t, chat, "gen_ai.usage.output_tokens"))
	assert.True(t, strings.Contains(spanAttrString(chat, "gen_ai.output.messages"), "réponse"))
	assertSpanAttr(t, chat, "gen_ai.harness.name", "opencode-v2")
}

// The default privacy mode must still emit redacted messages: the conversation
// view separates turns on them, and without them every tool merges into one.
func TestE2EOpenCodeV2DefaultPrivacySeparatesTurns(t *testing.T) {
	capture, server := newOTLPCapture(t)
	defer server.Close()
	cmd, stdin, stderr := openCodeV2Consumer(t, server.URL)()
	seq := 0
	write := func(kind, fields string, prompt ...string) {
		if fields != "" {
			fields = "," + fields
		}
		extra := ""
		if len(prompt) > 0 {
			extra = fmt.Sprintf(`,"prompt":{"text":%q,"role":"user"}`, prompt[0])
		}
		_, err := fmt.Fprintf(stdin, "{\"type\":%q,\"created\":%d,\"durable\":{\"seq\":%d},\"data\":{\"sessionID\":\"ses_v2_private\"%s}%s}\n", kind, 1000+seq*100, seq, fields, extra)
		require.NoError(t, err)
		seq++
	}
	write("session.created", "")
	for _, turn := range []string{"m1", "m2"} {
		write("session.execution.started", "")
		write("session.inbox.delivered", "", "PRIVATE_PROMPT_"+turn)
		write("session.step.started", `"assistantMessageID":"`+turn+`","model":{"id":"gpt-6-luna","providerID":"openai"}`)
		write("session.tool.input.started", `"assistantMessageID":"`+turn+`","id":"call0","name":"read"`)
		write("session.tool.called", `"assistantMessageID":"`+turn+`","id":"call0","input":{"path":"PRIVATE_PATH"}`)
		write("session.tool.success", `"assistantMessageID":"`+turn+`","id":"call0","content":[{"type":"text","text":"PRIVATE_RESULT"}]`)
		write("session.text.ended", `"assistantMessageID":"`+turn+`","text":"PRIVATE_RESPONSE"`)
		write("session.execution.succeeded", "")
	}
	require.NoError(t, stdin.Close())
	require.NoError(t, cmd.Wait(), "%s", stderr)
	bodies, _ := capture.snapshot()
	for _, body := range bodies {
		assert.NotContains(t, string(body), "PRIVATE_")
	}
	spans := collectSpans(t, bodies)
	require.Len(t, spans, 4)
	var chats []otlp.Span
	for _, s := range spans {
		if strings.HasPrefix(s.Name, "chat ") {
			chats = append(chats, s)
		}
	}
	require.Len(t, chats, 2)
	assert.NotEqual(t, chats[0].TraceID, chats[1].TraceID)
	for _, chat := range chats {
		assert.JSONEq(t, `[{"parts":[{"content":"<REDACTED>","type":"text"}],"role":"user"}]`, spanAttrString(chat, "gen_ai.input.messages"))
		assert.JSONEq(t, `[{"parts":[{"content":"<REDACTED>","type":"text"}],"role":"assistant"}]`, spanAttrString(chat, "gen_ai.output.messages"))
	}
	for _, s := range spans {
		if s.Name == "execute_tool read" {
			assert.Contains(t, []string{chats[0].SpanID, chats[1].SpanID}, s.ParentSpanID)
		}
	}
}

// openCodeV2Consumer builds the exporter into the bootstrap's cache and returns a
// function that starts one consumer the way the plugin does: via the real
// bootstrap, with options passed as OPENCODE_V2_PLUGIN_OPTION_* variables. Every
// consumer shares one state directory, so a second start acts as a reload.
func openCodeV2Consumer(t *testing.T, otlpURL string, extraEnv ...string) func() (*exec.Cmd, io.WriteCloser, *bytes.Buffer) {
	root := findPluginDir(t)
	dataDir := t.TempDir()
	cache := filepath.Join(dataDir, "bin")
	require.NoError(t, os.MkdirAll(cache, 0o700))
	version := bootstrapVersion(t, root)
	name := fmt.Sprintf("opencode-v2-on-event-%s-%s-%s", version, runtime.GOOS, runtime.GOARCH)
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	build := exec.Command("go", "build", "-o", filepath.Join(cache, name), "./cmd/opencode-v2-on-event")
	build.Dir = root
	out, err := build.CombinedOutput()
	require.NoError(t, err, "%s", out)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	return func() (*exec.Cmd, io.WriteCloser, *bytes.Buffer) {
		cwd := t.TempDir()
		env := append(hermeticEnv(t), "OPENCODE_V2_PLUGIN_DATA="+dataDir,
			"OPENCODE_V2_PLUGIN_INSTANCE=stream-test", "OPENCODE_V2_PLUGIN_OPTION_OTLP_URL="+otlpURL,
			"OPENCODE_V2_PLUGIN_OPTION_AUTH_TOKEN=e2e-token")
		env = append(env, extraEnv...)
		var cmd *exec.Cmd
		if runtime.GOOS == "windows" {
			resolve := exec.CommandContext(ctx, "powershell.exe", "-NoProfile", "-ExecutionPolicy", "Bypass", "-File", filepath.Join(root, "opencode-v2", "opencode-v2-on-event.ps1"), "--resolve-binary")
			resolve.Dir, resolve.Env = cwd, env
			path, err := resolve.Output()
			require.NoError(t, err)
			require.Equal(t, filepath.Join(cache, name), filepath.Clean(strings.TrimSpace(string(path))))
			cmd = exec.CommandContext(ctx, strings.TrimSpace(string(path)))
		} else {
			cmd = exec.CommandContext(ctx, "bash", filepath.Join(root, "opencode-v2", "opencode-v2-on-event.sh"))
		}
		cmd.Dir, cmd.Env = cwd, env
		stdin, err := cmd.StdinPipe()
		require.NoError(t, err)
		t.Cleanup(func() { _ = stdin.Close() })
		stderr := &bytes.Buffer{}
		cmd.Stderr = stderr
		require.NoError(t, cmd.Start())
		return cmd, stdin, stderr
	}
}

// A shutting-down OpenCode server signals its process group before the plugin
// has delivered the turn's last event; `opencode run --standalone` does it on
// every turn. The consumer must keep reading until stdin closes.
func TestE2EOpenCodeV2SurvivesShutdownSignal(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("process-group signals are POSIX")
	}
	capture, server := newOTLPCapture(t)
	defer server.Close()
	cmd, stdin, stderr := openCodeV2Consumer(t, server.URL)()
	write := func(seq int, kind, fields string) {
		if fields != "" {
			fields = "," + fields
		}
		_, err := fmt.Fprintf(stdin, "{\"type\":%q,\"created\":%d,\"durable\":{\"seq\":%d},\"data\":{\"sessionID\":\"ses_v2_signal\"%s}}\n", kind, 1000+seq*100, seq, fields)
		require.NoError(t, err)
	}
	write(0, "session.created", "")
	write(1, "session.execution.started", "")
	write(2, "session.step.started", `"assistantMessageID":"m1","model":{"id":"muse","providerID":"opencode"}`)
	write(3, "session.step.ended", `"tokens":{"input":3,"output":4,"reasoning":0,"cache":{"read":0,"write":0}}`)
	// The bootstrap execs the binary; signal only once it has, or this kills bash.
	require.Eventually(t, func() bool {
		out, _ := exec.Command("ps", "-o", "comm=", "-p", fmt.Sprint(cmd.Process.Pid)).Output()
		return strings.Contains(string(out), "opencode-v2-on-") // Linux truncates comm to 15 chars
	}, 10*time.Second, 25*time.Millisecond)
	time.Sleep(300 * time.Millisecond) // past exec, into main()
	require.NoError(t, cmd.Process.Signal(syscall.SIGTERM))
	time.Sleep(200 * time.Millisecond)
	write(4, "session.execution.succeeded", "")
	require.NoError(t, stdin.Close())
	require.NoError(t, cmd.Wait(), "%s", stderr)
	bodies, _ := capture.snapshot()
	spans := collectSpans(t, bodies)
	require.Len(t, spans, 1)
	assert.Equal(t, "chat muse", spans[0].Name)
	assert.Equal(t, int64(3), spanIntAttr(t, spans[0], "gen_ai.usage.input_tokens"))
}
