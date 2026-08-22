// SPDX-FileCopyrightText: Copyright 2026 Dash0 Inc.
// SPDX-License-Identifier: Apache-2.0
//
// Scripted OpenAI-compatible server. Drives one deterministic OpenCode turn
// that exercises everything the capture needs: a successful tool call, a
// failing tool call, an MCP tool call, a delegated sub-agent, and several
// assistant steps. OpenCode reaches it via `provider.mock.options.baseURL`.

import http from "node:http"
import fs from "node:fs"

const PORT = Number(process.env.MOCK_LLM_PORT ?? 8817)
const REQUEST_LOG = process.env.MOCK_LLM_REQUEST_LOG ?? ""
const PROJECT_DIR = process.env.MOCK_LLM_PROJECT_DIR ?? process.cwd()

// Each step is resolved against the tool names the request actually offers, so
// a rename on OpenCode's side degrades to a skipped step rather than a hang.
const SCRIPT = [
  { text: "Reading the project readme.", tool: ["read"], args: { filePath: `${PROJECT_DIR}/README.md` } },
  { text: "Now a call that fails.", tool: ["read"], args: { filePath: `${PROJECT_DIR}/no-such-file.txt` } },
  { text: "Calling the MCP tool.", tool: ["capture_echo", "capture*echo"], args: { message: "hello from mcp" } },
  {
    text: "Delegating to a sub-agent.",
    tool: ["task"],
    args: { description: "Sub-agent step", prompt: "Reply with the word done.", subagent_type: "general" },
  },
  { text: "All done." },
]

function resolveTool(candidates, available) {
  for (const c of candidates) {
    if (available.includes(c)) return c
    const re = new RegExp("^" + c.replace(/[.*+?^${}()|[\]\\]/g, "\\$&").replace(/\\\*/g, ".") + "$")
    const hit = available.find((n) => re.test(n))
    if (hit) return hit
  }
  return undefined
}

function chunk(delta, finish) {
  return {
    id: "chatcmpl-mock",
    object: "chat.completion.chunk",
    created: Math.floor(Date.now() / 1000),
    model: "mock-model",
    choices: [{ index: 0, delta, finish_reason: finish ?? null }],
  }
}

function respond(res, stream, message, finishReason, usage) {
  if (!stream) {
    res.writeHead(200, { "content-type": "application/json" })
    res.end(
      JSON.stringify({
        id: "chatcmpl-mock",
        object: "chat.completion",
        created: Math.floor(Date.now() / 1000),
        model: "mock-model",
        choices: [{ index: 0, message, finish_reason: finishReason }],
        usage,
      }),
    )
    return
  }

  res.writeHead(200, { "content-type": "text/event-stream", "cache-control": "no-cache", connection: "keep-alive" })
  const emit = (payload) => res.write(`data: ${JSON.stringify(payload)}\n\n`)

  emit(chunk({ role: "assistant", content: "" }))
  if (message.content) emit(chunk({ content: message.content }))
  ;(message.tool_calls ?? []).forEach((tc, index) => {
    emit(chunk({ tool_calls: [{ index, id: tc.id, type: "function", function: tc.function }] }))
  })
  emit(chunk({}, finishReason))
  emit({ ...chunk({}, finishReason), usage })
  res.write("data: [DONE]\n\n")
  res.end()
}

let callSeq = 0

function handle(req, res, raw) {
  if (REQUEST_LOG && !req.url.includes("probe=1")) {
    fs.appendFileSync(REQUEST_LOG, JSON.stringify({ ts: Date.now(), method: req.method, url: req.url, headers: req.headers, body: raw }) + "\n")
  }

  if (req.url.includes("/models")) {
    res.writeHead(200, { "content-type": "application/json" })
    res.end(JSON.stringify({ object: "list", data: [{ id: "mock-model", object: "model" }] }))
    return
  }

  let body = {}
  try {
    body = JSON.parse(raw)
  } catch {
    // fall through with an empty body; the no-tools branch answers safely
  }

  const stream = body.stream === true
  const available = (body.tools ?? []).map((t) => t.function?.name).filter(Boolean)
  const usage = {
    prompt_tokens: 100 + callSeq,
    completion_tokens: 10 + callSeq,
    total_tokens: 110 + 2 * callSeq,
    completion_tokens_details: { reasoning_tokens: 5 },
    prompt_tokens_details: { cached_tokens: 7 },
  }
  callSeq++

  // Title generation and sub-agent sessions get no `task` tool; answer in one
  // step so the sub-agent session terminates instead of recursing.
  if (!available.includes("task")) {
    respond(res, stream, { role: "assistant", content: "done" }, "stop", usage)
    return
  }

  const step = (body.messages ?? []).filter((m) => m.role === "assistant" && (m.tool_calls ?? []).length > 0).length
  const spec = SCRIPT[Math.min(step, SCRIPT.length - 1)]
  const toolName = spec.tool ? resolveTool(spec.tool, available) : undefined

  if (!toolName) {
    respond(res, stream, { role: "assistant", content: spec.text ?? "done" }, "stop", usage)
    return
  }

  respond(
    res,
    stream,
    {
      role: "assistant",
      content: spec.text ?? "",
      tool_calls: [
        {
          id: `call_${step}_${Date.now()}`,
          type: "function",
          function: { name: toolName, arguments: JSON.stringify(spec.args ?? {}) },
        },
      ],
    },
    "tool_calls",
    usage,
  )
}

http
  .createServer((req, res) => {
    let raw = ""
    req.on("data", (c) => (raw += c))
    req.on("end", () => {
      try {
        handle(req, res, raw)
      } catch (err) {
        res.writeHead(500, { "content-type": "application/json" })
        res.end(JSON.stringify({ error: { message: String(err) } }))
      }
    })
  })
  .listen(PORT, "127.0.0.1", () => console.error(`mock-llm listening on ${PORT}`))
