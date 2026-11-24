// 常驻会话接口里名册用到的那些：history 打到 /v1/seats/{id}/history，返回体原样交给组件。
import { afterEach, describe, expect, it, vi } from 'vitest'

const calls: Array<{ url: string; init?: RequestInit }> = []
vi.mock('./client', () => ({
  request: vi.fn(async (url: string, init?: RequestInit) => {
    calls.push({ url, init })
    return { bindings: [], charter: [] }
  }),
}))

import { seatHistory } from './seats'

afterEach(() => {
  calls.length = 0
})

describe('seatHistory', () => {
  it('reads the history of one seat', async () => {
    const got = await seatHistory('seat-1')
    expect(calls[0].url).toBe('/v1/seats/seat-1/history')
    expect(got).toEqual({ bindings: [], charter: [] })
  })

  it('escapes the id like the other seat calls', async () => {
    await seatHistory('a/b')
    expect(calls[0].url).toBe('/v1/seats/a%2Fb/history')
  })
})
