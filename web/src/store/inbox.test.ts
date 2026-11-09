import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import type { InboxItem } from '../api/inbox'

const posted: unknown[] = []
const fetched: Array<{ includeCleared?: boolean; includeExec?: boolean }> = []
let server: { columns: { action: InboxItem[]; fyi: InboxItem[] }; recheck_at?: number } = { columns: { action: [], fyi: [] } }

vi.mock('../api/inbox', () => ({
  getInbox: vi.fn(async (opts: { includeCleared?: boolean; includeExec?: boolean }) => {
    fetched.push(opts)
    return { ...server, unread: { action: 0, fyi: 0 }, total_unread: 0, generated_at: 1 }
  }),
  postInboxState: vi.fn(async (body: unknown) => {
    posted.push(body)
    return { updated: 2, action: (body as { action: string }).action }
  }),
}))

import { inboxBadgeCount, inboxData, markInbox, refreshInbox, showCleared } from './inbox'

function item(key: string, column: 'action' | 'fyi', state: InboxItem['state']): InboxItem {
  return { key, column, kind: 'review', title: key, summary: '', source_revision: 'r', state, actions: [], refs: {}, activity_at: 1 }
}

beforeEach(() => {
  posted.length = 0
  fetched.length = 0
  showCleared.value = false
  server = {
    columns: {
      action: [item('a1', 'action', 'unread'), item('a2', 'action', 'seen')],
      fyi: [item('f1', 'fyi', 'unread'), item('f2', 'fyi', 'unread')],
    },
  }
})

afterEach(() => {
  vi.useRealTimers()
})

describe('inbox store', () => {
  it('refetches when a grace-period relay turn becomes eligible', async () => {
    vi.useFakeTimers()
    try {
      vi.setSystemTime(1000)
      server.recheck_at = 6
      await refreshInbox()
      expect(fetched).toHaveLength(1)
      vi.advanceTimersByTime(4000)
      await Promise.resolve()
      expect(fetched).toHaveLength(1)
      vi.advanceTimersByTime(1000)
      await Promise.resolve()
      await Promise.resolve()
      expect(fetched).toHaveLength(2)
    } finally {
      vi.useRealTimers()
    }
  })

  it('derives the badge from the action column only', async () => {
    await refreshInbox()
    expect(inboxData.value?.unread).toEqual({ action: 1, fyi: 2 })
    expect(inboxBadgeCount.value).toBe(1)
  })

  it('asks for cleared items only when shown', async () => {
    await refreshInbox()
    showCleared.value = true
    await refreshInbox()
    expect(fetched[0].includeCleared).toBe(false)
    expect(fetched[1].includeCleared).toBe(true)
  })

  it('posts the column on a bulk mark and updates counts immediately', async () => {
    await refreshInbox()
    const n = await markInbox('seen', { column: 'fyi' })
    expect(posted).toEqual([{ action: 'seen', column: 'fyi' }])
    expect(n).toBe(2)
    expect(inboxData.value?.unread.fyi).toBe(0)
    expect(inboxBadgeCount.value).toBe(1)
  })

  it('clears read items by key and drops them from the list', async () => {
    await refreshInbox()
    await markInbox('clear', { keys: ['a2'], column: 'action' })
    expect(posted).toEqual([{ action: 'clear', keys: ['a2'], column: 'action' }])
    expect(inboxData.value?.columns.action.map((i) => i.key)).toEqual(['a1'])
  })

  it('marking the only action item read clears the badge', async () => {
    await refreshInbox()
    await markInbox('seen', { keys: ['a1'] })
    expect(inboxBadgeCount.value).toBe(0)
    expect(inboxData.value?.unread.fyi).toBe(2)
  })
})
