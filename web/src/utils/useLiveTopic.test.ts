import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { nextTick } from 'vue'
import { liveFallback, type LiveClient, type LiveHandler, type LiveMessage } from '../api/live'
import { createLiveTopic } from './useLiveTopic'
import { DETAIL_INCLUDES } from './jobDetailInclude'
import { getJobDetail } from '../api/client'

function fakeClient(): { client: LiveClient; push(m: LiveMessage): void; subs: () => number } {
  const handlers = new Set<LiveHandler>()
  return {
    client: {
      subscribe(_t, h) {
        handlers.add(h)
        return () => handlers.delete(h)
      },
      noteSeq() {},
      status: () => 'connected',
      close() {},
    },
    push: (m) => handlers.forEach((h) => h(m)),
    subs: () => handlers.size,
  }
}

describe('useLiveTopic', () => {
  beforeEach(() => {
    vi.useFakeTimers()
    liveFallback.value = false
  })
  afterEach(() => {
    vi.useRealTimers()
  })

  it('fetches once initially, then only on debounced inval / resync / reconnect', async () => {
    const f = fakeClient()
    const fetch = vi.fn()
    const t = createLiveTopic('jobs', { fetch, client: f.client, debounceMs: 100 })
    t.start()
    await vi.advanceTimersByTimeAsync(0)
    expect(fetch).toHaveBeenCalledTimes(1)
    for (let i = 0; i < 20; i++) f.push({ type: 'inval', data: {} })
    await vi.advanceTimersByTimeAsync(150)
    expect(fetch).toHaveBeenCalledTimes(2) // 20 invals -> one refetch
    f.push({ type: 'reconnect' })
    await vi.advanceTimersByTimeAsync(150)
    expect(fetch).toHaveBeenCalledTimes(3)
    t.stop()
    expect(f.subs()).toBe(0)
    f.push({ type: 'inval', data: {} })
    await vi.advanceTimersByTimeAsync(500)
    expect(fetch).toHaveBeenCalledTimes(3)
  })

  it('snapshot topics skip the initial fetch and never refetch on reconnect', async () => {
    const f = fakeClient()
    const fetch = vi.fn()
    const snaps: unknown[] = []
    const t = createLiveTopic('stats', { fetch, client: f.client, initial: false, onSnap: (d) => snaps.push(d) })
    t.start()
    f.push({ type: 'snap', data: { a: 1 } })
    f.push({ type: 'reconnect' })
    await vi.advanceTimersByTimeAsync(1000)
    expect(snaps).toEqual([{ a: 1 }])
    expect(fetch).not.toHaveBeenCalled()
    t.stop()
  })

  it('runs a 30s fallback poller only while the socket has been down, and stops when it recovers', async () => {
    const f = fakeClient()
    const fetch = vi.fn()
    const made: { start: ReturnType<typeof vi.fn>; stop: ReturnType<typeof vi.fn>; ms: number }[] = []
    const t = createLiveTopic('plans', {
      fetch,
      client: f.client,
      initial: false,
      makePoller: (_fn, ms) => {
        const p = { start: vi.fn(), stop: vi.fn(), ms }
        made.push(p)
        return p
      },
    })
    t.start()
    expect(made.length).toBe(0)
    liveFallback.value = true
    await nextTick()
    expect(made.length).toBe(1)
    expect(made[0].ms).toBe(30_000)
    expect(made[0].start).toHaveBeenCalled()
    liveFallback.value = false
    await nextTick()
    expect(made[0].stop).toHaveBeenCalled()
    t.stop()
  })
})

describe('job detail aggregation', () => {
  afterEach(() => vi.unstubAllGlobals())

  it('opening the page costs ONE request carrying every include', async () => {
    const urls: string[] = []
    vi.stubGlobal('sessionStorage', { getItem: () => null })
    vi.stubGlobal('fetch', vi.fn(async (url: string) => {
      urls.push(url)
      return { ok: true, status: 200, text: async () => JSON.stringify({ id: 'j1', status: 'done' }) } as unknown as Response
    }))
    await getJobDetail('j1', DETAIL_INCLUDES)
    expect(urls).toHaveLength(1)
    expect(urls[0]).toBe(
      '/v1/jobs/j1?include=events,comments,deliveries,retries,wakeups,pty_sessions,artifacts,session_jobs',
    )
    // older-events paging reuses the same endpoint
    await getJobDetail('j1', ['events'], { before: 12 })
    expect(urls[1]).toBe('/v1/jobs/j1?include=events&before=12')
  })
})
