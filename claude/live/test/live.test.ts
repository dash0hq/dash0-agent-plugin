import { expect, mock, test } from 'claude-code/testing'

import { FAILED_CHECKS, THREADS } from './fixtures.ts'

const PANE = {
  component: 'Pane',
  requestId: 'dash0-live',
  props: { title: 'Dash0', isFocused: true, bodyColumns: 100, placement: 'dock', scroll: { offset: 0, bodyRows: 30 }, view: {} },
} as const

const BAND = {
  component: 'AbovePrompt',
  props: { hasSurvey: false, isWorking: false, maxRows: 6, bodyColumns: 120, scroll: { offset: 0, bodyRows: 6 }, view: {} },
} as const

test('the bar and pane read Dash0 through the connected MCP server', async ($, on) => {
  const clock = mock.clock(on)
  const servers: string[] = []
  const statuses: (string | undefined)[] = []
  let filled = ''
  on('session.start', ($, e) => ({ cwd: e.cwd }))
  on('command.register', ($, e) => ({ value: { command: e.name } }))
  on('ui.status', ($, e) => {
    statuses.push(e.text)
    return { value: undefined }
  })
  on('ui.open', () => ({ value: { isPlaced: true as const } }))
  on('ui.close', () => ({ value: undefined }))
  on('prompt.fill', ($, e) => {
    filled = e.text
    return { isFilled: true }
  })
  on('mcp.call', ($, e) => {
    servers.push(e.server)
    // Only the name a claude.ai connector gets answers, to exercise discovery.
    if (e.server !== 'claude.ai Dash0') throw new Error(`no server ${e.server}`)
    const text = e.tool === 'getFailedChecks' ? FAILED_CHECKS : THREADS
    return { value: { content: [{ type: 'text', text }], isError: false } }
  })

  await $.session.start({ surface: 'terminal', isInteractive: true, cwd: '/work' })
  await clock.settle()
  expect(servers).toContain('claude.ai Dash0')
  expect(statuses.at(-1)).toBe('dash0: 1 critical')

  const band = await $.ui.mount({ plugin: 'dash0-live', surface: 'terminal', ...BAND })
  expect(await band.find({ type: 'Text', text: /1 critical/ })).toBeDefined()
  await band.unmount()

  const pane = await $.ui.mount({ plugin: 'dash0-live', surface: 'terminal', ...PANE })
  await pane.press({ key: 'tab-checks' })
  expect(await pane.find({ type: 'Text', text: /Low freeable memory/ })).toBeDefined()
  await pane.press({ key: 'investigate-0' })
  expect(filled).toContain('1923268341316241030')
  await pane.unmount()
})

test('a missing Dash0 server shows offline, not stale counts', async ($, on) => {
  const clock = mock.clock(on)
  const statuses: (string | undefined)[] = []
  on('session.start', ($, e) => ({ cwd: e.cwd }))
  on('command.register', ($, e) => ({ value: { command: e.name } }))
  on('ui.status', ($, e) => {
    statuses.push(e.text)
    return { value: undefined }
  })
  on('mcp.call', () => {
    throw new Error('Dash0 MCP server not connected')
  })

  await $.session.start({ surface: 'terminal', isInteractive: true, cwd: '/work' })
  await clock.settle()
  expect(statuses.at(-1)).toBe('dash0: offline')
  for (const surface of ['terminal', 'desktop'] as const) {
    const band = await $.ui.mount({ plugin: 'dash0-live', surface, ...BAND })
    expect(await band.find({ type: 'Text', text: /offline/ })).toBeDefined()
    await band.unmount()
  }
})
