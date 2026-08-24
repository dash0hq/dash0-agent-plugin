// SPDX-FileCopyrightText: Copyright 2026 Dash0 Inc.
// SPDX-License-Identifier: Apache-2.0

// The Dash0 plugin for OpenCode.
//
// OpenCode has no hook mechanism that can spawn a process, so this plugin is
// the only runtime-side piece Dash0 ships in TypeScript. It filters the event
// bus and writes an envelope to the stdin of opencode-on-event.sh, which owns
// configuration, secrets, and the binary download:
//
//   opencode event bus → this plugin → opencode-on-event.sh
//     → opencode-on-event binary → OTLP
//
// It parses no config, resolves no secrets, downloads nothing, and knows
// nothing about spans.

import { accessSync, constants } from "node:fs"
import { homedir } from "node:os"
import { fileURLToPath } from "node:url"
import type { Plugin } from "@opencode-ai/plugin"
import { spawnEvent, spawnEventSync } from "./spawn.ts"
import { Translator } from "./translate.ts"

/** Toast is the notifier the session-start message is rendered through. */
export type Toast = (message: string) => void

/** Deps is what the hooks need from the host, injected so they can be faked. */
export interface Deps {
  cwd: string
  wrapper?: string
  toast?: Toast
  spawnAsync?: typeof spawnEvent
  spawnBlocking?: typeof spawnEventSync
  /**
   * Where the shutdown flush hangs off. Defaults to the host process exiting;
   * a test passes its own so building hooks does not leak a process listener.
   */
  registerShutdown?: (flush: () => void) => void
}

/** Dash0Hooks is the subset of OpenCode's `Hooks` this plugin implements. */
export interface Dash0Hooks {
  event: (input: { event: unknown }) => Promise<void>
  config: (config: unknown) => Promise<void>
  "chat.message": (input: unknown, output: unknown) => Promise<void>
  dispose: () => Promise<void>
  /** Resolves once every wrapper spawned so far has finished. For tests. */
  flush: () => Promise<void>
  translator: Translator
}

// The pipeline prefixes every user-facing message with this, which is what
// separates them from the wrapper's own diagnostics on the same stream.
const MESSAGE_PREFIX = "dash0: "

// A user who has not configured an endpoint has not opted in to being told
// about it, so `pipeline.Process`'s not-active message is the one prefixed line
// that is never rendered.
const NOT_CONFIGURED_TEXT = "telemetry is not active"

const WRAPPER_NAME = "opencode-on-event.sh"

// How long shutdown waits for the queued wrappers. A hung child must not hold
// the OpenCode process open.
const DRAIN_TIMEOUT_MS = 2000

/**
 * resolveWrapper finds the wrapper next to the installed plugin, falling back
 * to the install script's fixed location. Returns undefined when the plugin is
 * loaded without its wrapper, in which case the plugin does nothing at all.
 */
export function resolveWrapper(): string | undefined {
  const candidates = [
    process.env.DASH0_OPENCODE_ON_EVENT,
    fileURLToPath(new URL(`./${WRAPPER_NAME}`, import.meta.url)),
    fileURLToPath(new URL(`../${WRAPPER_NAME}`, import.meta.url)),
    `${homedir()}/.config/opencode/dash0-agent-plugin/${WRAPPER_NAME}`,
  ]
  for (const candidate of candidates) {
    if (!candidate) continue
    try {
      accessSync(candidate, constants.X_OK)
      return candidate
    } catch {
      // Try the next location.
    }
  }
  return undefined
}

/**
 * createHooks builds the hook set. Every handler swallows its own failures, so
 * an event OpenCode reports in a shape this version does not recognize costs
 * that one event and nothing more.
 */
export function createHooks(deps: Deps): Dash0Hooks {
  const translator = new Translator(deps.cwd)
  const spawnAsync = deps.spawnAsync ?? spawnEvent
  const spawnBlocking = deps.spawnBlocking ?? spawnEventSync
  let shutdownDone = false
  // The pipeline is order-sensitive in both directions: Stop clears the turn's
  // trace context, so a tool event that overtakes it loses its parent, and
  // SubagentStart snapshots that context, so it has to land while it is live.
  // Two concurrent processes give no such guarantee, so the wrappers run one
  // after another. Nothing awaits this chain from a hook handler.
  let inFlight: Promise<unknown> = Promise.resolve()

  function send(canonical: string, envelope: unknown): void {
    if (!deps.wrapper) return
    // Only the session-start message is ever rendered, so only that spawn pays
    // for a stderr pipe.
    const onStderr =
      canonical === "SessionStart" && deps.toast
        ? (stderr: string) => {
            try {
              for (const line of stderr.split("\n")) {
                if (!line.startsWith(MESSAGE_PREFIX)) continue
                if (line.includes(NOT_CONFIGURED_TEXT)) continue
                deps.toast?.(line)
              }
            } catch {
              // There is no TUI to notify, which is the headless case.
            }
          }
        : undefined
    const wrapper = deps.wrapper
    const payload = JSON.stringify(envelope) + "\n"
    inFlight = inFlight.then(() => spawnAsync(wrapper, payload, { cwd: deps.cwd, onStderr })).catch(() => {})
  }

  function shutdown(): void {
    if (shutdownDone) return
    shutdownDone = true
    if (!deps.wrapper) return
    for (const { envelope } of translator.shutdown()) {
      // Blocking, unlike every other event: a child started as the host exits
      // is killed before it can free the session's scratch directory.
      spawnBlocking(deps.wrapper, JSON.stringify(envelope) + "\n", { cwd: deps.cwd })
    }
  }

  const registerShutdown = deps.registerShutdown ?? ((flush: () => void) => void process.once("exit", flush))
  registerShutdown(shutdown)

  return {
    translator,
    flush: async () => void (await inFlight),
    event: async ({ event }) => {
      try {
        const forwarded = translator.event(event)
        if (forwarded) send(forwarded.canonical, forwarded.envelope)
      } catch {
        // Drop this event only.
      }
    },
    config: async (config) => {
      try {
        const mcp = (config as { mcp?: Record<string, unknown> } | undefined)?.mcp
        if (mcp) translator.setMcpServers(Object.keys(mcp))
      } catch {
        // Without the server keys an MCP tool keeps OpenCode's flat name.
      }
    },
    "chat.message": async (input, output) => {
      try {
        const forwarded = translator.chatMessage(input, output)
        if (forwarded) send(forwarded.canonical, forwarded.envelope)
      } catch {
        // Drop this event only.
      }
    },
    dispose: async () => {
      // Give the queued wrappers a bounded chance to land before SessionEnd
      // removes the scratch directory they write into.
      await Promise.race([inFlight, new Promise((done) => setTimeout(done, DRAIN_TIMEOUT_MS).unref?.())])
      shutdown()
    },
  }
}

export const Dash0Plugin: Plugin = async ({ client, directory, worktree }) => {
  const hooks = createHooks({
    cwd: directory || worktree,
    wrapper: resolveWrapper(),
    toast: (message) => {
      // Fails in headless mode (`opencode run`), where there is no TUI to
      // notify. The session must not notice.
      void Promise.resolve(client.tui.showToast({ body: { message, variant: "info" } })).catch(() => {})
    },
  })

  return {
    event: hooks.event,
    config: hooks.config,
    "chat.message": hooks["chat.message"],
    dispose: hooks.dispose,
  }
}

export default Dash0Plugin
