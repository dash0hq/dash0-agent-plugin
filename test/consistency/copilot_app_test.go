// SPDX-FileCopyrightText: Copyright 2026 Dash0 Inc.
// SPDX-License-Identifier: Apache-2.0

package consistency

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/dash0hq/dash0-agent-plugin/internal/source/copilotapp"
)

func readExtension(t *testing.T) string {
	t.Helper()
	body, err := os.ReadFile(filepath.Join(repoRoot(t), "copilot-app", "extension.mjs"))
	require.NoError(t, err)
	return string(body)
}

// The app loads extension.mjs as an ES module and drops it on a syntax error,
// with nothing in the session to say so.
func TestCopilotAppExtensionParses(t *testing.T) {
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("node is not installed")
	}
	out, err := exec.Command("node", "--check", filepath.Join(repoRoot(t), "copilot-app", "extension.mjs")).CombinedOutput()
	assert.NoError(t, err, "node --check: %s", out)
}

// Every event name the extension passes on argv has to be one the binary
// normalizes; an unknown one is silently dropped.
func TestCopilotAppExtensionSendsOnlyKnownEvents(t *testing.T) {
	sends := regexp.MustCompile(`send\("([A-Za-z]+)"`).FindAllStringSubmatch(readExtension(t), -1)
	require.NotEmpty(t, sends)

	seen := map[string]bool{}
	for _, m := range sends {
		seen[m[1]] = true
		assert.NotNil(t, copilotapp.Normalize(m[1], map[string]any{"sessionId": "s"}),
			"extension.mjs sends %q, which copilotapp.Normalize does not know", m[1])
	}
	for _, want := range []string{"sessionStart", "userPromptSubmitted", "turnEnd", "sessionEnd"} {
		assert.True(t, seen[want], "extension.mjs never sends %q", want)
	}
}

// The extension forwards only the data keys it lists, so a key the adapter
// reads but the extension drops is a field that is always empty in production
// and present in every fixture.
func TestCopilotAppExtensionForwardsWhatTheAdapterReads(t *testing.T) {
	ext := readExtension(t)
	for _, key := range []string{
		"inputTokens", "outputTokens", "cacheReadTokens", "cacheWriteTokens", "reasoningTokens",
		"model", "isAuto", "content", "parentToolCallId", "toolCallId", "toolName", "arguments",
		"mcpServerName", "mcpToolName", "success", "result", "error", "agentName",
		"errorType", "message", "reason",
	} {
		assert.Contains(t, ext, `"`+key+`"`, "extension.mjs does not forward data.%s", key)
	}
	for _, typ := range []string{
		"assistant.usage", "assistant.message", "tool.execution_start", "tool.execution_complete",
		"subagent.started", "subagent.completed", "subagent.failed", "session.error",
	} {
		assert.Contains(t, ext, `"`+typ+`"`, "extension.mjs does not buffer %s", typ)
	}
}

// The extension runs the bootstrap and loads the skills by paths relative to
// itself; a rename on either side leaves an install that does nothing.
func TestCopilotAppExtensionPathsExist(t *testing.T) {
	root := repoRoot(t)
	ext := readExtension(t)

	for _, name := range []string{"copilot-app-on-event.sh", "copilot-app-on-event.ps1"} {
		assert.Contains(t, ext, `"`+name+`"`)
		_, err := os.Stat(filepath.Join(root, "copilot-app", name))
		assert.NoError(t, err)
	}
	assert.Contains(t, ext, `join(HERE, "skills")`)
	_, err := os.Stat(filepath.Join(root, "copilot-app", "skills", "dash0-configure", "SKILL.md"))
	assert.NoError(t, err)

	script := filepath.Join(root, "copilot-app", "copilot-app-on-event.sh")
	info, err := os.Stat(script)
	require.NoError(t, err)
	if runtime.GOOS != "windows" {
		assert.NotZero(t, info.Mode()&0o111, "bootstrap must be executable")
	}
	body, err := os.ReadFile(script)
	require.NoError(t, err)
	assert.Contains(t, string(body), `exec "$BINARY" "$@"`, "bootstrap must forward args to the binary")
}

// stdout is the SDK's JSON-RPC channel: one stray write corrupts the session's
// connection to the extension. Several console methods write to it, so the
// extension uses none. The behavioural tests also assert an empty stdout.
func TestCopilotAppExtensionNeverWritesStdout(t *testing.T) {
	ext := readExtension(t)
	for _, banned := range []string{"console.", "process.stdout"} {
		assert.NotContains(t, ext, banned)
	}
}
