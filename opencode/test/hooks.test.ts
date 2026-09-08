// SPDX-FileCopyrightText: Copyright 2026 Dash0 Inc.
// SPDX-License-Identifier: Apache-2.0

import assert from "node:assert/strict"
import { test } from "node:test"
import { createHooks } from "../src/index.ts"
import type { SpawnOptions } from "../src/spawn.ts"
import { feed, readCapture } from "./replay.ts"

interface Spawned {
  command: string
  payload: string
  options: SpawnOptions
}

function harness({ wrapper }: { wrapper?: string } = { wrapper: "/tmp/opencode-on-event.sh" }) {
  const async: Spawned[] = []
  const blocking: Spawned[] = []
  const toasts: string[] = []
  const hooks = createHooks({
    cwd: "/tmp/project",
    wrapper,
    toast: (message) => toasts.push(message),
    spawnAsync: async (command, payload, options) => void async.push({ command, payload, options }),
    spawnBlocking: (command, payload, options) => void blocking.push({ command, payload, options }),
    registerShutdown: () => {},
  })
  return { hooks, async, blocking, toasts }
}

function envelopes(spawned: Spawned[]): Record<string, unknown>[] {
  return spawned.map((s) => JSON.parse(s.payload) as Record<string, unknown>)
}

test("an unrecognized event shape drops that event only", async () => {
  const { hooks, async } = harness()
  const lines = readCapture()

  await hooks.event({ event: null })
  await hooks.event({ event: "not an object" })
  await hooks.event({ event: { type: "session.created" } })
  await hooks.event({ event: { type: "message.part.updated", properties: { part: null } } })
  await hooks.event({ event: { type: "session.idle", properties: {} } })
  await hooks["chat.message"](undefined, undefined)
  await hooks.flush()
  assert.equal(async.length, 0)

  for (const line of lines) {
    if (line.kind === "event") await hooks.event({ event: line.payload })
    if (line.kind === "hook" && line.name === "chat.message") {
      await hooks["chat.message"](line.payload.input, line.payload.output)
    }
  }
  await hooks.flush()
  assert.equal(async.length, 9)
})

test("a handler survives a payload that makes the translator throw", async () => {
  const { hooks, async } = harness()
  const exploding = {
    type: "session.created",
    get properties() {
      throw new Error("boom")
    },
  }

  await hooks.event({ event: exploding })
  await hooks.event({ event: { type: "session.created", properties: { info: { id: "ses_root" } } } })
  await hooks.flush()

  assert.equal(async.length, 1)
})

test("every spawn carries the project directory and a newline-terminated envelope", async () => {
  const { hooks, async } = harness()
  await hooks.event({ event: { type: "session.created", properties: { info: { id: "ses_root" } } } })
  await hooks.flush()

  const [spawned] = async
  assert.ok(spawned)
  assert.equal(spawned.command, "/tmp/opencode-on-event.sh")
  assert.equal(spawned.options.cwd, "/tmp/project")
  assert.ok(spawned.payload.endsWith("\n"))
  assert.equal(JSON.parse(spawned.payload).cwd, "/tmp/project")
})

test("only the session-start spawn reads the child's stderr", async () => {
  const { hooks, async } = harness()
  await hooks.event({ event: { type: "session.created", properties: { info: { id: "ses_root" } } } })
  await hooks.event({ event: { type: "session.idle", properties: { sessionID: "ses_root" } } })
  await hooks.flush()

  assert.equal(typeof async[0]?.options.onStderr, "function")
  assert.equal(async[1]?.options.onStderr, undefined)
})

test("the pipeline's user message is toasted and the wrapper's own diagnostics are not", async () => {
  const { hooks, async, toasts } = harness()
  await hooks.event({ event: { type: "session.created", properties: { info: { id: "ses_root" } } } })
  await hooks.flush()

  async[0]?.options.onStderr?.(
    "opencode-on-event: download failed\ndash0: connected (v0.1.24) — https://app.dash0.com/x\n",
  )

  assert.deepEqual(toasts, ["dash0: connected (v0.1.24) — https://app.dash0.com/x"])
})

