import { describe, expect, it } from 'vitest'
import type { Runner } from '../api/types'
import { runnerTools, toolTitle } from './runners'

const base = { name: 'w1', type: 'worker', status: 'connected' } as Runner

describe('runnerTools', () => {
  it('reads the capability summary first', () => {
    const r = { ...base, capabilities: { tools: [{ name: 'ffmpeg' }] }, worker: { tools: [{ name: 'other' }] } } as Runner
    expect(runnerTools(r).map((t) => t.name)).toEqual(['ffmpeg'])
  })
  it('falls back to the worker detail, then to none', () => {
    const r = { ...base, worker: { last_heartbeat: 1, heartbeat_age_ms: 0, in_flight: 0, tools: [{ name: 'sim' }] } } as Runner
    expect(runnerTools(r).map((t) => t.name)).toEqual(['sim'])
    expect(runnerTools(base)).toEqual([])
  })
})

describe('toolTitle', () => {
  it('shows desc and a bin that differs from the name', () => {
    expect(toolTitle({ name: 'sim', desc: 'simulator', bin: '/opt/sim' })).toBe('simulator\nbin: /opt/sim')
    expect(toolTitle({ name: 'ffmpeg', bin: 'ffmpeg' })).toBe('ffmpeg')
  })
})
