// SPDX-FileCopyrightText: Copyright 2026 Dash0 Inc.
// SPDX-License-Identifier: Apache-2.0

package consistency

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The SDK stub hands the extension a session whose history read stays pending
// until the driver releases it, so live events can land in that window.
const extensionSDKStub = `export async function joinSession(config) { globalThis.__config = config; return globalThis.__session; }`

const extensionDriver = `
import { appendFileSync, readFileSync } from "node:fs";
let release, handler;
const pending = new Promise((r) => (release = r));
globalThis.__session = {
  sessionId: "s1",
  getEvents: () => pending,
  on: (h) => (handler = h),
  log: async () => {},
};
const loaded = import("./extension.mjs");
while (!handler) await new Promise((r) => setTimeout(r, 1));

const now = new Date().toISOString();
handler({ id: "h3", type: "tool.execution_complete", timestamp: now, data: { toolCallId: "t1", success: true } });
for (const type of process.argv[2].split(",")) handler({ id: "l-" + type, type, timestamp: now, data: {} });
release([
  { id: "h1", type: "user.message", timestamp: now, data: { content: "hi" } },
  { id: "h2", type: "tool.execution_start", timestamp: now, data: { toolCallId: "t1", toolName: "view" } },
  { id: "h3", type: "tool.execution_complete", timestamp: now, data: { toolCallId: "t1", success: true } },
]);
await loaded;

// End the session through the hook, as the app does on exit, and mark when the
// hook's promise settles relative to the sends it should have waited for.
if (process.argv[4]) {
  await globalThis.__config.hooks.onSessionEnd({ sessionId: "s1", reason: process.argv[4] });
  appendFileSync(process.env.LOG, "hookReturned\n");
}

// Sends are chained child processes; wait for the expected number to land.
const want = Number(process.argv[3]);
for (let i = 0; i < 500; i++) {
  let lines = [];
  try { lines = readFileSync(process.env.LOG, "utf8").trim().split("\n"); } catch {}
  if (lines.length >= want) break;
  await new Promise((r) => setTimeout(r, 10));
}
await new Promise((r) => setTimeout(r, 100));
`

// runExtension loads the real extension.mjs against the stub, delivers the
// given live events while the history read is pending, optionally ends the
// session through the sessionEnd hook with exitReason, and returns one
// "<event> <payload>" line per bootstrap call.
func runExtension(t *testing.T, live string, want int, exitReason string) []string {
	t.Helper()
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("node is not installed")
	}
	if runtime.GOOS == "windows" {
		t.Skip("the fake bootstrap is a shell script")
	}
	dir := t.TempDir()
	src, err := os.ReadFile(filepath.Join(repoRoot(t), "copilot-app", "extension.mjs"))
	require.NoError(t, err)
	sdk := filepath.Join(dir, "node_modules", "@github", "copilot-sdk")
	require.NoError(t, os.MkdirAll(sdk, 0o755))
	files := map[string]string{
		"extension.mjs":           string(src),
		"driver.mjs":              extensionDriver,
		"copilot-app-on-event.sh": `{ printf '%s ' "$1"; cat; echo; } >> "$LOG"` + "\n",
		filepath.Join("node_modules", "@github", "copilot-sdk", "package.json"): `{"name":"@github/copilot-sdk","type":"module","exports":{"./extension":"./extension.js"}}`,
		filepath.Join("node_modules", "@github", "copilot-sdk", "extension.js"): extensionSDKStub,
	}
	for name, body := range files {
		require.NoError(t, os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644))
	}

	log := filepath.Join(dir, "calls.log")
	cmd := exec.Command("node", "driver.mjs", live, strconv.Itoa(want), exitReason)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "LOG="+log)
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "driver: %s", out)

	body, _ := os.ReadFile(log)
	return strings.Split(strings.TrimSpace(string(body)), "\n")
}

func eventNames(calls []string) []string {
	names := make([]string, len(calls))
	for i, c := range calls {
		names[i], _, _ = strings.Cut(c, " ")
	}
	return names
}

// The first prompt is history by the time the extension listens. A turn that
// goes idle before the history read returns is still that prompt's turn, and
// carries the events from both the history and the live stream, once each.
func TestCopilotAppExtension_idleDuringCatchUpKeepsTheFirstTurn(t *testing.T) {
	calls := runExtension(t, "session.idle", 3, "")
	require.Equal(t, []string{"sessionStart", "userPromptSubmitted", "turnEnd"}, eventNames(calls))
	assert.Equal(t, 1, strings.Count(calls[2], `"tool.execution_start"`))
	assert.Equal(t, 1, strings.Count(calls[2], `"tool.execution_complete"`))
}

// A session that shut down while the history read was pending stays ended:
// no prompt is replayed after its sessionEnd.
func TestCopilotAppExtension_shutdownDuringCatchUpStaysEnded(t *testing.T) {
	calls := runExtension(t, "session.shutdown", 2, "")
	assert.Equal(t, []string{"sessionStart", "sessionEnd"}, eventNames(calls))
}

// The app stops the extension soon after a user exits, so the sessionEnd hook
// must not return before the open turn and the session end have been sent.
func TestCopilotAppExtension_userExitWaitsForTheFinalSends(t *testing.T) {
	calls := runExtension(t, "", 5, "user_exit")
	assert.Equal(t, []string{"sessionStart", "userPromptSubmitted", "turnEnd", "sessionEnd", "hookReturned"}, eventNames(calls))
}
