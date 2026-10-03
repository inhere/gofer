import { afterEach, describe, expect, it, vi } from 'vitest'
import { MAX_STDERR_BUFFER_BYTES, MAX_LOG_BUFFER_BYTES, streamJob } from './sse'

function mockFetch(): { urls: string[] } {
  const urls: string[] = []
  vi.stubGlobal('sessionStorage', { getItem: () => null })
  vi.stubGlobal('fetch', vi.fn(async (url: string) => {
    urls.push(url)
    const body = new ReadableStream({
      start(c) {
        c.enqueue(new TextEncoder().encode('event: end\ndata: {}\n\n'))
        c.close()
      },
    })
    return { ok: true, status: 200, body } as unknown as Response
  }))
  return { urls }
}

afterEach(() => vi.unstubAllGlobals())

describe('streamJob query params', () => {
  it('first connect sends tail, reconnect sends byte offsets and no tail', async () => {
    const m = mockFetch()
    await streamJob('j1', { tail: 500, onEvent: () => {} })
    await streamJob('j1', { from: 1234, stderrFrom: 99, onEvent: () => {} })
    expect(m.urls[0]).toBe('/v1/jobs/j1/stream?tail=500')
    expect(m.urls[1]).toBe('/v1/jobs/j1/stream?from=1234&stderr_from=99')
  })

  it('keeps the stderr buffer cap well below the stdout cap', () => {
    expect(MAX_STDERR_BUFFER_BYTES).toBe(256 * 1024)
    expect(MAX_STDERR_BUFFER_BYTES).toBeLessThan(MAX_LOG_BUFFER_BYTES)
  })
})
