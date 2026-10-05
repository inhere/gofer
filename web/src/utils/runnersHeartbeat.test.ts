import { describe, expect, it } from 'vitest'
import type { Runner } from '../api/types'
import { STALE_MS, SERVER_PING_MS, beatOf, fmtAge, workerAgeMs, workerStatusText } from './runners'

function w(lastBeat: number): Runner {
  return {
    name: 'w1', type: 'worker', status: 'connected', worker_id: 'w1',
    worker: { last_heartbeat: lastBeat, heartbeat_age_ms: 0, in_flight: 0 },
  } as Runner
}

describe('worker heartbeat age', () => {
  it('is recomputed from the local clock, not from fetched age', () => {
    const r = w(1_000_000)
    expect(workerAgeMs(r, 1_003_000)).toBe(3000)
    expect(fmtAge(workerAgeMs(r, 1_003_000))).toBe('3s ago')
    expect(fmtAge(workerAgeMs(r, 1_008_000))).toBe('8s ago')
  })
  it('turns stale only after two missed server pings', () => {
    expect(STALE_MS).toBe(2 * SERVER_PING_MS)
    const r = w(1_000_000)
    expect(beatOf(r, 1_000_000 + SERVER_PING_MS)).toBe('connected')
    expect(beatOf(r, 1_000_000 + STALE_MS + 1000)).toBe('stale')
    expect(workerStatusText(r, 1_000_000 + STALE_MS + 1000)).toContain('no heartbeat')
    expect(workerStatusText(r, 1_000_000 + 2000)).toBe('connected')
  })
})
