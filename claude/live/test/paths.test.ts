import type { On } from 'claude-code'
import { expect, mock, test } from 'claude-code/testing'

import { FAILED_CHECKS, FAILED_CHECKS_EMPTY, FORBIDDEN_DATASET, THREADS } from './fixtures.ts'

const SURFACES = ['terminal', 'desktop', 'vscode', 'mobile'] as const

const BAND = {
  component: 'AbovePrompt',
  props: { hasSurvey: false, isWorking: false, maxRows: 6, bodyColumns: 120, scroll: { offset: 0, bodyRows: 6 }, view: {} },
} as const

const pane = (placement: 'dock' | 'inline', bodyColumns: number) =>
  ({
    component: 'Pane',
    requestId: 'dash0-live',
    props: { title: 'Dash0', isFocused: true, bodyColumns, placement, scroll: { offset: 0, bodyRows: 18 }, view: {} },
  }) as const

type Answer = { text: string; isError?: boolean } | Error

/** The engine's own hooks, with `mcp.call` answered by `answer(server, tool)`. */
function engine(on: On, answer: (server: string, tool: string) => Answer) {
  const seen = { servers: [] as string[], statuses: [] as (string | undefined)[], opened: 0, toasts: [] as string[] }
  on('session.start', ($, e) => ({ cwd: e.cwd }))
  on('command.register', ($, e) => ({ value: { command: e.name } }))
  on('ui.status', ($, e) => {
    seen.statuses.push(e.text)
    return { value: undefined }
  })
  on('ui.open', () => {
    seen.opened += 1
    return { value: { isPlaced: true as const } }
  })
  on('ui.close', () => ({ value: undefined }))
  on('ui.toast', ($, e) => {
    seen.toasts.push(String(e.text))
    return { value: undefined }
  })
  on('ui.copy', () => ({ value: { isCopied: true } }))
  // What core draws when the mod yields (a hidden band): nothing.
  on('ui.render', ($, e) => $.ui.resolve(e).Box({ key: 'core' }))
  on('session.end', ($, e) => ({ sessionId: e.sessionId }))
  on('mcp.call', ($, e) => {
    seen.servers.push(e.server)
    const a = answer(e.server, e.tool)
    if (a instanceof Error) throw a
    return { value: { content: [{ type: 'text', text: a.text }], isError: a.isError ?? false } }
  })
  return seen
}

/** `/dash0 <args>` as the person types it. */
const dash0 = (args: string) => ({ command: 'dash0', args, origin: { kind: 'composer' }, presentation: { isFullscreen: false, columns: 120 } }) as const

const healthy = (server: string, tool: string): Answer =>
  server === 'Dash0' ? { text: tool === 'getFailedChecks' ? FAILED_CHECKS : THREADS } : new Error(`no server ${server}`)

test('a tool error from a connected server surfaces the API message and pins that server', async ($, on) => {
  const clock = mock.clock(on)
  const seen = engine(on, server => (server === 'claude.ai Dash0' ? { text: FORBIDDEN_DATASET, isError: true } : new Error('unknown server')))
  await $.session.start({ surface: 'terminal', isInteractive: true, cwd: '/work' })
  await clock.settle()
  expect(seen.statuses.at(-1)).toBe('dash0: offline')
  const band = await $.ui.mount({ plugin: 'dash0-live', surface: 'terminal', ...BAND })
  expect(await band.find({ type: 'Text', text: /403: access to dataset/ })).toBeDefined()
  await band.unmount()
  // The server that answered is remembered: a refresh asks it alone.
  const misses = () => seen.servers.filter(s => s !== 'claude.ai Dash0').length
  const before = { misses: misses(), calls: seen.servers.length }
  await $.command.run(dash0('refresh'))
  await clock.settle()
  expect(seen.servers.length).toBeGreaterThan(before.calls)
  expect(misses()).toBe(before.misses)
})

test('no reachable server names every name tried', async ($, on) => {
  const clock = mock.clock(on)
  engine(on, server => new Error(`no server ${server}`))
  await $.session.start({ surface: 'terminal', isInteractive: true, cwd: '/work' })
  await clock.settle()
  const band = await $.ui.mount({ plugin: 'dash0-live', surface: 'terminal', ...BAND })
  expect(await band.find({ type: 'Text', text: /not found \(tried Dash0, dash0, claude\.ai Dash0\)/ })).toBeDefined()
  await band.unmount()
})

test('an empty answer reads as all clear', async ($, on) => {
  const clock = mock.clock(on)
  const seen = engine(on, (server, tool) => (server === 'Dash0' ? { text: tool === 'getFailedChecks' ? FAILED_CHECKS_EMPTY : THREADS } : new Error('x')))
  await $.session.start({ surface: 'terminal', isInteractive: true, cwd: '/work' })
  await clock.settle()
  expect(seen.statuses.at(-1)).toBe('dash0: all clear')
})

