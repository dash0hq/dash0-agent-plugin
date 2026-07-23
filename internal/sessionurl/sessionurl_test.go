// SPDX-FileCopyrightText: Copyright 2026 Dash0 Inc.
// SPDX-License-Identifier: Apache-2.0

package sessionurl

import (
	"bytes"
	"compress/zlib"
	"encoding/base64"
	"encoding/json"
	"io"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDeriveAppURL(t *testing.T) {
	tests := []struct {
		name    string
		otlpURL string
		want    string
	}{
		{"dash0 prod us1", "https://ingress.us1.dash0.com:4318", "https://app.dash0.com"},
		{"dash0 prod eu1", "https://ingress.eu1.dash0.com:4318", "https://app.dash0.com"},
		{"dash0 dev", "https://ingress.eu-west-1.aws.dash0-dev.com:4318", "https://app.dash0-dev.com"},
		{"dash0 dev no port", "https://ingress.eu-west-1.aws.dash0-dev.com", "https://app.dash0-dev.com"},
		{"unknown endpoint", "https://otel.example.com:4318", ""},
		{"empty", "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, deriveAppURL(tt.otlpURL))
		})
	}
}

func TestSessionURL_UnknownHostIsEmpty(t *testing.T) {
	assert.Empty(t, SessionURL("https://otel.example.com:4318", "sess-abc123"))
	assert.Empty(t, SessionURL("", "sess-abc123"))
}

func TestSessionURL(t *testing.T) {
	u := SessionURL("https://ingress.us1.dash0.com:4318", "sess-abc123")
	assert.Contains(t, u, "https://app.dash0.com/coding-agents?s=")
	assert.NotContains(t, u, "agent-monitoring")

	// Round-trip: decode the ?s= param and verify the state structure matches
	// what the Dash0 UI url-state library expects.
	parts := strings.SplitN(u, "?s=", 2)
	require.Len(t, parts, 2)
	compressed, err := base64.URLEncoding.WithPadding(base64.NoPadding).DecodeString(parts[1])
	require.NoError(t, err)
	r, err := zlib.NewReader(bytes.NewReader(compressed))
	require.NoError(t, err)
	decoded, err := io.ReadAll(r)
	require.NoError(t, err)

	var state map[string]any
	require.NoError(t, json.Unmarshal(decoded, &state))
	page, ok := state["/coding-agents"].(map[string]any)
	require.True(t, ok, "state must be keyed by pathname")
	// agentSession drives the detail sidebar; the sessions tab must be selected.
	assert.Equal(t, "sess-abc123", page["agentSession"])
	tab, ok := page["tab"].(map[string]any)
	require.True(t, ok, "coding-agents state must carry a tab selection")
	assert.Equal(t, "sessions", tab["pageTab"])
}
