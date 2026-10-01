// SPDX-FileCopyrightText: Copyright 2026 Dash0 Inc.
// SPDX-License-Identifier: Apache-2.0

import assert from "node:assert/strict"
import { test } from "node:test"
import { Translator } from "../src/translate.ts"
import { CAPTURE_MCP_SERVERS, readCapture, replay } from "./replay.ts"

const ROOT = "ses_fd56324abffeYUUI5c6tAmJq8L"
const CHILD = "ses_fd5632109ffeIJoNI5KhyWKCl2"

test("the recorded session produces exactly this sequence of canonical events", () => {
  const { forwarded } = replay()

  assert.deepEqual(
    forwarded.map((f) => f.canonical),
    [
      "SessionStart",
      "UserPromptSubmit",
      "PostToolUse", // read
      "PostToolUseFailure", // read of a missing file
      "PostToolUse", // the MCP tool
      "SubagentStart",
      "SubagentStop",
      "PostToolUse", // the delegation itself, which the normalizer renames to Agent
      "Stop",
    ],
  )
})

// The whole point of the filter: the bus reports a streaming delta per token,
// and a regression that forwards one would show up here as a spawn count in
// the dozens rather than as a subtly wrong span.
test("the recorded session spawns the wrapper once per canonical event", () => {
  const lines = readCapture()
  const { forwarded } = replay(lines)

  assert.equal(forwarded.length, 9)
  assert.ok(lines.length > 180, "the fixture should still be the full unfiltered stream")
})

test("every forwarded event collapses onto the root session", () => {
  const { forwarded } = replay()
  for (const { envelope } of forwarded) {
    assert.equal(envelope.root_session_id, ROOT)
  }
})

test("the child session's events carry the parent's root and the child's own assistant entry", () => {
  const { forwarded } = replay()
  const subagentStop = forwarded.find((f) => f.canonical === "SubagentStop")
  assert.ok(subagentStop)

  const child = subagentStop.envelope.assistants[CHILD]
  assert.ok(child)
  assert.equal(child.mode, "general")
  assert.equal(child.text, "done")
  assert.deepEqual(child.tokens, { input: 98, output: 10, reasoning: 5, cache: { read: 7, write: 0 } })
})

test("a turn's usage is summed across its steps and flushed on Stop", () => {
  const { translator, forwarded } = replay()
  const stop = forwarded.find((f) => f.canonical === "Stop")
  assert.ok(stop)

  const root = stop.envelope.assistants[ROOT]
  assert.ok(root)
  assert.equal(root.modelID, "mock-model")
  assert.equal(root.text, "All done.")
  // Five assistant messages: inputs 94+95+96+97+99, outputs 6+7+8+9+11,
  // reasoning and cache reads 5 × 5 and 5 × 7.
  assert.deepEqual(root.tokens, { input: 481, output: 41, reasoning: 25, cache: { read: 35, write: 0 } })

  const [next] = translator.shutdown()
  assert.ok(next)
  assert.deepEqual(next.envelope.assistants[ROOT]?.tokens, {
    input: 0,
    output: 0,
    reasoning: 0,
    cache: { read: 0, write: 0 },
  })
})

// The placeholder OpenCode gives a brand-new session is passed through as it
// arrives; `internal/source/opencode` is where it stops being a conversation
// name, so both runtimes drop it in the same place.
test("the envelope carries the session title OpenCode reported at that moment", () => {
  const { forwarded } = replay()
  assert.equal(forwarded.find((f) => f.canonical === "Stop")?.envelope.session_title, "done")
  assert.match(
    forwarded.find((f) => f.canonical === "SessionStart")?.envelope.session_title ?? "",
    /^New session - /,
  )
})

test("the configured MCP server keys ride along on every envelope", () => {
  const { forwarded } = replay()
  for (const { envelope } of forwarded) {
    assert.deepEqual(envelope.mcp_servers, CAPTURE_MCP_SERVERS)
  }
})

test("shutdown emits one SessionEnd per root session and none for a child", () => {
  const { translator } = replay()
  const shutdown = translator.shutdown()

  assert.deepEqual(
    shutdown.map((f) => f.canonical),
    ["SessionEnd"],
  )
  assert.deepEqual(shutdown[0]?.envelope.payload, { sessionID: ROOT })
  assert.equal(shutdown[0]?.envelope.root_session_id, ROOT)
})

test("a repeated terminal tool update spawns once", () => {
  const translator = new Translator("/tmp/project")
  const terminal = {
    type: "message.part.updated",
    properties: {
      part: {
        type: "tool",
        sessionID: "ses_root",
        callID: "call_0",
        tool: "read",
        state: { status: "completed", time: { start: 1, end: 2 } },
      },
    },
  }

  assert.ok(translator.event(terminal))
  assert.equal(translator.event(terminal), null)
})

test("a non-terminal tool update never spawns", () => {
  const translator = new Translator("/tmp/project")
  for (const status of ["pending", "running"]) {
    const result = translator.event({
      type: "message.part.updated",
      properties: {
        part: { type: "tool", sessionID: "ses_root", callID: "call_0", tool: "read", state: { status } },
      },
    })
    assert.equal(result, null)
  }
})

test("a child session's prompt is dropped so it never allocates a second trace", () => {
  const translator = new Translator("/tmp/project")
  translator.event({ type: "session.created", properties: { info: { id: "ses_root" } } })
  translator.event({ type: "session.created", properties: { info: { id: "ses_child", parentID: "ses_root" } } })

  assert.ok(translator.chatMessage({ sessionID: "ses_root" }, {}))
  assert.equal(translator.chatMessage({ sessionID: "ses_child" }, {}), null)
})

test("a session.updated that omits parentID leaves the child attached to its root", () => {
  const translator = new Translator("/tmp/project")
  translator.event({ type: "session.created", properties: { info: { id: "ses_root", title: "the turn" } } })
  translator.event({ type: "session.created", properties: { info: { id: "ses_child", parentID: "ses_root" } } })
  translator.event({ type: "session.updated", properties: { info: { id: "ses_child" } } })

  const idle = translator.event({ type: "session.idle", properties: { sessionID: "ses_child" } })
  assert.equal(idle?.canonical, "SubagentStop")
  assert.equal(idle?.envelope.root_session_id, "ses_root")
  assert.equal(idle?.envelope.session_title, "the turn")
  assert.equal(translator.chatMessage({ sessionID: "ses_child" }, {}), null)
  assert.deepEqual(
    translator.shutdown().map((f) => f.envelope.payload),
    [{ sessionID: "ses_root" }],
  )
})

test("a session.error with no session is dropped", () => {
  const translator = new Translator("/tmp/project")
  assert.equal(translator.event({ type: "session.error", properties: { error: { name: "boom" } } }), null)
})

test("a child session's error becomes SubagentStop, which leaves the parent turn's trace alive", () => {
  const translator = new Translator("/tmp/project")
  translator.event({ type: "session.created", properties: { info: { id: "ses_root" } } })
  translator.event({ type: "session.created", properties: { info: { id: "ses_child", parentID: "ses_root" } } })

  const child = translator.event({ type: "session.error", properties: { sessionID: "ses_child" } })
  assert.equal(child?.canonical, "SubagentStop")
  const root = translator.event({ type: "session.error", properties: { sessionID: "ses_root" } })
  assert.equal(root?.canonical, "StopFailure")
})
