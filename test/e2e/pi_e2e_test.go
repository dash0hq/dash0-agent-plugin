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

	"github.com/dash0hq/dash0-agent-plugin/internal/config"
)

// make test-pi needs Bun and runs on Linux only. This runs the helper as the
// extension does, a real process with one envelope on stdin, on every CI OS,
// with its configuration in ~/.pi.
const piEnvelope = `{"session_id":"019a-e2e","start":"2026-11-13T12:00:00Z","end":"2026-11-13T12:00:10Z","status":"done","prompt":"the ask","response":"the answer",` +
	`"calls":[{"provider":"openai-codex","model":"gpt-5.5","start":"2026-11-13T12:00:01Z","end":"2026-11-13T12:00:02Z","stop_reason":"toolUse","usage":{"input":40,"output":10,"cache_read":1,"cache_write":0}},` +
	`{"provider":"claude-code","model":"claude-sonnet-5","start":"2026-11-13T12:00:05Z","end":"2026-11-13T12:00:09Z","stop_reason":"stop","usage":{"input":2,"output":19,"cache_read":60,"cache_write":9}}],` +
	`"tools":[{"id":"call_1","name":"bash","start":"2026-11-13T12:00:02Z","end":"2026-11-13T12:00:04Z","error":false,"call":0,"input":"{\"command\":\"ls\"}","output":"listing"}]}`

func TestE2EPiHelperDeliversASettledTurn(t *testing.T) {
	capture, server := newOTLPCapture(t)
	defer server.Close()

	name := "pi-on-event"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	bin := filepath.Join(t.TempDir(), name)
	build := exec.Command("go", "build", "-o", bin, "./cmd/pi-on-event")
	build.Dir = repoRootDir(t)
	out, err := build.CombinedOutput()
	require.NoError(t, err, "building pi-on-event: %s", out)

	home := t.TempDir()
	configDir := filepath.Join(home, ".pi")
	require.NoError(t, os.MkdirAll(configDir, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(configDir, config.Name), []byte("---\r\n"+
		"otlp_url: "+server.URL+"\r\nauth_token: e2e-token\r\nomit_io: false\r\nomit_identity_fallback: true\r\n---\r\n"), 0o600))
	cmd := exec.Command(bin)
	cmd.Dir = t.TempDir()
	cmd.Stdin = strings.NewReader(piEnvelope)
	cmd.Env = append(os.Environ(), "HOME="+home, "USERPROFILE="+home, "PI_CODING_AGENT_DIR=",
		"PI_PLUGIN_OPTION_OTLP_URL=", "PI_PLUGIN_OPTION_AUTH_TOKEN=", "DASH0_OTLP_URL=")
	out, err = cmd.CombinedOutput()
	require.NoError(t, err, "helper failed: %s", out)

	bodies, auths := capture.snapshot()
	require.Len(t, bodies, 1, "one turn is one export")
	assert.Equal(t, []string{"Bearer e2e-token"}, auths)
	spans := collectSpans(t, bodies)
	logSpanTree(t, spans)
	require.Len(t, spans, 3)
	root := spans[0]
	require.Equal(t, "chat claude-sonnet-5", root.Name)
	assert.Empty(t, root.ParentSpanID)
	for _, child := range spans[1:] {
		assert.Equal(t, root.SpanID, child.ParentSpanID)
	}
	attrs := attributesOfSpan(root)
	assert.Equal(t, "pi", attrs["gen_ai.harness.name"])
	assert.Equal(t, "anthropic", attrs["gen_ai.provider.name"])
	assert.Equal(t, "71", attrs["gen_ai.usage.input_tokens"])
	assert.Contains(t, attrs["gen_ai.input.messages"], "the ask")
	assert.Equal(t, "openai", attributesOfSpan(spans[1])["gen_ai.provider.name"])
	assert.Equal(t, "listing", attributesOfSpan(spans[2])["gen_ai.tool.call.result"])
}
