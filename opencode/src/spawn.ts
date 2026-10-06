// SPDX-FileCopyrightText: Copyright 2026 Dash0 Inc.
// SPDX-License-Identifier: Apache-2.0

// The transport: one wrapper process per canonical event, never awaited.
//
// Telemetry must not add latency to a turn, so nothing here returns a promise
// the caller could block on, and every failure mode of the child — a missing
// wrapper, a non-zero exit, a closed pipe — is swallowed. An unhandled `error`
// on a ChildProcess would take the whole OpenCode process down with it.

import { spawn, spawnSync } from "node:child_process"

/** SpawnOptions configures one wrapper invocation. */
export interface SpawnOptions {
  /** Working directory, which is where the wrapper looks for a project config. */
  cwd: string
  /**
   * Called with the child's stderr once it exits. The wrapper writes the
   * pipeline's user-facing messages there; without this the stream is not even
   * opened, so the child cannot block on a full pipe.
   */
  onStderr?: (stderr: string) => void
}

/**
 * spawnEvent starts the wrapper. stdin is closed right after the payload — a
 * child whose stdin stays open waits forever on the read.
 *
 * The returned promise resolves when the child is done and never rejects. It
 * exists so the caller can order one spawn after another; awaiting it in a hook
 * handler would put telemetry on the turn's critical path.
 */
export function spawnEvent(command: string, payload: string, options: SpawnOptions): Promise<void> {
  const { promise, resolve } = Promise.withResolvers<void>()
  try {
    const wantsStderr = options.onStderr !== undefined
    const child = spawn(command, [], {
      cwd: options.cwd,
      stdio: ["pipe", "ignore", wantsStderr ? "pipe" : "ignore"],
    })

    let collected = ""
    child.on("error", () => resolve())
    child.stdin?.on("error", () => {})
    child.stdin?.end(payload)

    if (wantsStderr && child.stderr) {
      child.stderr.setEncoding("utf8")
      child.stderr.on("error", () => {})
      child.stderr.on("data", (chunk: string) => {
        collected += chunk
      })
    }

    child.on("close", () => {
      try {
        if (wantsStderr) options.onStderr?.(collected)
      } catch {
        // A failed notification must not surface as an unhandled rejection.
      }
      resolve()
    })

    // OpenCode should be free to exit while a wrapper is still uploading.
    child.unref()
  } catch {
    // Telemetry is never worth an exception in the host process.
    resolve()
  }
  return promise
}

/**
 * spawnEventSync runs the wrapper to completion. Only shutdown uses it: a
 * fire-and-forget child started while the process is exiting is killed before
 * it can write anything.
 */
export function spawnEventSync(command: string, payload: string, options: SpawnOptions & { timeoutMs?: number }): void {
  try {
    spawnSync(command, [], {
      cwd: options.cwd,
      input: payload,
      stdio: ["pipe", "ignore", "ignore"],
      timeout: options.timeoutMs ?? 5000,
    })
  } catch {
    // Same rule as above: shutdown continues regardless.
  }
}