test('a failing Agent0 listing does not take the checks offline', async ($, on) => {
  const clock = mock.clock(on)
  const seen = engine(on, (server, tool) =>
    server !== 'Dash0' ? new Error('x') : tool === 'getFailedChecks' ? { text: FAILED_CHECKS } : { text: 'HTTP 500', isError: true },
  )
  await $.session.start({ surface: 'terminal', isInteractive: true, cwd: '/work' })
  await clock.settle()
  expect(seen.statuses.at(-1)).toBe('dash0: 1 critical')
})

test('band and pane draw on every surface, docked and inline', async ($, on) => {
  const clock = mock.clock(on)
  engine(on, healthy)
  await $.session.start({ surface: 'terminal', isInteractive: true, cwd: '/work' })
  await clock.settle()
  for (const surface of SURFACES) {
    const band = await $.ui.mount({ plugin: 'dash0-live', surface, ...BAND })
    expect(await band.find({ type: 'Text', text: /1 critical/ })).toBeDefined()
    await band.unmount()
    for (const [placement, columns] of [['dock', 100], ['inline', 60]] as const) {
      const ui = await $.ui.mount({ plugin: 'dash0-live', surface, ...pane(placement, columns) })
      expect(await ui.find({ key: 'tab-overview' })).toBeDefined()
      await ui.press({ key: 'tab-agent0' })
      expect(await ui.find({ type: 'Text', text: /Profiling tools availability/ })).toBeDefined()
      await ui.press({ key: 'tab-overview' })
      await ui.unmount()
    }
  }
})

test('/dash0 opens the pane on a tab, and hide/show toggles the bar', async ($, on) => {
  const clock = mock.clock(on)
  const seen = engine(on, healthy)
  await $.session.start({ surface: 'terminal', isInteractive: true, cwd: '/work' })
  await clock.settle()

  const opened = await $.command.run(dash0('checks'))
  expect(seen.opened).toBe(1)
  expect(opened.text).toBe('Dash0 pane open. dash0: 1 critical')
  // The model reads this row: counts only, never a check summary.
  expect(opened.text).not.toMatch(/freeable memory/i)
  const ui = await $.ui.mount({ plugin: 'dash0-live', surface: 'terminal', ...pane('dock', 100) })
  expect(await ui.find({ type: 'Text', text: /Low freeable memory/ })).toBeDefined()
  await ui.unmount()

  await $.command.run(dash0('hide'))
  const hidden = await $.ui.mount({ plugin: 'dash0-live', surface: 'terminal', ...BAND })
  expect(await hidden.find({ type: 'Text', text: /critical/ })).toBeUndefined()
  await hidden.unmount()

  await $.command.run(dash0('show'))
  const shown = await $.ui.mount({ plugin: 'dash0-live', surface: 'terminal', ...BAND })
  expect(await shown.find({ type: 'Text', text: /1 critical/ })).toBeDefined()
  await shown.press({ key: 'band-hide' })
  await shown.unmount()
  const afterPress = await $.ui.mount({ plugin: 'dash0-live', surface: 'terminal', ...BAND })
  expect(await afterPress.find({ type: 'Text', text: /critical/ })).toBeUndefined()
  await afterPress.unmount()
})

test('polling refreshes on the configured period and stops at session end', async ($, on) => {
  const clock = mock.clock(on)
  const seen = engine(on, healthy)
  await $.session.start({ surface: 'terminal', isInteractive: true, cwd: '/work' })
  await clock.settle()
  const first = seen.servers.length
  await clock.advance(60_000)
  await clock.settle()
  expect(seen.servers.length).toBeGreaterThan(first)
  await $.session.end({ reason: 'prompt_input_exit', sessionId: 's1' } as never)
  const atEnd = seen.servers.length
  await clock.advance(180_000)
  await clock.settle()
  expect(seen.servers.length).toBe(atEnd)
  expect(seen.statuses.at(-1)).toBeUndefined()
})

test('copy link copies the Dash0 failed-checks URL', async ($, on) => {
  const clock = mock.clock(on)
  const seen = engine(on, healthy)
  await $.session.start({ surface: 'terminal', isInteractive: true, cwd: '/work' })
  await clock.settle()
  const ui = await $.ui.mount({ plugin: 'dash0-live', surface: 'terminal', ...pane('dock', 100) })
  await ui.press({ key: 'copy-checks' })
  await clock.settle()
  expect(seen.toasts).toContain('Dash0 link copied')
  await ui.unmount()
})

test('a server that connects just after start is picked up within seconds, not a polling period', async ($, on) => {
  const clock = mock.clock(on)
  let isConnected = false
  const seen = engine(on, (server, tool) => (isConnected ? healthy(server, tool) : new Error(`no server ${server}`)))
  await $.session.start({ surface: 'terminal', isInteractive: true, cwd: '/work' })
  await clock.settle()
  expect(seen.statuses.at(-1)).toBe('dash0: offline')
  isConnected = true
  await clock.advance(2_000)
  await clock.settle()
  expect(seen.statuses.at(-1)).toBe('dash0: 1 critical')
})
