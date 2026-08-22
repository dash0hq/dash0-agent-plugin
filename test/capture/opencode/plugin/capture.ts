// SPDX-FileCopyrightText: Copyright 2026 Dash0 Inc.
// SPDX-License-Identifier: Apache-2.0
//
// Throwaway capture plugin: appends every OpenCode bus event and every Hooks
// callback to a JSONL file so the `internal/source/opencode` normalizer can be
// written against observed payloads instead of inferred ones. It is not the
// real plugin and is never shipped.

import { appendFileSync } from "node:fs"
import type { Plugin } from "@opencode-ai/plugin"

const OUT = process.env.DASH0_OPENCODE_CAPTURE_FILE ?? "/tmp/opencode-capture.jsonl"

let seq = 0

function record(kind: string, name: string, payload: unknown): void {
  try {
    appendFileSync(OUT, JSON.stringify({ seq: seq++, ts: Date.now(), kind, name, payload }) + "\n")
  } catch {
    // Capture must never break the session it is observing.
  }
}

export const CapturePlugin: Plugin = async (input) => {
  record("plugin", "init", {
    directory: input.directory,
    worktree: input.worktree,
    project: input.project,
    serverUrl: String(input.serverUrl),
  })

  return {
    event: async ({ event }) => record("event", (event as { type?: string }).type ?? "unknown", event),
    "chat.message": async (i, o) => record("hook", "chat.message", { input: i, output: o }),
    "chat.params": async (i, o) => record("hook", "chat.params", { input: i, output: o }),
    "chat.headers": async (i, o) => record("hook", "chat.headers", { input: i, output: o }),
    "permission.ask": async (i, o) => record("hook", "permission.ask", { input: i, output: o }),
    "command.execute.before": async (i, o) => record("hook", "command.execute.before", { input: i, output: o }),
    "tool.execute.before": async (i, o) => record("hook", "tool.execute.before", { input: i, output: o }),
    "tool.execute.after": async (i, o) => record("hook", "tool.execute.after", { input: i, output: o }),
  }
}
