import { afterEach, describe, expect, it, vi } from 'vitest'

const requested: string[] = []
vi.mock('./client', () => ({
  request: vi.fn(async (url: string) => {
    requested.push(url)
    return { projects: [], attention: [], total: 0, since: 0 }
  }),
}))

import { listWorkbenchThreads } from './workbench'

afterEach(() => {
  requested.length = 0
})

describe('listWorkbenchThreads query', () => {
  it('sends nothing extra by default', async () => {
    await listWorkbenchThreads()
    expect(requested).toEqual(['/v1/workbench/threads'])
  })

  it('sends detail=summary and one full= per open thread', async () => {
    await listWorkbenchThreads({ detail: 'summary', full: ['s:a', 'j:b'] })
    expect(requested).toEqual(['/v1/workbench/threads?detail=summary&full=s%3Aa&full=j%3Ab'])
  })

  it('sends detail=summary without full when nothing is open', async () => {
    await listWorkbenchThreads({ detail: 'summary', full: [] })
    expect(requested).toEqual(['/v1/workbench/threads?detail=summary'])
  })

  it('sends the activity window and paging parameters only when set', async () => {
    await listWorkbenchThreads({ active_within: 604800, limit: 30 })
    await listWorkbenchThreads({ cursor: 'abc_-', limit: 30, detail: 'summary' })
    await listWorkbenchThreads({ active_within: 0, limit: 0, cursor: '' })
    expect(requested).toEqual([
      '/v1/workbench/threads?active_within=604800&limit=30',
      '/v1/workbench/threads?limit=30&cursor=abc_-&detail=summary',
      '/v1/workbench/threads',
    ])
  })
})
