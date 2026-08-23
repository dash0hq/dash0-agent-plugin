// SPDX-FileCopyrightText: Copyright 2026 Dash0 Inc.
// SPDX-License-Identifier: Apache-2.0

// A QA fixture is only useful if its output is exact. These tests pin every
// value a spec asserts against, so a change to the fixture breaks here rather
// than in a paid live session that reports a product finding instead.
package main

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func call(t *testing.T, method, params string) response {
	t.Helper()
	return handle(request{
		JSONRPC: "2.0",
		ID:      json.RawMessage(`1`),
		Method:  method,
		Params:  json.RawMessage(params),
	}, "alpha")
}

func TestInitializeEchoesTheClientVersion(t *testing.T) {
	resp := call(t, "initialize", `{"protocolVersion":"2025-06-18"}`)

	require.Nil(t, resp.Error)
	result := resp.Result.(map[string]any)
	assert.Equal(t, "2025-06-18", result["protocolVersion"])
	assert.Equal(t, map[string]any{"name": "alpha", "version": "1.0.0"}, result["serverInfo"])
	assert.Contains(t, result["capabilities"], "tools")
}

func TestInitializeFallsBackWhenTheClientNamesNoVersion(t *testing.T) {
	resp := call(t, "initialize", `{}`)

	require.Nil(t, resp.Error)
	assert.Equal(t, fallbackProtocolVersion, resp.Result.(map[string]any)["protocolVersion"])
}

// Exactly two tools, named exactly this. A spec's expected tool name comes from
// here, and a third tool would let a run make a call no spec accounts for.
func TestToolsListIsExactlyTwoTools(t *testing.T) {
	resp := call(t, "tools/list", `{}`)

	require.Nil(t, resp.Error)
	tools := resp.Result.(map[string]any)["tools"].([]any)
	require.Len(t, tools, 2)

	var names []string
	for _, tool := range tools {
		names = append(names, tool.(map[string]any)["name"].(string))
	}
	assert.Equal(t, []string{"echo_text", "always_fails"}, names)
}

func TestEchoTextResultIsExact(t *testing.T) {
	resp := call(t, "tools/call", `{"name":"echo_text","arguments":{"text":"qa-mcp-probe"}}`)

	require.Nil(t, resp.Error)
	content := resp.Result.(map[string]any)["content"].([]any)
	require.Len(t, content, 1)
	assert.Equal(t, map[string]any{"type": "text", "text": "qa-fixture alpha: qa-mcp-probe"}, content[0])
}

func TestAlwaysFailsReturnsAProtocolError(t *testing.T) {
	resp := call(t, "tools/call", `{"name":"always_fails","arguments":{}}`)

	require.Nil(t, resp.Result)
	require.NotNil(t, resp.Error)
	assert.Equal(t, codeToolFailed, resp.Error.Code)
	assert.Equal(t, "qa-fixture: always_fails failed on purpose", resp.Error.Message)
}

func TestUnknownToolAndMethodAreErrors(t *testing.T) {
	tool := call(t, "tools/call", `{"name":"no_such_tool","arguments":{}}`)
	require.NotNil(t, tool.Error)
	assert.Equal(t, codeNoMethod, tool.Error.Code)

	method := call(t, "resources/list", `{}`)
	require.NotNil(t, method.Error)
	assert.Equal(t, codeNoMethod, method.Error.Code)
}

// The id is echoed verbatim. A client that sends a string id and gets a number
// back never matches the reply to its request, and the session hangs.
func TestTheRequestIDIsEchoedVerbatim(t *testing.T) {
	resp := handle(request{
		JSONRPC: "2.0",
		ID:      json.RawMessage(`"req-7"`),
		Method:  "tools/list",
		Params:  json.RawMessage(`{}`),
	}, "beta")

	assert.JSONEq(t, `"req-7"`, string(resp.ID))
	assert.Equal(t, "2.0", resp.JSONRPC)
}
