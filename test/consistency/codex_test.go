// SPDX-FileCopyrightText: Copyright 2026 Dash0 Inc.
// SPDX-License-Identifier: Apache-2.0

package consistency

import (
	"bytes"
	"encoding/json"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/dash0hq/dash0-agent-plugin/internal/source/codex"
)

// The two Codex install paths enumerate the event set twice, and nothing else ties
// them together: `codex plugin add` reads codex/hooks.json, while install-codex.sh
// renders a config.toml block from codex.HookEvents. The installer cannot read the
// JSON, fetching only the bootstrap, and needs Go regardless to compute each hook's
// trusted_hash from install-time values.
func TestCodexHookEventsMatchManifest(t *testing.T) {
	a := agentByLabel(t, "codex")

	raw, err := os.ReadFile(abs(t, a.Hooks))
	require.NoError(t, err)

	// A map loses ordering, and the comparison is order-sensitive so the
	// config.toml block and the manifest stay readable side by side.
	var doc struct {
		Hooks json.RawMessage `json:"hooks"`
	}
	require.NoError(t, json.Unmarshal(raw, &doc))
	require.NotEmpty(t, doc.Hooks, "%s has no hooks object", a.Hooks)

	manifestEvents := jsonObjectKeys(t, doc.Hooks)

	goEvents := make([]string, 0, len(codex.HookEvents))
	for _, e := range codex.HookEvents {
		goEvents = append(goEvents, e.ConfigName)
	}

	assert.Equal(t, manifestEvents, goEvents,
		a.Hooks+" and codex.HookEvents must declare the same events in the same order; "+
			"the marketplace install reads the JSON, install-codex.sh renders from HookEvents")
}

// A JSON object's keys in source order.
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

		var discard json.RawMessage
		require.NoError(t, dec.Decode(&discard))
	}
	return keys
}
