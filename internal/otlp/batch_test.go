// SPDX-FileCopyrightText: Copyright 2026 Dash0 Inc.
// SPDX-License-Identifier: Apache-2.0

package otlp

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/dash0hq/dash0-agent-plugin/internal/identity"
)

func TestBatchContextResolvesIdentityOnceWithoutChangingOtherCallers(t *testing.T) {
	original := resolveIdentity
	t.Cleanup(func() { resolveIdentity = original })
	calls := 0
	resolveIdentity = func() identity.Info { calls++; return identity.Info{Name: "test-user", Source: identity.SourceGit} }
	cfg := Config{OmitUserInfo: true}.WithSpanContext()
	for range 20 {
		span := NewToolSpan("trace", "span", "parent", time.Now(), time.Now(), map[string]any{"tool_name": "test"}, false, cfg)
		found := false
		for _, a := range span.Attributes {
			if a.Key == "user.name" {
				require.Equal(t, hashIdentity("test-user"), *a.Value.StringValue)
				found = true
			}
		}
		require.True(t, found)
	}
	require.Equal(t, 1, calls)
	_ = NewLLMSpan("trace", "span", "parent", time.Now(), time.Now(), nil, false, Config{})
	require.Equal(t, 2, calls)
}
