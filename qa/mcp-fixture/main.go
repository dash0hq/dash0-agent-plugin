// SPDX-FileCopyrightText: Copyright 2026 Dash0 Inc.
// SPDX-License-Identifier: Apache-2.0

// Command mcp-fixture is a stdio MCP server that exists so a QA session can make
// a real MCP tool call whose exact shape is known before the session starts.
//
// Why a stub rather than a real server. The connected claude.ai connectors are
// production systems: their availability in a headless `claude -p` session is
// not guaranteed, their output is not reproducible, and a spec that reads one is
// asserting against somebody else's data. This server's tool names, arguments,
// and results are fixed, so the expectation for a run is computable from the
// prompt alone.
//
// The MCP tool name Claude Code reports to a hook is mcp__<key>__<tool>, where
// <key> is the server's key in the --mcp-config JSON, not anything this process
// says about itself. So one binary serves every server identity a spec needs;
// the config file picks the name. See qa/tools/mcp-config.json.
//
// Tools:
//
//	echo_text    — returns "qa-fixture <server>: <text>". Always succeeds.
//	always_fails — returns a JSON-RPC error. Exists so a spec can ask what the
//	               plugin does with an MCP call that fails, without needing a
//	               real server to break.
//
// QA_MCP_SERVER_NAME labels the instance in serverInfo and in echo_text's
// result, so a two-server run can tell its two processes apart in a transcript.
// It does not affect the mcp__ prefix.
//
// Protocol: JSON-RPC 2.0, one object per line, on stdin and stdout. Nothing but
// protocol goes to stdout; diagnostics go to stderr.
package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
)

const (
	// The version echoed when the client does not name one. A client that does
	// name one gets its own back: this server has no version-specific
	// behaviour, and disagreeing about the version is the one way a stub can
	// fail to connect at all.
	fallbackProtocolVersion = "2024-11-05"

	// JSON-RPC reserved code for an application error. always_fails returns it
	// rather than an isError result, because a protocol-level error is the
	// unambiguous failure; whether Claude Code turns either into a
	// PostToolUseFailure hook is exactly what a spec is for.
	codeToolFailed = -32000
	codeNoMethod   = -32601
)

type request struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params"`
}

type response struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func main() {
	serverName := os.Getenv("QA_MCP_SERVER_NAME")
	if serverName == "" {
		serverName = "qa-fixture"
	}

	in := bufio.NewScanner(os.Stdin)
	// A tool result can be larger than the default 64 KB line budget, and a
	// truncated line reads as a protocol error rather than as a big message.
	in.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	out := bufio.NewWriter(os.Stdout)

	for in.Scan() {
		line := in.Bytes()
		if len(line) == 0 {
			continue
		}
		var req request
		if err := json.Unmarshal(line, &req); err != nil {
			fmt.Fprintf(os.Stderr, "qa-mcp-fixture: undecodable line: %v\n", err)
			continue
		}
		// A notification has no id and takes no reply. notifications/initialized
		// is the one that matters: replying to it is a protocol violation.
		if len(req.ID) == 0 {
			continue
		}
		resp := handle(req, serverName)
		encoded, err := json.Marshal(resp)
		if err != nil {
			fmt.Fprintf(os.Stderr, "qa-mcp-fixture: unencodable reply: %v\n", err)
			continue
		}
		_, _ = out.Write(encoded)
		_ = out.WriteByte('\n')
		// Flushed per message: the client blocks on the reply, so a buffered
		// one deadlocks the session rather than slowing it down. A flush error
		// means stdout is gone, and every later reply would be written into the
		// void while the client waited on it. Stop, loudly.
		if err := out.Flush(); err != nil {
			fmt.Fprintf(os.Stderr, "qa-mcp-fixture: stdout: %v\n", err)
			os.Exit(1)
		}
	}
	if err := in.Err(); err != nil {
		fmt.Fprintf(os.Stderr, "qa-mcp-fixture: stdin: %v\n", err)
		os.Exit(1)
	}
}

func handle(req request, serverName string) response {
	reply := response{JSONRPC: "2.0", ID: req.ID}

	switch req.Method {
	case "initialize":
		reply.Result = map[string]any{
			"protocolVersion": negotiatedVersion(req.Params),
			"capabilities":    map[string]any{"tools": map[string]any{}},
			"serverInfo":      map[string]any{"name": serverName, "version": "1.0.0"},
		}

	case "tools/list":
		reply.Result = map[string]any{"tools": []any{
			map[string]any{
				"name":        "echo_text",
				"description": "Returns the text it is given. Used by QA to make one MCP tool call whose result is known in advance.",
				"inputSchema": map[string]any{
					"type":       "object",
					"properties": map[string]any{"text": map[string]any{"type": "string"}},
					"required":   []any{"text"},
				},
			},
			map[string]any{
				"name":        "always_fails",
				"description": "Always returns an error. Used by QA to observe how a failed MCP tool call is reported.",
				"inputSchema": map[string]any{"type": "object", "properties": map[string]any{}},
			},
		}}

	case "tools/call":
		var params struct {
			Name      string         `json:"name"`
			Arguments map[string]any `json:"arguments"`
		}
		_ = json.Unmarshal(req.Params, &params)
		switch params.Name {
		case "echo_text":
			text, _ := params.Arguments["text"].(string)
			reply.Result = map[string]any{"content": []any{
				map[string]any{"type": "text", "text": fmt.Sprintf("qa-fixture %s: %s", serverName, text)},
			}}
		case "always_fails":
			reply.Error = &rpcError{Code: codeToolFailed, Message: "qa-fixture: always_fails failed on purpose"}
		default:
			reply.Error = &rpcError{Code: codeNoMethod, Message: "qa-fixture: no such tool: " + params.Name}
		}

	default:
		reply.Error = &rpcError{Code: codeNoMethod, Message: "qa-fixture: no such method: " + req.Method}
	}

	return reply
}

// negotiatedVersion echoes the client's protocol version back. MCP negotiates by
// the server naming a version it supports, and a stub that insists on one
// version fails to connect the day the host moves on.
func negotiatedVersion(params json.RawMessage) string {
	var p struct {
		ProtocolVersion string `json:"protocolVersion"`
	}
	if err := json.Unmarshal(params, &p); err == nil && p.ProtocolVersion != "" {
		return p.ProtocolVersion
	}
	return fallbackProtocolVersion
}
