// SPDX-FileCopyrightText: Copyright 2026 Dash0 Inc.
// SPDX-License-Identifier: Apache-2.0

package consistency

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/dash0hq/dash0-agent-plugin/internal/source/codex"
)

// TestCodexHookEventsMatchManifest locks the two Codex install paths to the same
// event set. They necessarily enumerate it twice:
//
//   - `codex plugin add` reads codex/hooks.json, referenced from
//     .codex-plugin/plugin.json.
//   - install-codex.sh writes a config.toml block rendered by
//     `emit-codex-hooks`, which uses codex.HookEvents — it cannot read
//     codex/hooks.json, because the installer only fetches the bootstrap script,
//     and it needs Go anyway to compute each hook's trusted_hash from
//     install-time values (absolute command path, group index).
//
// Nothing else ties the two together, so adding an event to one and forgetting
// the other would silently leave that install path uninstrumented.
func TestCodexHookEventsMatchManifest(t *testing.T) {
	root := repoRoot(t)

	raw, err := os.ReadFile(filepath.Join(root, "codex", "hooks.json"))
	require.NoError(t, err)

	// json.Unmarshal into a map loses ordering, so decode the object key order
	// from the token stream instead — the two lists are asserted order-sensitive
	// so the config.toml block and the manifest stay readable side by side.
	var doc struct {
		Hooks json.RawMessage `json:"hooks"`
	}
	require.NoError(t, json.Unmarshal(raw, &doc))
	require.NotEmpty(t, doc.Hooks, "codex/hooks.json has no hooks object")

	manifestEvents := jsonObjectKeys(t, doc.Hooks)

	goEvents := make([]string, 0, len(codex.HookEvents))
	for _, e := range codex.HookEvents {
		goEvents = append(goEvents, e.ConfigName)
	}

	assert.Equal(t, manifestEvents, goEvents,
		"codex/hooks.json and codex.HookEvents must declare the same events in the same order — "+
			"the marketplace install reads the JSON, install-codex.sh renders from HookEvents")
}

// jsonObjectKeys returns a JSON object's keys in source order.
func jsonObjectKeys(t *testing.T, obj json.RawMessage) []string {
	t.Helper()
	dec := json.NewDecoder(bytes.NewReader(obj))

	tok, err := dec.Token()
	require.NoError(t, err)
	require.Equal(t, json.Delim('{'), tok, "expected a JSON object")

	var keys []string
	for dec.More() {
		tok, err := dec.Token()
		require.NoError(t, err)
		key, ok := tok.(string)
		require.True(t, ok, "expected a string key, got %T", tok)
		keys = append(keys, key)

		// Skip the value, whatever shape it has.
		var discard json.RawMessage
		require.NoError(t, dec.Decode(&discard))
	}
	return keys
}
