// SPDX-FileCopyrightText: Copyright 2026 Dash0 Inc.
// SPDX-License-Identifier: Apache-2.0

package otlpcapture

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

// TestWaitForChatSpans covers the turn barrier the e2e tests synchronize on,
// including that it counts only chat spans.
func TestWaitForChatSpans(t *testing.T) {
	c := &Capture{}

	// A tool span must not be mistaken for a closed turn.
	c.requests = append(c.requests, Request{Path: TracesPath, Body: exportBody(t, "execute_tool")})
	c.requests = append(c.requests, Request{Path: TracesPath, Body: exportBody(t, "chat")})

	c.WaitForChatSpans(t, 1, 2*time.Second)

	assert.Len(t, ChatSpans(CollectSpans(t, c.TraceBodies())), 1,
		"the barrier must count chat spans only")
}

// exportBody builds a minimal OTLP traces payload holding one span with the given
// operation, matching the wire shape the exporter produces.
func exportBody(t *testing.T, operation string) []byte {
	t.Helper()
	return []byte(`{"resourceSpans":[{"scopeSpans":[{"spans":[{"name":"` + operation +
		`","attributes":[{"key":"gen_ai.operation.name","value":{"stringValue":"` + operation + `"}}]}]}]}]}`)
}
