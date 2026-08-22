// SPDX-FileCopyrightText: Copyright 2026 Dash0 Inc.
// SPDX-License-Identifier: Apache-2.0
//
// Minimal stdio MCP server exposing one tool, so the capture contains a real
// MCP-provided tool call and we can observe how OpenCode names it.

import readline from "node:readline"

const TOOLS = [
  {
    name: "echo",
    description: "Echoes the message back.",
    inputSchema: {
      type: "object",
      properties: { message: { type: "string", description: "Text to echo." } },
      required: ["message"],
    },
  },
]

function send(msg) {
  process.stdout.write(JSON.stringify(msg) + "\n")
}

readline.createInterface({ input: process.stdin }).on("line", (line) => {
  if (!line.trim()) return
  let req
  try {
    req = JSON.parse(line)
  } catch {
    return
  }
  if (req.id === undefined) return

  const reply = (result) => send({ jsonrpc: "2.0", id: req.id, result })

  switch (req.method) {
    case "initialize":
      reply({
        protocolVersion: req.params?.protocolVersion ?? "2025-06-18",
        capabilities: { tools: {} },
        serverInfo: { name: "dash0-capture-mcp", version: "0.0.0" },
      })
      break
    case "tools/list":
      reply({ tools: TOOLS })
      break
    case "tools/call":
      reply({
        content: [{ type: "text", text: `echo: ${req.params?.arguments?.message ?? ""}` }],
        isError: false,
      })
      break
    default:
      reply({})
  }
})
