import { beforeEach, describe, expect, it, vi } from 'vitest'
import type { TodayCard, TodayResponse } from '../api/today'

const calls: string[] = []
let keepaliveDepth = 0
let failWrite = false
let handledRows: Array<{ at: number; card_key: string; action_id: string }> = []

vi.mock('../api/client', () => ({
  withKeepalive: <T>(fn: () => T): T => {
    keepaliveDepth++
    try {
      return fn()
    } finally {
      keepaliveDepth--
    }
  },
}))
vi.mock('../api/steward', () => ({}))
vi.mock('../api/today', () => ({
  getToday: () => Promise.resolve({ decisions: [], snoozed: 1, generated_at: 1 } as unknown as TodayResponse),
  listTodayHandled: () => Promise.resolve({ handled: handledRows }),
  recordTodayAction: (b: { card_key: string }) => {
    calls.push(`audit(${b.card_key})`)
    return Promise.resolve({})
  },
  snoozeTodayCard: (b: { card_key: string; until_at?: number; until_job_id?: string }) => {
    calls.push(`snooze(${b.card_key},${b.until_at ?? ''},${b.until_job_id ?? ''})${keepaliveDepth > 0 ? ':keepalive' : ''}`)
    return failWrite ? Promise.reject(new Error('boom')) : Promise.resolve({})
  },
  unsnoozeTodayCard: (k: string) => {
    calls.push(`unsnooze(${k})`)
    return Promise.resolve({})
  },
}))
vi.mock('../utils/useLiveTopic', () => ({ createLiveTopic: () => ({ start() {}, stop() {} }) }))
vi.stubGlobal('localStorage', { getItem: () => null, setItem: () => undefined })

const today = await import('./today')
const flush = () => new Promise((r) => setTimeout(r, 0))

function card(over: Partial<TodayCard> = {}): TodayCard {
  return {
    key: 'interaction:j1/i1',
    kind: 'interaction',
    tag: '提问',
    urgency: 'normal',
    blocks: { score: 0, items: 0 },
    title: 'c',
    waiting_since: 0,
    activity_at: 0,
    summary: '',
    refs: { job_id: 'j1', interaction_id: 'i1' },
    actions: [],
    advice: null,
    ...over,
  }
}

describe('snooze through the undo window', () => {
  beforeEach(() => {
    calls.length = 0
    failWrite = false
    today.hiddenKeys.value = new Set()
    today.actionError.value = ''
  })

  it('hides at once, sends nothing until the window ends, and undo puts the card back', async () => {
    today.snoozeCard(card(), { id: 'hour', label: '1 小时', until_at: 9999 })
    expect(today.hiddenKeys.value.has('interaction:j1/i1')).toBe(true)
    expect(today.undoQueue.current.value?.label).toBe('已稍后 · 1 小时')
    expect(calls).toEqual([])

    expect(today.undoQueue.undo()).toBe(true)
    await flush()
    expect(today.hiddenKeys.value.has('interaction:j1/i1')).toBe(false)
    expect(calls).toEqual([])
  })

  it('commits the snooze (keepalive on unload) without a second audit call', async () => {
    today.snoozeCard(card(), { id: 'job', label: '等相关 job 结束', until_job_id: 'j1' })
    today.undoQueue.flush(true)
    await flush()
    expect(calls).toEqual(['snooze(interaction:j1/i1,,j1):keepalive'])
  })

  it('puts the card back with an error when the snooze fails', async () => {
    failWrite = true
    today.snoozeCard(card(), { id: 'hour', label: '1 小时', until_at: 9999 })
    today.undoQueue.flush(false)
    await flush()
    expect(today.hiddenKeys.value.has('interaction:j1/i1')).toBe(false)
    expect(today.actionError.value).toContain('boom')
  })

  it('sends at once when the card is about to time out', async () => {
    today.snoozeCard(card({ expires_at: Math.floor(Date.now() / 1000) + 10 }), { id: 'hour', label: '1 小时', until_at: 9999 })
    expect(today.undoQueue.current.value).toBeNull()
    await flush()
    expect(calls).toEqual(['snooze(interaction:j1/i1,9999,)'])
  })

  it('「放回队列」 calls the API and refreshes', async () => {
    await today.unsnoozeCard('work:w1')
    expect(calls).toEqual(['unsnooze(work:w1)'])
    expect(today.todayData.value?.snoozed).toBe(1)
  })

  it('does not count snoozes as handled today', async () => {
    const now = Math.floor(Date.now() / 1000)
    handledRows = [
      { at: now, card_key: 'a', action_id: 'accept' },
      { at: now, card_key: 'b', action_id: 'snooze' },
    ]
    await today.loadHandledToday()
    expect(today.handledTodayCount.value).toBe(1)
  })
})
