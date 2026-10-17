import assert from 'node:assert/strict'
import { test } from 'node:test'

import { parseFailedChecks, parseThreads } from '../hooks/model.ts'
import { bandView, paneView, type Ui } from '../hooks/views.ts'
import { FAILED_CHECKS, THREADS } from './fixtures.ts'

type Node = { type: string; props: Record<string, any> }
const el = (type: string) => (props: Record<string, unknown>) => ({ type, props })
const ui = { Box: el('Box'), Text: el('Text'), Button: el('Button') } as unknown as Ui

function walk(node: unknown, out: Node[] = []): Node[] {
  if (Array.isArray(node)) node.forEach(n => walk(n, out))
  else if (node && typeof node === 'object' && 'type' in node) {
    out.push(node as Node)
    walk((node as Node).props.children, out)
  }
  return out
}
const text = (tree: unknown) => walk(tree).filter(n => n.type === 'Text').map(n => n.props.children).join('')
const button = (tree: unknown, key: string) => walk(tree).find(n => n.type === 'Button' && n.props.key === key)

const NOW = Date.parse('2026-10-04T10:30:00Z')
const snap = { checks: parseFailedChecks(FAILED_CHECKS).checks, threads: parseThreads(THREADS), checksUrl: 'https://app.dash0.com/x', fetchedAt: NOW - 20_000 }
const noop = { open() {}, refresh() {}, hide() {} }
const band = (over = {}) => bandView(ui, { snap, now: NOW, refreshMs: 60_000, window: '1h', isRefreshing: false, ...over }, noop)

test('the band leads with what is failing and how fresh it is', () => {
  const line = text(band())
  assert.match(line, /1 critical/)
  assert.match(line, /2 resolved/)
  assert.match(line, /Low freeable memory/)
  assert.match(line, /1h · 20s ago/)
  assert.doesNotMatch(line, /stale/)
})

test('the band never shows old data as live', () => {
  assert.match(text(band({ now: NOW + 10 * 60_000 })), /stale/)
  assert.match(text(band({ snap: { ...snap, error: 'Dash0 MCP server not connected' } })), /offline: Dash0 MCP server not connected/)
  assert.match(text(band({ snap: undefined, isRefreshing: true })), /loading/)
})

test('band buttons call their handlers', () => {
  let opened = 0
  const tree = bandView(ui, { snap, now: NOW, refreshMs: 60_000, window: '1h', isRefreshing: false }, { ...noop, open: () => opened++ })
  button(tree, 'band-open')!.props.onPress()
  assert.equal(opened, 1)
})

const paneInput = { snap, now: NOW, refreshMs: 60_000, window: '1h', isRefreshing: false, isFocused: true, bodyColumns: 80, bodyRows: 16, dataset: 'default' }

test('pane tabs carry digit hotkeys and switch views', () => {
  const seen: string[] = []
  const tree = paneView(ui, { ...paneInput, view: 'overview' }, { setView: v => seen.push(v), refresh() {}, investigate() {}, copyLink() {} })
  assert.equal(button(tree, 'tab-checks')!.props.hotkey, '2')
  button(tree, 'tab-agent0')!.props.onPress()
  assert.deepEqual(seen, ['agent0'])
})

test('only active checks offer Investigate', () => {
  const investigated: string[] = []
  const tree = paneView(ui, { ...paneInput, view: 'checks' }, { setView() {}, refresh() {}, investigate: c => investigated.push(c.id), copyLink() {} })
  const buttons = walk(tree).filter(n => n.type === 'Button' && String(n.props.key).startsWith('investigate-'))
  assert.equal(buttons.length, 1)
  buttons[0].props.onPress()
  assert.deepEqual(investigated, ['1923268341316241030'])
})

test('agent0 view lists threads; an unfocused pane says how to get the keys', () => {
  const tree = paneView(ui, { ...paneInput, view: 'agent0', isFocused: false }, { setView() {}, refresh() {}, investigate() {}, copyLink() {} })
  const all = text(tree)
  assert.match(all, /Profiling tools availability/)
  assert.match(all, /ctrl\+x tab/)
})

test('every keyed sibling list has unique keys', () => {
  for (const view of ['overview', 'checks', 'agent0'] as const) {
    const tree = paneView(ui, { ...paneInput, view }, { setView() {}, refresh() {}, investigate() {}, copyLink() {} })
    for (const node of walk(tree)) {
      const kids = Array.isArray(node.props.children) ? node.props.children : []
      const keys = kids.map((k: Node) => k?.props?.key).filter(Boolean)
      assert.equal(new Set(keys).size, keys.length, `duplicate keys in ${view}: ${keys}`)
    }
  }
})
