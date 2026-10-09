import { describe, expect, it } from 'vitest'
import type { TunnelForwarder } from '../api/types'
import { forwarderStopState, forwarderStopUnsupportedHint } from './forwarderStop'

const base: TunnelForwarder = {
  id: 'fw-1',
  caller_id: 'default',
  worker: 'w-hw',
  specs: [],
  host: 'workshop-pc',
  pid: 42,
  hosted: false,
  started_at: '2026-01-01T00:00:00Z',
  last_seen_at: '2026-01-01T00:00:00Z',
  connections: 0,
  bytes_up: 0,
  bytes_down: 0,
}

const read = (glob: Record<string, unknown>): string => Object.values(glob)[0] as string
const page = read(import.meta.glob('../views/settings/Tunnels.vue', { eager: true, query: '?raw', import: 'default' }))

describe('forwarder stop button state', () => {
  it('lets a forwarder that advertised the stop capability be stopped', () => {
    expect(forwarderStopState({ ...base, caps: ['stop'] })).toBe('stoppable')
  })

  it('shows a pending stop until the forwarder disappears', () => {
    expect(forwarderStopState({ ...base, caps: ['stop'], stop_requested: true })).toBe('stopping')
  })

  it('disables the button for an older forwarder without the capability, naming its host', () => {
    expect(forwarderStopState(base)).toBe('unsupported')
    expect(forwarderStopState({ ...base, caps: [] })).toBe('unsupported')
    const hint = forwarderStopUnsupportedHint(base)
    expect(hint).toContain('workshop-pc')
    expect(hint).toContain('Ctrl+C')
  })

  it('leaves hosted forwarders to their own stop', () => {
    expect(forwarderStopState({ ...base, hosted: true, caps: ['stop'] })).toBe('hosted')
  })
})

describe('Tunnels page stop controls', () => {
  it('confirms in the page (no window.confirm) and shows the pending state', () => {
    for (const hook of ['fw-stop', 'fw-stop-confirm', 'fw-stop-cancel', 'fw-stopping', 'fw-stop-disabled']) {
      expect(page).toContain(`data-test="${hook}"`)
    }
    expect(page).toContain('停止中（≤30 秒内退出）')
    expect(page).toContain('stopTunnelForwarder(')
    expect(page).not.toContain('window.confirm(`停止')
  })
})
