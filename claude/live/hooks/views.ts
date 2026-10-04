// SPDX-FileCopyrightText: Copyright 2026 Dash0 Inc.
// SPDX-License-Identifier: Apache-2.0

// Pure tree builders: `ui` is the surface's element table and every handler is
// passed in, so these run in plain Node against stub elements. Only Box, Text
// and Button are used — the three every surface (terminal, desktop, vscode,
// mobile) has — so no tree is refused for an element a surface lacks.

import type { CheckStatus, FailedCheck, Snapshot, View } from '../types/index.d.ts'
import { age, counts, isActive, isStale } from './model.ts'

import type { BoxProps, ButtonProps, TextProps } from 'claude-code'

// Only the props side is typed: the engine's element constructors are wider.
type El<P> = (props: P & { key?: string; children?: any }) => any
export type Ui = { Box: El<BoxProps>; Text: El<TextProps>; Button: El<ButtonProps> }

export type { View }
export const VIEWS: readonly View[] = ['overview', 'checks', 'agent0']

// Severity colours value-extracted from the Dash0 design tokens (red-600,
// amber-500, emerald-500), the same values agent0-responder renders.
export const COLOR: Record<CheckStatus | 'brand', string> = {
  critical: '#e33238',
  degraded: '#f59e0b',
  resolved: '#48b78c',
  unknown: 'gray',
  brand: '#FF8A67',
}
const DOT = '●'

export type BandHandlers = { open: () => void; refresh: () => void; hide: () => void }

export type BandInput = {
  snap: Snapshot | undefined
  now: number
  refreshMs: number
  window: string
  isRefreshing: boolean
}

/** The one-row bar above the prompt. */
export function bandView(ui: Ui, input: BandInput, on: BandHandlers): unknown {
  const { Box, Text, Button } = ui
  // Text takes no flex props: each part sits in a Box, and only the summary
  // (or the error) may shrink, so the counts and the age never wrap.
  const fixed = (key: string, props: TextProps & { children: string }) => Box({ key, flexShrink: 0, children: Text(props) })
  const shrinking = (key: string, props: TextProps & { children: string }) =>
    Box({ key, flexShrink: 1, overflow: 'hidden', children: Text({ ...props, wrap: 'truncate-end' }) })
  const parts: unknown[] = [fixed('brand', { bold: true, color: COLOR.brand, children: 'Dash0 ' })]
  const { snap } = input

  if (!snap) {
    parts.push(fixed('wait', { dimColor: true, children: input.isRefreshing ? 'loading…' : 'not loaded' }))
  } else if (snap.error) {
    parts.push(shrinking('err', { color: COLOR.critical, children: `offline: ${snap.error}` }))
  } else {
    const c = counts(snap.checks)
    if (c.critical + c.degraded === 0) {
      parts.push(fixed('clear', { color: COLOR.resolved, children: `${DOT} all clear` }))
    } else {
      if (c.critical) parts.push(fixed('crit', { color: COLOR.critical, children: `${DOT} ${c.critical} critical ` }))
      if (c.degraded) parts.push(fixed('deg', { color: COLOR.degraded, children: `${DOT} ${c.degraded} degraded ` }))
    }
    if (c.resolved) parts.push(fixed('res', { dimColor: true, children: ` ${c.resolved} resolved` }))
    const top = snap.checks.find(isActive)
    if (top) parts.push(shrinking('top', { dimColor: true, children: ` · ${top.summary}` }))
    const stale = isStale(snap, input.now, input.refreshMs)
    parts.push(
      fixed('age', {
        dimColor: !stale,
        color: stale ? COLOR.degraded : undefined,
        children: ` · ${input.window} · ${stale ? 'stale ' : ''}${age(snap.fetchedAt, input.now)} ago`,
      }),
    )
  }

  return Box({
    flexDirection: 'row',
    justifyContent: 'space-between',
    gap: 1,
    children: [
      Box({ key: 'facts', flexDirection: 'row', flexShrink: 1, overflow: 'hidden', children: parts }),
      Box({
        key: 'actions',
        flexDirection: 'row',
        flexShrink: 0,
        gap: 1,
        children: [
          Button({ key: 'band-open', label: 'Open', plain: true, onPress: on.open }),
          Button({ key: 'band-refresh', label: '↻', plain: true, onPress: on.refresh }),
          Button({ key: 'band-hide', label: 'Hide', plain: true, dimColor: true, onPress: on.hide }),
        ],
      }),
    ],
  })
}

export type PaneHandlers = {
  setView: (view: View) => void
  refresh: () => void
  investigate: (check: FailedCheck) => void
  copyLink: (url: string) => void
}

export type PaneInput = BandInput & {
  view: View
  isFocused: boolean
  bodyColumns: number
  bodyRows: number
  dataset: string
}

function tabs(ui: Ui, view: View, on: PaneHandlers): unknown {
  const labels: Record<View, string> = { overview: 'Overview (1)', checks: 'Checks (2)', agent0: 'Agent0 (3)' }
  return ui.Box({
    key: 'tabs',
    flexDirection: 'row',
    // A docked pane can be narrower than the four tabs: wrap, never clip Refresh.
    flexWrap: 'wrap',
    columnGap: 1,
    children: [
      ...VIEWS.map((v, i) =>
        ui.Button({
          key: `tab-${v}`,
          label: labels[v],
          hotkey: String(i + 1),
          variant: v === view ? 'primary' : 'secondary',
          onPress: () => on.setView(v),
        }),
      ),
      ui.Button({ key: 'refresh', label: 'Refresh (r)', hotkey: 'r', onPress: on.refresh }),
    ],
  })
}

