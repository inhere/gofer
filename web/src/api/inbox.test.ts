import { afterEach, describe, expect, it, vi } from 'vitest'

const calls: Array<{ url: string; init?: RequestInit }> = []
vi.mock('./client', () => ({
  request: vi.fn(async (url: string, init?: RequestInit) => {
    calls.push({ url, init })
    return { updated: 1, action: 'seen' }
  }),
}))

import { getInbox, postInboxState } from './inbox'
import { stateRequest } from '../utils/inbox'

afterEach(() => {
  calls.length = 0
})

describe('getInbox query', () => {
  it('sends nothing by default', async () => {
    await getInbox()
    expect(calls[0].url).toBe('/v1/inbox')
  })

  it('sends include_cleared and include_exec when asked', async () => {
    await getInbox({ includeCleared: true, includeExec: true })
    expect(calls[0].url).toBe('/v1/inbox?include_cleared=1&include_exec=1')
  })
})

describe('postInboxState body', () => {
  it('posts one JSON body to /v1/inbox/state', async () => {
    await postInboxState(stateRequest('seen', { column: 'action' }))
    expect(calls[0].url).toBe('/v1/inbox/state')
    expect(calls[0].init?.method).toBe('POST')
    expect(JSON.parse(String(calls[0].init?.body))).toEqual({ action: 'seen', column: 'action' })
  })

  it('posts keys for a single item', async () => {
    await postInboxState(stateRequest('clear', { keys: ['review:j1'] }))
    expect(JSON.parse(String(calls[0].init?.body))).toEqual({ action: 'clear', keys: ['review:j1'] })
  })
})