test("nothing is toasted when no endpoint is configured", async () => {
  const { hooks, async, toasts } = harness()
  await hooks.event({ event: { type: "session.created", properties: { info: { id: "ses_root" } } } })
  await hooks.flush()

  async[0]?.options.onStderr?.("dash0: telemetry is not active — configure the plugin to start sending data.\n")

  assert.deepEqual(toasts, [])
})

test("a toast that throws does not escape the stderr callback", async () => {
  const { hooks, async } = harness()
  const failing = createHooks({
    cwd: "/tmp/project",
    wrapper: "/tmp/opencode-on-event.sh",
    toast: () => {
      throw new Error("no tui")
    },
    spawnAsync: async (command, payload, options) => void async.push({ command, payload, options }),
    registerShutdown: () => {},
  })

  await failing.event({ event: { type: "session.created", properties: { info: { id: "ses_root" } } } })
  await failing.flush()
  assert.doesNotThrow(() => async[0]?.options.onStderr?.("dash0: connected\n"))
})

// Stop clears the turn's trace context, so a tool event that reaches the
// pipeline after it loses its parent and is dropped. Two concurrent processes
// give no ordering guarantee, so the spawns queue.
test("the wrappers run one after another, and the handler does not wait for them", async () => {
  const started: string[] = []
  const finished: string[] = []
  const release: Array<() => void> = []
  const hooks = createHooks({
    cwd: "/tmp/project",
    wrapper: "/tmp/opencode-on-event.sh",
    spawnAsync: (_command, payload) => {
      const canonical = String(JSON.parse(payload).name)
      started.push(canonical)
      return new Promise<void>((resolve) =>
        release.push(() => {
          finished.push(canonical)
          resolve()
        }),
      )
    },
    registerShutdown: () => {},
  })

  const before = process.hrtime.bigint()
  await hooks.event({ event: { type: "session.created", properties: { info: { id: "ses_root" } } } })
  await hooks.event({ event: { type: "session.idle", properties: { sessionID: "ses_root" } } })
  const elapsedMs = Number(process.hrtime.bigint() - before) / 1e6

  assert.ok(elapsedMs < 100, `the handlers blocked for ${elapsedMs}ms`)
  await Promise.resolve()
  assert.deepEqual(started, ["session.created"])

  release[0]?.()
  await new Promise((done) => setImmediate(done))
  assert.deepEqual(started, ["session.created", "session.idle"])
  assert.deepEqual(finished, ["session.created"])

  release[1]?.()
  await hooks.flush()
  assert.deepEqual(finished, ["session.created", "session.idle"])
})

test("dispose emits SessionEnd once, blocking, so the scratch directory is freed", async () => {
  const { hooks, blocking } = harness()
  for (const line of readCapture()) feed(hooks.translator, line)

  await hooks.dispose()
  await hooks.dispose()

  const sent = envelopes(blocking)
  assert.equal(sent.length, 1)
  assert.equal(sent[0]?.kind, "plugin")
  assert.equal(sent[0]?.name, "shutdown")
  assert.equal(sent[0]?.root_session_id, "ses_fd56324abffeYUUI5c6tAmJq8L")
})

test("without a wrapper the plugin does nothing at all", async () => {
  const { hooks, async, blocking } = harness({})
  await hooks.event({ event: { type: "session.created", properties: { info: { id: "ses_root" } } } })
  await hooks.dispose()

  assert.equal(async.length, 0)
  assert.equal(blocking.length, 0)
})

test("the config hook supplies the MCP server keys the tool-name rewrite needs", async () => {
  const { hooks, async } = harness()
  await hooks.config({ mcp: { capture: {}, other: {} } })
  await hooks.event({ event: { type: "session.created", properties: { info: { id: "ses_root" } } } })
  await hooks.flush()

  assert.deepEqual(envelopes(async)[0]?.mcp_servers, ["capture", "other"])
})
