// SPDX-FileCopyrightText: Copyright 2026 Dash0 Inc.
// SPDX-License-Identifier: Apache-2.0

package consistency

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The reference runtime is Codex, not Claude Code: Codex owns the only other
// golden span snapshot in the repo, and its normalizer covers the same event
// vocabulary (prompt, tool, failing tool, MCP tool, delegated sub-agent). If a
// Claude Code golden is added later, pointing referenceGolden at it is the whole
// change.
const referenceGolden = "internal/source/codex/testdata/golden_spans.json"

// knownGaps are the reference attributes OpenCode deliberately does not carry.
// Every entry is a fact about OpenCode, not an omission to fix later — anything
// else that goes missing from the normalizer fails this test.
var knownGaps = map[string]string{
	// harness.OpenCode declares no provider: OpenCode is model-agnostic and the
	// provider varies per session, so nothing can fill this at the harness level.
	"gen_ai.provider.name": "OpenCode has no fixed provider",
	// Codex reports a turn id on every hook payload. OpenCode has no turn
	// identity of its own; a turn is delimited by session.idle.
	"turn_id": "OpenCode has no turn id",
	// Read out of the Codex rollout file. OpenCode reports no plan, billing mode
	// or rate-limit state to any consumer.
	"dash0.gen_ai.billing_mode":                      "Codex-only, from the rollout file",
	"dash0.gen_ai.plan_type":                         "Codex-only, from the rollout file",
	"dash0.gen_ai.rate_limit.primary.resets_at":      "Codex-only, from the rollout file",
	"dash0.gen_ai.rate_limit.primary.used_percent":   "Codex-only, from the rollout file",
	"dash0.gen_ai.rate_limit.primary.window_minutes": "Codex-only, from the rollout file",
}

// fixtureGaps are attributes OpenCode would carry for an event its recorded
// session happens not to contain. They are excused for the same reason as
// knownGaps but describe the fixture, not the runtime: extending the capture is
// what removes an entry here.
var fixtureGaps = map[string]string{
	// Extracted by the shared, case-insensitive EnrichToolEvent, so OpenCode's
	// lowercase `bash` tool produces it — the recorded session just never runs one.
	"dash0.gen_ai.tool.bash.command_family": "the recorded session runs no bash tool",
}

func excused(key string) (string, bool) {
	if reason, ok := knownGaps[key]; ok {
		return reason, true
	}
	reason, ok := fixtureGaps[key]
	return reason, ok
}

type goldenSpan struct {
	Name       string                     `json:"name"`
	Attributes map[string]json.RawMessage `json:"attributes"`
}

// TestOpenCodeAttributeParity asserts that for every kind of span both runtimes
// emit, OpenCode carries at least the attribute keys the reference runtime does.
// It is the guard the golden snapshots cannot be: a golden re-blessed after an
// attribute silently disappeared from the normalizer still passes its own test,
// but fails this one.
func TestOpenCodeAttributeParity(t *testing.T) {
	root := repoRoot(t)
	reference := keysByOperation(t, filepath.Join(root, referenceGolden))
	opencode := keysByOperation(t, filepath.Join(root, "internal", "source", "opencode", "testdata", "golden_spans.json"))

	for _, operation := range []string{"chat", "execute_tool", "invoke_agent"} {
		want := reference[operation]
		got := opencode[operation]
		require.NotEmpty(t, want, "the reference golden has no %s span — the fixture no longer covers this event", operation)
		require.NotEmpty(t, got, "the OpenCode golden has no %s span — the fixture no longer covers this event", operation)

		var missing []string
		for key := range want {
			if _, ok := got[key]; !ok {
				if _, ok := excused(key); !ok {
					missing = append(missing, key)
				}
			}
		}
		sort.Strings(missing)
		assert.Emptyf(t, missing, "OpenCode %s spans drop attributes the reference runtime carries: %s",
			operation, strings.Join(missing, ", "))
	}
}

// TestOpenCodeKnownGapsAreStillGaps keeps the excuse lists honest in the other
// direction: an entry that OpenCode has since started emitting is stale and must
// be deleted, or it would go on excusing a future regression of that same key.
func TestOpenCodeKnownGapsAreStillGaps(t *testing.T) {
	root := repoRoot(t)
	opencode := keysByOperation(t, filepath.Join(root, "internal", "source", "opencode", "testdata", "golden_spans.json"))

	for key, reason := range allGaps() {
		for operation, keys := range opencode {
			_, present := keys[key]
			assert.Falsef(t, present, "%q is listed as absent (%s) but the OpenCode %s span carries it — drop the entry",
				key, reason, operation)
		}
	}
}

func allGaps() map[string]string {
	out := map[string]string{}
	for key, reason := range knownGaps {
		out[key] = reason
	}
	for key, reason := range fixtureGaps {
		out[key] = reason
	}
	return out
}

// keysByOperation unions the attribute keys of a golden snapshot per span
// operation, which is what makes the two runtimes comparable: they run different
// sessions, so only the kind of span is common ground.
func keysByOperation(t *testing.T, path string) map[string]map[string]bool {
	t.Helper()
	raw, err := os.ReadFile(path)
	require.NoError(t, err, "reading %s", path)
	var spans []goldenSpan
	require.NoError(t, json.Unmarshal(raw, &spans), "parsing %s", path)

	out := map[string]map[string]bool{}
	for _, span := range spans {
		operation, _, _ := strings.Cut(span.Name, " ")
		if out[operation] == nil {
			out[operation] = map[string]bool{}
		}
		for key := range span.Attributes {
			out[operation][key] = true
		}
	}
	return out
}
