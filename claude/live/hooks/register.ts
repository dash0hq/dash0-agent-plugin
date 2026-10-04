// SPDX-FileCopyrightText: Copyright 2026 Dash0 Inc.
// SPDX-License-Identifier: Apache-2.0

// The only file that touches `$`: hooks, timers and the MCP call. Everything it
// draws comes from views.ts and everything it computes from model.ts. The
// engine lets `$` reach only functions declared at the top of this file, which
// is why the helpers below are top-level and share module variables.

import { atom, read, update } from 'claude-code'
import type { EngineInterface, McpToolResult, Register } from 'claude-code'

import type { FailedCheck, Snapshot, View } from '../types/index.d.ts'
import { clampInt, investigatePrompt, statusLine, toolErrorMessage } from './model.ts'
import { createMcpSource } from './source.ts'
import { bandView, paneView, VIEWS } from './views.ts'

const PANE = 'dash0-live'
const snapshot = atom({ plugin: 'dash0-live', key: 'snapshot' } as const, null)
const view = atom({ plugin: 'dash0-live', key: 'view' } as const, 'overview')
const isBandHidden = atom({ plugin: 'dash0-live', key: 'isBandHidden' } as const, false)

// Claude Code names the Dash0 server after where it was added: a `claude mcp
// add` entry, a claude.ai connector, or a plugin's manifest. The configured
// name is tried first and the first one that answers is remembered.
const SERVER_CANDIDATES = ['Dash0', 'dash0', 'claude.ai Dash0']
const WINDOW = /^\d{1,3}[mhd]$/
const DATASET = /^[a-zA-Z0-9_-]{1,64}$/
const BAND_MODES = ['always', 'failing', 'off'] as const

type Settings = {
  refreshMs: number
  window: string
  dataset: string
  band: (typeof BAND_MODES)[number]
  mcpServer: string
}

// Module variables reset on a hot reload; the snapshot lives in $.state and
// survives, and the render hooks restart polling when they find it stopped.
let settings: Settings = { refreshMs: 60_000, window: '1h', dataset: 'default', band: 'always', mcpServer: '' }
let server: string | undefined
let poll: { cancel: () => void } | undefined
let isRefreshing = false

function readSettings(options: Record<string, unknown>): Settings {
  const band = String(options.band)
  return {
    refreshMs: clampInt(options.refreshSeconds, 60, 15, 3600) * 1000,
    window: WINDOW.test(String(options.window ?? '')) ? String(options.window) : '1h',
    dataset: DATASET.test(String(options.dataset ?? '')) ? String(options.dataset) : 'default',
    band: (BAND_MODES as readonly string[]).includes(band) ? (band as Settings['band']) : 'always',
    mcpServer: String(options.mcpServer ?? '').trim(),
  }
}

function textOf(result: McpToolResult): string {
  return result.content.map(block => (block.type === 'text' ? block.text : '')).join('\n')
}

async function callDash0($: EngineInterface, tool: string, args: Record<string, unknown>): Promise<string> {
  const candidates = server ? [server] : [...new Set([settings.mcpServer, ...SERVER_CANDIDATES].filter(Boolean))]
  const failures: string[] = []
  for (const name of candidates) {
    let result: McpToolResult
    try {
      result = await $.mcp.call(name, tool, args)
    } catch (error) {
      // Only a call that never reached a server moves on to the next name.
      failures.push(`${name}: ${error instanceof Error ? error.message : String(error)}`)
      continue
    }
    // The server answered, so this is its name even when the tool failed (a
    // 403 on the dataset must surface as such, not as "server not found").
    server = name
    if (result.isError) throw new Error(toolErrorMessage(textOf(result)))
    return textOf(result)
  }
  throw new Error(
    candidates.length === 1 ? toolErrorMessage(failures[0] ?? 'Dash0 MCP server not connected') : `Dash0 MCP server not found (tried ${candidates.join(', ')})`,
  )
}

async function refresh($: EngineInterface): Promise<void> {
  if (isRefreshing) return
  isRefreshing = true
  const now = await $.clock.now()
  let next: Snapshot
  try {
    const source = createMcpSource((tool, args) => callDash0($, tool, args))
    next = await source.fetch({ dataset: settings.dataset, window: settings.window, now })
  } catch (error) {
    const previous = (await read($, snapshot)) ?? undefined
    // Keep the last good data but mark the snapshot failed: the views show the
    // error and the age, never the old counts under a live label.
    next = {
      checks: previous?.checks ?? [],
      threads: previous?.threads ?? [],
      checksUrl: previous?.checksUrl,
      fetchedAt: previous?.fetchedAt ?? now,
      error: error instanceof Error ? error.message.slice(0, 120) : 'unreachable',
    }
  } finally {
    isRefreshing = false
  }
  await update($, snapshot, () => next)
  $.ui.status(statusLine(next))
}

// MCP servers connect after session.start, so the first refresh can miss the
// server; retry briefly instead of showing "not found" for a whole period.
const WARM_UP_MS = [0, 2_000, 4_000, 8_000, 16_000]

