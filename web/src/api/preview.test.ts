import { afterEach, describe, expect, it, vi } from 'vitest'

const calls: Array<{ url: string; init?: RequestInit }> = []
vi.mock('./client', () => ({
  request: vi.fn(async (url: string, init?: RequestInit) => {
    calls.push({ url, init })
    return { previews: [{ id: 'pv1', port: 18100, state: 'active', expires_at: 1900000000 }] }
  }),
}))

import { createJobPreview, listJobPreviews, renewJobPreview, revokeJobPreview } from './preview'

afterEach(() => {
  calls.length = 0
})

describe('job preview api', () => {
  it('creates with port and ttl_sec (snake_case) via POST /v1/jobs/{id}/previews', async () => {
    await createJobPreview('j 1', { port: 3000, ttl_sec: 600 })
    expect(calls[0].url).toBe('/v1/jobs/j%201/previews')
    expect(calls[0].init?.method).toBe('POST')
    expect(JSON.parse(String(calls[0].init?.body))).toEqual({ port: 3000, ttl_sec: 600 })
  })

  it('lists via GET and unwraps previews', async () => {
    const rows = await listJobPreviews('j1')
    expect(calls[0].url).toBe('/v1/jobs/j1/previews')
    expect(calls[0].init).toBeUndefined()
    expect(rows).toEqual([{ id: 'pv1', port: 18100, state: 'active', expires_at: 1900000000 }])
  })

  it('revokes with DELETE and renews with POST {ttl_sec}', async () => {
    await revokeJobPreview('j1', 'pv1')
    expect(calls[0].url).toBe('/v1/jobs/j1/previews/pv1')
    expect(calls[0].init?.method).toBe('DELETE')
    await renewJobPreview('j1', 'pv1', 1800)
    expect(calls[1].url).toBe('/v1/jobs/j1/previews/pv1/renew')
    expect(calls[1].init?.method).toBe('POST')
    expect(JSON.parse(String(calls[1].init?.body))).toEqual({ ttl_sec: 1800 })
  })

  it('never sends a request to the one-time preview URL (it is not an API URL)', async () => {
    await createJobPreview('j1', { port: 3000 })
    await listJobPreviews('j1')
    for (const c of calls) expect(c.url.startsWith('/v1/')).toBe(true)
  })
})
