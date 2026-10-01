// SPDX-FileCopyrightText: Copyright 2026 Dash0 Inc.
// SPDX-License-Identifier: Apache-2.0

import assert from "node:assert/strict"
import { mkdtempSync, readFileSync, rmSync, writeFileSync } from "node:fs"
import { tmpdir } from "node:os"
import { join } from "node:path"
import { after, test } from "node:test"
import { setTimeout as sleep } from "node:timers/promises"
import { spawnEvent, spawnEventSync } from "../src/spawn.ts"

const dir = mkdtempSync(join(tmpdir(), "dash0-opencode-spawn-"))
after(() => rmSync(dir, { recursive: true, force: true }))

function script(name: string, body: string): string {
  const path = join(dir, name)
  writeFileSync(path, `#!/usr/bin/env bash\n${body}\n`, { mode: 0o755 })
  return path
}

test("the caller is not held up by a slow child", () => {
  const marker = join(dir, "slow.txt")
  const wrapper = script("slow.sh", `cat > "${marker}"\nsleep 2\nexit 3`)

  const before = process.hrtime.bigint()
  spawnEvent(wrapper, '{"hook":"Stop"}\n', { cwd: dir })
  const elapsedMs = Number(process.hrtime.bigint() - before) / 1e6

  assert.ok(elapsedMs < 500, `spawnEvent took ${elapsedMs}ms`)
})

test("a non-zero exit is swallowed and the payload still arrives on stdin", async () => {
  const marker = join(dir, "failing.txt")
  const wrapper = script("failing.sh", `cat > "${marker}"\nexit 3`)

  spawnEvent(wrapper, '{"hook":"Stop"}\n', { cwd: dir })

  for (let attempt = 0; attempt < 40; attempt++) {
    try {
      assert.equal(readFileSync(marker, "utf8"), '{"hook":"Stop"}\n')
      return
    } catch {
      await sleep(50)
    }
  }
  assert.fail("the child never read the payload")
})

// A child whose stdin is left open blocks on the read forever, which would
// leak one process per event for the life of the session.
test("stdin is closed after the payload, so a reading child exits on its own", async () => {
  const marker = join(dir, "drain.txt")
  const wrapper = script("drain.sh", `cat > "${marker}"\nprintf 'done\\n' >> "${marker}"`)

  spawnEvent(wrapper, '{"hook":"Stop"}\n', { cwd: dir })

  for (let attempt = 0; attempt < 40; attempt++) {
    try {
      if (readFileSync(marker, "utf8").endsWith("done\n")) return
    } catch {
      // The child has not written yet.
    }
    await sleep(50)
  }
  assert.fail("the child never saw end-of-input")
})

test("a missing wrapper is swallowed rather than thrown", () => {
  assert.doesNotThrow(() => spawnEvent(join(dir, "does-not-exist.sh"), "{}\n", { cwd: dir }))
})

test("stderr is handed back when a caller asks for it", async () => {
  const wrapper = script("chatty.sh", `cat > /dev/null\nprintf 'dash0: connected\\n' >&2`)

  const stderr = await new Promise<string>((resolve) => {
    spawnEvent(wrapper, "{}\n", { cwd: dir, onStderr: resolve })
  })
  assert.equal(stderr, "dash0: connected\n")
})

test("the blocking spawn used at shutdown returns after the child is done", () => {
  const marker = join(dir, "shutdown.txt")
  const wrapper = script("shutdown.sh", `cat > "${marker}"`)

  spawnEventSync(wrapper, '{"hook":"SessionEnd"}\n', { cwd: dir })

  assert.equal(readFileSync(marker, "utf8"), '{"hook":"SessionEnd"}\n')
})
