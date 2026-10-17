import assert from 'node:assert/strict'
import { test } from 'node:test'

import { age, clampInt, counts, investigatePrompt, isStale, parseFailedChecks, parseThreads, statusLine, tables, toolErrorMessage } from '../hooks/model.ts'
import { createMcpSource } from '../hooks/source.ts'
import { readFileSync } from 'node:fs'

import { FAILED_CHECKS, FAILED_CHECKS_EMPTY, FORBIDDEN_DATASET, THREADS } from './fixtures.ts'

test('parses the failed-checks table and joins the labels table by issue id', () => {
  const { checks, url, parseError } = parseFailedChecks(FAILED_CHECKS)
  assert.equal(parseError, undefined)
  assert.equal(checks.length, 3)
  const [first] = checks
  assert.equal(first.status, 'critical', 'active checks sort first')
  assert.equal(first.id, '1923268341316241030')
  assert.equal(first.endedAt, undefined, '"ongoing" means still failing')
  assert.equal(first.owner, 'gtm')
  assert.equal(first.priority, 'p3')
  assert.deepEqual(checks[1].services, ['agents'])
  assert.equal(url, 'https://app.dash0.com/goto/alerting/failed-checks?org=dash0-production&dataset=default&from=now-1h&to=now')
})

test("the server's header-only empty answer is zero checks, an unknown shape is a parse error", () => {
  const empty = parseFailedChecks(FAILED_CHECKS_EMPTY)
  assert.deepEqual(empty.checks, [])
  assert.equal(empty.parseError, undefined)
  assert.equal(parseFailedChecks('<html>502</html>').parseError, 'unrecognised failed-checks response')
})

test('40 live rows over 24h parse completely and consistently', () => {
  const blocks = JSON.parse(readFileSync(new URL('./live/failed-checks-24h.json', import.meta.url), 'utf8'))
  const text = blocks.map((b: { type: string; text?: string }) => (b.type === 'text' ? b.text : '')).join('\n')
  const { checks, parseError, url } = parseFailedChecks(text)
  assert.equal(parseError, undefined)
  assert.equal(checks.length, 40)
  assert.ok(url?.includes('from=now-24h'))
  assert.equal(new Set(checks.map(c => c.id)).size, 40, 'ids unique')
  assert.ok(checks.every(c => c.startedAt !== undefined && c.status !== 'unknown' && c.owner))
  // Still failing (no end) is exactly the active set.
  assert.deepEqual(checks.filter(c => c.endedAt === undefined).map(c => c.id).sort(), checks.filter(c => c.status !== 'resolved').map(c => c.id).sort())
  assert.ok(checks.every(c => new Set(c.services).size === c.services.length), 'services deduplicated')
  const order = checks.map(c => c.status)
  assert.deepEqual(order, [...order].sort((a, b) => ['critical', 'degraded', 'unknown', 'resolved'].indexOf(a) - ['critical', 'degraded', 'unknown', 'resolved'].indexOf(b)))
})

test('tool errors read as the API message', () => {
  assert.equal(toolErrorMessage(FORBIDDEN_DATASET), "403: access to dataset 'no-such-dataset-xyz' is not permitted")
  assert.equal(toolErrorMessage('Error: connection refused'), 'connection refused')
  assert.equal(toolErrorMessage(''), 'tool failed')
})

test('unknown statuses map to unknown, not to healthy', () => {
  const text = FAILED_CHECKS.replace('| critical    |', '| exploded    |')
  assert.equal(parseFailedChecks(text).checks.find(c => c.id === '1923268341316241030')?.status, 'unknown')
})

test('escaped pipes stay inside their cell', () => {
  const [table] = tables('| a | b |\n| :- | :- |\n| x \\| y | z |')
  assert.deepEqual(table.rows, [{ a: 'x | y', b: 'z' }])
})

test('parses Agent0 threads', () => {
  const threads = parseThreads(THREADS)
  assert.equal(threads.length, 3)
  assert.equal(threads[0].name, 'Profiling tools availability')
  assert.equal(threads[0].updatedAt, Date.parse('2026-10-04T07:13:56.600Z'))
})

test('counts, status line and staleness', () => {
  const { checks } = parseFailedChecks(FAILED_CHECKS)
  assert.deepEqual(counts(checks), { critical: 1, degraded: 0, resolved: 2, unknown: 0 })
  const snap = { checks, threads: [], fetchedAt: 1_000 }
  assert.equal(statusLine(snap), 'dash0: 1 critical')
  assert.equal(statusLine({ ...snap, checks: [] }), 'dash0: all clear')
  assert.equal(statusLine({ ...snap, error: 'x' }), 'dash0: offline')
  assert.equal(isStale(snap, 1_000 + 180_000, 60_000), false)
  assert.equal(isStale(snap, 1_000 + 180_001, 60_000), true)
})

test('age and option clamping', () => {
  assert.equal(age(0, 59_000), '59s')
  assert.equal(age(0, 12 * 60_000), '12m')
  assert.equal(age(undefined, 0), '?')
  assert.equal(clampInt('5', 60, 15, 3600), 15)
  assert.equal(clampInt('', 60, 15, 3600), 60)
  assert.equal(clampInt('abc', 60, 15, 3600), 60)
})

test('the investigate draft names the check, not its telemetry-derived summary', () => {
  const [check] = parseFailedChecks(FAILED_CHECKS).checks
  const draft = investigatePrompt(check, 'default')
  assert.match(draft, /1923268341316241030/)
  assert.doesNotMatch(draft, /freeable memory for RDS instance/)
})

test('the MCP source treats checks as first-class and threads as best-effort', async () => {
  const calls: string[] = []
  const ok = createMcpSource(async tool => {
    calls.push(tool)
    if (tool === 'listAgent0Threads') throw new Error('boom')
    return FAILED_CHECKS
  })
  const snap = await ok.fetch({ dataset: 'default', window: '1h', now: 42 })
  assert.deepEqual(calls.sort(), ['getFailedChecks', 'listAgent0Threads'])
  assert.equal(snap.checks.length, 3)
  assert.deepEqual(snap.threads, [])
  assert.equal(snap.fetchedAt, 42)

  const broken = createMcpSource(async tool => {
    if (tool === 'getFailedChecks') throw new Error('not connected')
    return THREADS
  })
  await assert.rejects(broken.fetch({ dataset: 'default', window: '1h', now: 0 }), /not connected/)
})

test('short rows read as empty cells, and threads without an id are dropped', () => {
  const [table] = tables('| a | b |\n| - | - |\n| x |')
  assert.deepEqual(table?.rows, [{ a: 'x', b: '' }])
  assert.deepEqual(parseThreads('| Thread ID | Name | Updated |\n| - | - | - |\n| | orphan | |\n| t1 | | |').map(t => [t.id, t.name]), [['t1', 'Untitled']])
})
