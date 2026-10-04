// SPDX-FileCopyrightText: Copyright 2026 Dash0 Inc.
// SPDX-License-Identifier: Apache-2.0

// Pure view model for the live bar: no `$`, so `node --test` covers it. The
// Dash0 MCP tools answer LLM-oriented markdown, not JSON, so the parsers below
// are the contract with that output and fail soft: an unrecognised shape yields
// an empty list plus a `parseError`, never a throw.

import type { Agent0Thread, CheckStatus, FailedCheck, Snapshot } from '../types/index.d.ts'

export type { Agent0Thread, CheckStatus, FailedCheck, Snapshot }

export type Counts = { critical: number; degraded: number; resolved: number; unknown: number }

const STATUSES: readonly CheckStatus[] = ['critical', 'degraded', 'resolved', 'unknown']
const SEVERITY_ORDER: Record<CheckStatus, number> = { critical: 0, degraded: 1, unknown: 2, resolved: 3 }

/** Splits a markdown row on unescaped pipes, unescaping `\|` inside cells. */
function cells(line: string): string[] {
  const inner = line.trim().replace(/^\|/, '').replace(/\|$/, '')
  return inner.split(/(?<!\\)\|/).map(cell => cell.trim().replace(/\\\|/g, '|'))
}

const isSeparator = (line: string) => /^\|(\s*:?-+:?\s*\|)+$/.test(line.trim())

export type Table = { header: string[]; rows: Record<string, string>[] }

/** Every markdown table in `text`, header kept so an empty table is still recognisable. */
export function tables(text: string): Table[] {
  const found: Table[] = []
  const lines = text.split('\n')
  const isRow = (k: number) => (lines[k] ?? '').trim().startsWith('|')
  for (let i = 0; i < lines.length - 1; i++) {
    if (!isRow(i) || !isSeparator(lines[i + 1] ?? '')) continue
    const header = cells(lines[i] ?? '')
    const rows: Record<string, string>[] = []
    let j = i + 2
    for (; j < lines.length && isRow(j); j++) {
      const values = cells(lines[j] ?? '')
      rows.push(Object.fromEntries(header.map((key, k) => [key, values[k] ?? ''])))
    }
    found.push({ header, rows })
    i = j - 1
  }
  return found
}

function epoch(value: string | undefined): number | undefined {
  if (!value) return undefined
  const ms = Date.parse(value)
  return Number.isFinite(ms) ? ms : undefined
}

function status(value: string | undefined): CheckStatus {
  const v = (value ?? '').toLowerCase()
  return (STATUSES as readonly string[]).includes(v) ? (v as CheckStatus) : 'unknown'
}

export function parseFailedChecks(text: string): { checks: FailedCheck[]; url?: string; parseError?: string } {
  const all = tables(text)
  const main = all.find(t => t.header.includes('Last Status'))
  const url = /\[View failed checks in Dash0\]\((https:\/\/[^)\s]+)\)/.exec(text)?.[1]
  // No results is a header-only table ("Showing 0 failed checks"); a missing
  // table means the tool's output format changed.
  if (!main) return { checks: [], url, parseError: 'unrecognised failed-checks response' }
  const labelTable = all.find(t => t.header[0] === 'dash0.issue.identifier' && !t.header.includes('Last Status'))
  const labels = new Map((labelTable?.rows ?? []).map(row => [row['dash0.issue.identifier'], row] as const))
  const checks = main.rows.map((row): FailedCheck => {
    const id = row['dash0.issue.identifier'] ?? ''
    const label = labels.get(id)
    return {
      id,
      status: status(row['Last Status']),
      summary: row['dash0.failed_check.summary'] || row['Check Rule Name'] || id,
      rule: row['Check Rule Name'] ?? '',
      startedAt: epoch(row['Start Time']),
      endedAt: row['End Time'] === 'ongoing' ? undefined : epoch(row['End Time']),
      // The tool repeats a service once per referenced metric ("agents, agents").
      services: [...new Set((row['Referenced Services'] ?? '').split(',').map(s => s.trim()).filter(Boolean))],
      owner: label?.owner || undefined,
      priority: label?.priority || undefined,
    }
  })
  return { checks: sortChecks(checks), url }
}

export function parseThreads(text: string): Agent0Thread[] {
  const rows = tables(text).find(t => t.header.includes('Thread ID'))?.rows ?? []
  return rows.flatMap(row => {
    const id = row['Thread ID']
    return id ? [{ id, name: row['Name'] || 'Untitled', updatedAt: epoch(row['Updated']) }] : []
  })
}

export function sortChecks(checks: readonly FailedCheck[]): FailedCheck[] {
  return [...checks].sort(
    (a, b) => SEVERITY_ORDER[a.status] - SEVERITY_ORDER[b.status] || (b.startedAt ?? 0) - (a.startedAt ?? 0),
  )
}

export function counts(checks: readonly FailedCheck[]): Counts {
  const c: Counts = { critical: 0, degraded: 0, resolved: 0, unknown: 0 }
  for (const check of checks) c[check.status] += 1
  return c
}

export const isActive = (check: FailedCheck) => check.status === 'critical' || check.status === 'degraded'

/** "12m", "3h", "2d": the age of `at` relative to `now`. */
export function age(at: number | undefined, now: number): string {
  if (at === undefined) return '?'
  const s = Math.max(0, Math.round((now - at) / 1000))
  if (s < 60) return `${s}s`
  if (s < 3600) return `${Math.round(s / 60)}m`
  if (s < 86400) return `${Math.round(s / 3600)}h`
  return `${Math.round(s / 86400)}d`
}

/** One status-line string; undefined clears the entry. */
export function statusLine(snap: Snapshot | undefined): string | undefined {
  if (!snap) return undefined
  if (snap.error) return 'dash0: offline'
  const c = counts(snap.checks)
  if (c.critical + c.degraded === 0) return 'dash0: all clear'
  return ['dash0:', c.critical && `${c.critical} critical`, c.degraded && `${c.degraded} degraded`]
    .filter(Boolean)
    .join(' ')
}

/** Data older than three refresh periods is shown as stale, never as live. */
export const isStale = (snap: Snapshot, now: number, refreshMs: number) => now - snap.fetchedAt > refreshMs * 3

/**
 * The draft the "Investigate" button puts in the prompt box. It names the check
 * by id and rule only: summaries are telemetry-derived text, and the person
 * reviews the draft before sending it.
 */
export function investigatePrompt(check: FailedCheck, dataset: string): string {
  return (
    `Investigate the Dash0 failed check ${check.id} (rule "${check.rule}") in dataset "${dataset}". ` +
    `Use the Dash0 MCP tools: start with getFailedCheckDetails, then correlate with recent changes in this repository.`
  )
}

/**
 * A tool error as the person should read it: the API's own `message` when the
 * text carries a JSON error body (`HTTP 403: {"error":{"message":"…"}}`).
 */
export function toolErrorMessage(text: string): string {
  const message = /"message"\s*:\s*"((?:[^"\\]|\\.)*)"/.exec(text)?.[1]
  const status = /HTTP (\d{3})/.exec(text)?.[1]
  const clean = (message ?? text).replace(/^Error:\s*/, '').trim()
  return (status && message ? `${status}: ${clean}` : clean).slice(0, 160) || 'tool failed'
}

export function clampInt(value: unknown, fallback: number, lo: number, hi: number): number {
  const n = Number(value)
  return Number.isFinite(n) && String(value ?? '').trim() !== '' ? Math.min(Math.max(Math.round(n), lo), hi) : fallback
}