function checkRow(ui: Ui, check: FailedCheck, now: number, on: PaneHandlers, index: number, withActions: boolean): unknown {
  const { Box, Text, Button } = ui
  const meta = [
    check.endedAt === undefined ? `failing ${age(check.startedAt, now)}` : `resolved ${age(check.endedAt, now)} ago`,
    check.services.length ? check.services.join(', ') : undefined,
    check.owner && `owner ${check.owner}`,
    check.priority,
  ].filter(Boolean)
  return Box({
    key: `check-${check.id}`,
    flexDirection: 'column',
    children: [
      Box({
        key: 'head',
        flexDirection: 'row',
        children: [
          Text({ key: 'dot', color: COLOR[check.status], children: `${DOT} ` }),
          Text({ key: 'sum', bold: isActive(check), wrap: 'truncate-end', children: check.summary }),
        ],
      }),
      Box({
        key: 'meta',
        flexDirection: 'row',
        paddingLeft: 2,
        gap: 1,
        children: [
          Text({ key: 'm', dimColor: true, wrap: 'truncate-end', children: meta.join(' · ') }),
          ...(withActions
            ? [
                Button({
                  key: `investigate-${index}`,
                  label: 'Investigate',
                  plain: true,
                  onPress: () => on.investigate(check),
                }),
              ]
            : []),
        ],
      }),
    ],
  })
}

function overview(ui: Ui, input: PaneInput, on: PaneHandlers): unknown[] {
  const { Text, Button, Box } = ui
  const snap = input.snap!
  const c = counts(snap.checks)
  const active = snap.checks.filter(isActive)
  const rows: unknown[] = [
    Box({
      key: 'kpis',
      flexDirection: 'row',
      gap: 2,
      children: [
        Text({ key: 'k-crit', color: COLOR.critical, bold: true, children: `${c.critical} critical` }),
        Text({ key: 'k-deg', color: COLOR.degraded, bold: true, children: `${c.degraded} degraded` }),
        Text({ key: 'k-res', color: COLOR.resolved, children: `${c.resolved} resolved` }),
        Text({ key: 'k-thr', dimColor: true, children: `${snap.threads.length} Agent0 threads` }),
      ],
    }),
  ]
  if (active.length === 0) rows.push(Text({ key: 'clear', color: COLOR.resolved, children: 'Nothing is failing right now.' }))
  active.slice(0, 3).forEach((check, i) => rows.push(checkRow(ui, check, input.now, on, i, true)))
  if (active.length > 3) rows.push(Text({ key: 'more', dimColor: true, children: `+${active.length - 3} more · Checks (2)` }))
  if (snap.checksUrl) {
    rows.push(Button({ key: 'copy-checks', label: 'Copy Dash0 link', plain: true, onPress: () => on.copyLink(snap.checksUrl!) }))
  }
  return rows
}

function checksList(ui: Ui, input: PaneInput, on: PaneHandlers): unknown[] {
  const checks = input.snap!.checks
  if (checks.length === 0) return [ui.Text({ key: 'none', dimColor: true, children: `No failed checks in the last ${input.window}.` })]
  return checks.map((check, i) => checkRow(ui, check, input.now, on, i, isActive(check)))
}

function agent0(ui: Ui, input: PaneInput): unknown[] {
  const threads = input.snap!.threads
  if (threads.length === 0) return [ui.Text({ key: 'none', dimColor: true, children: 'No recent Agent0 threads.' })]
  return threads.map(thread =>
    ui.Box({
      key: `thread-${thread.id}`,
      flexDirection: 'row',
      gap: 1,
      children: [
        ui.Text({ key: 'age', dimColor: true, children: age(thread.updatedAt, input.now).padStart(4) }),
        ui.Text({ key: 'name', wrap: 'truncate-end', children: thread.name }),
      ],
    }),
  )
}

export function paneView(ui: Ui, input: PaneInput, on: PaneHandlers): unknown {
  const { Box, Text } = ui
  const children: unknown[] = [tabs(ui, input.view, on)]
  if (!input.isFocused) {
    children.push(Text({ key: 'focus-hint', dimColor: true, children: 'Click the pane (or ctrl+x tab) for keys · /dash0 checks|agent0|refresh' }))
  }
  if (!input.snap) {
    children.push(Text({ key: 'wait', dimColor: true, children: input.isRefreshing ? 'Loading from Dash0…' : 'No data yet.' }))
  } else if (input.snap.error) {
    children.push(Text({ key: 'err', color: COLOR.critical, children: `Could not read Dash0: ${input.snap.error}` }))
    children.push(Text({ key: 'err-hint', dimColor: true, children: 'Check /mcp: the Dash0 server must be connected and signed in.' }))
  } else {
    const body =
      input.view === 'checks' ? checksList(ui, input, on) : input.view === 'agent0' ? agent0(ui, input) : overview(ui, input, on)
    children.push(Box({ key: `body-${input.view}`, flexDirection: 'column', children: body }))
    children.push(
      Text({
        key: 'footer',
        dimColor: true,
        children: `dataset ${input.dataset} · last ${input.window} · updated ${age(input.snap.fetchedAt, input.now)} ago`,
      }),
    )
  }
  return Box({ flexDirection: 'column', paddingX: 1, gap: 1, children })
}
