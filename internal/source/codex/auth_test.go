// SPDX-FileCopyrightText: Copyright 2026 Dash0 Inc.
// SPDX-License-Identifier: Apache-2.0

package codex

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBillingMode(t *testing.T) {
	dir := t.TempDir()
	write := func(name, content string) string {
		p := filepath.Join(dir, name)
		require.NoError(t, os.WriteFile(p, []byte(content), 0o600))
		return p
	}

	cases := []struct {
		name, content, want string
	}{
		// The API key + tokens are present to prove we never read them.
		{"chatgpt", `{"auth_mode":"chatgpt","OPENAI_API_KEY":null,"tokens":{"access_token":"secret"}}`, billingSubscription},
		{"apikey", `{"auth_mode":"apikey","OPENAI_API_KEY":"sk-secret"}`, billingAPI},
		{"other_nonempty", `{"auth_mode":"api_key"}`, billingAPI},
		{"empty_auth_mode", `{"auth_mode":""}`, billingUnknown},
		{"missing_field", `{"tokens":{}}`, billingUnknown},
		{"bad_json", `not json`, billingUnknown},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			assert.Equal(t, c.want, billingMode(write(c.name+".json", c.content)))
		})
	}

	assert.Equal(t, billingUnknown, billingMode(filepath.Join(dir, "does-not-exist.json")), "missing file")
	assert.Equal(t, billingUnknown, billingMode(""), "empty path")
}

func TestInjectBillingMode(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "auth.json")
	require.NoError(t, os.WriteFile(p, []byte(`{"auth_mode":"chatgpt"}`), 0o600))

	old := authPathOverride
	authPathOverride = p
	defer func() { authPathOverride = old }()

	ev := map[string]any{"hook_event_name": "Stop"}
	injectBillingMode(ev)
	assert.Equal(t, billingSubscription, ev["dash0.codex.billing_mode"])
}
