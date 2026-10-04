// SPDX-FileCopyrightText: Copyright 2026 Dash0 Inc.
// SPDX-License-Identifier: Apache-2.0

// The InsightSource port and its first adapter. The adapter goes through the
// Dash0 MCP server Claude Code already has connected, so it rides that
// connection's OAuth session and no Dash0 secret ever enters this module. A
// second adapter (the plugin's Go binary, reading the same keychain/config the
// telemetry export uses, answering JSON) can implement the same port.

import { parseFailedChecks, parseThreads, type Snapshot } from './model.ts'

export type FetchInput = { dataset: string; window: string; now: number }

export interface InsightSource {
  fetch(input: FetchInput): Promise<Snapshot>
}

/** Calls one MCP tool and returns its text blocks joined; throws when the tool errors. */
export type McpText = (tool: string, args: Record<string, unknown>) => Promise<string>

export function createMcpSource(call: McpText): InsightSource {
  return {
    async fetch({ dataset, window, now }) {
      const timeRange = { from: `now-${window}`, to: 'now' }
      // Checks are first-class: their failure is the snapshot's error. Agent0
      // threads are best-effort and contribute nothing on failure.
      const [checksText, threadsText] = await Promise.all([
        call('getFailedChecks', { dataset, timeRange, pagination: { limit: 50 } }),
        call('listAgent0Threads', { dataset, pagination: { limit: 10 } }).catch(() => ''),
      ])
      const parsed = parseFailedChecks(checksText)
      return {
        checks: parsed.checks,
        threads: parseThreads(threadsText),
        checksUrl: parsed.url,
        fetchedAt: now,
        error: parsed.parseError,
      }
    },
  }
}