/** Fire and forget: pending ops reject when the plugin unloads (session end, hot reload). */
function detach(work: Promise<unknown>): void {
  work.catch(() => {})
}

async function warmUp($: EngineInterface): Promise<void> {
  for (const delay of WARM_UP_MS) {
    if (delay) await $.clock.sleep(delay)
    if (!poll || server) return
    await refresh($)
  }
}

function startPolling($: EngineInterface): void {
  if (poll) return
  poll = $.clock.every(settings.refreshMs, () => detach(refresh($)))
  detach(warmUp($))
}

function stopPolling(): void {
  poll?.cancel()
  poll = undefined
}

async function openPane($: EngineInterface, target?: View): Promise<void> {
  if (target) await update($, view, () => target)
  await $.ui.open({ id: PANE, title: 'Dash0', focus: true, closeOnEscape: true, holdToasts: true, rows: 18 })
  startPolling($)
}

async function investigate($: EngineInterface, check: FailedCheck): Promise<void> {
  // A draft, not a submit: the person reads and sends it themselves.
  await $.prompt.fill({ text: investigatePrompt(check, settings.dataset), mode: 'replace' })
  await $.ui.close({ id: PANE })
}

async function copyLink($: EngineInterface, url: string): Promise<void> {
  const copied = await $.ui.copy({ text: url })
  $.ui.toast(copied.isCopied ? 'Dash0 link copied' : 'Could not copy the link')
}

async function hideBand($: EngineInterface): Promise<void> {
  await update($, isBandHidden, () => true)
}

async function selectView($: EngineInterface, next: View): Promise<void> {
  await update($, view, () => next)
}

export const register: Register = (on, options) => {
  settings = readSettings(options ?? {})

  on('session.start', async ($, e, next) => {
    await $.command
      .register({
        name: 'dash0',
        description: 'Dash0 live insights: failing checks and Agent0 threads',
        argumentHint: '[checks|agent0|refresh|show|hide]',
      })
      .catch(() => {})
    if (settings.band !== 'off') startPolling($)
    return next(e)
  })

  on('command.run', { command: 'dash0' }, async ($, e) => {
    const arg = String(e.args ?? '').trim().toLowerCase()
    if (arg === 'refresh') {
      await refresh($)
      return { text: statusLine((await read($, snapshot)) ?? undefined) ?? 'dash0: no data' }
    }
    if (arg === 'hide' || arg === 'show') {
      await update($, isBandHidden, () => arg === 'hide')
      if (arg === 'show') startPolling($)
      return { text: `Dash0 bar ${arg === 'hide' ? 'hidden' : 'shown'}.` }
    }
    const target = (VIEWS as readonly string[]).includes(arg) ? (arg as View) : undefined
    await openPane($, target)
    // The model reads this row, so it carries counts only, never telemetry text.
    return { text: `Dash0 pane open. ${statusLine((await read($, snapshot)) ?? undefined) ?? ''}`.trim() }
  })

  on('ui.render', { component: 'AbovePrompt' }, async ($, e, next) => {
    if (settings.band === 'off' || e.props.hasSurvey || (await read($, isBandHidden))) return next(e)
    const snap = (await read($, snapshot)) ?? undefined
    const isFailing = snap?.checks.some(c => c.status === 'critical' || c.status === 'degraded') ?? false
    if (settings.band === 'failing' && !isFailing) return next(e)
    if (!poll) startPolling($)
    return bandView(
      $.ui.resolve(e),
      { snap, now: await $.clock.now(), refreshMs: settings.refreshMs, window: settings.window, isRefreshing },
      { open: () => detach(openPane($)), refresh: () => detach(refresh($)), hide: () => detach(hideBand($)) },
    ) as Awaited<ReturnType<typeof next>>
  })

  on('ui.render', { component: 'Pane', requestId: PANE }, async ($, e, next) => {
    if (!poll) startPolling($)
    return paneView(
      $.ui.resolve(e),
      {
        snap: (await read($, snapshot)) ?? undefined,
        now: await $.clock.now(),
        refreshMs: settings.refreshMs,
        window: settings.window,
        isRefreshing,
        view: await read($, view),
        isFocused: e.props.isFocused,
        bodyColumns: e.props.bodyColumns,
        bodyRows: e.props.scroll.bodyRows,
        dataset: settings.dataset,
      },
      {
        setView: v => detach(selectView($, v)),
        refresh: () => detach(refresh($)),
        investigate: check => detach(investigate($, check)),
        copyLink: url => detach(copyLink($, url)),
      },
    ) as Awaited<ReturnType<typeof next>>
  })

  on('ui.close', { id: PANE }, async ($, e, next) => {
    if (settings.band === 'off') stopPolling()
    return next(e)
  })

  on('session.end', async ($, e, next) => {
    stopPolling()
    $.ui.status(undefined)
    return next(e)
  })
}
