import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import type { TodayCard, TodayResponse } from '../api/today'

const calls: string[] = []
let keepaliveDepth = 0
let failWrite = false
let serverCards: TodayCard[] = []

vi.mock('../api/client', () => ({
  withKeepalive: <T>(fn: () => T): T => {
    keepaliveDepth++
    try {
      return fn()
    } finally {
      keepaliveDepth--
    }
  },
  requestWorkReport: (id: string) => {
    calls.push(`report(${id})${keepaliveDepth > 0 ? ':keepalive' : ''}`)
    return failWrite ? Promise.reject(new Error('boom')) : Promise.resolve({})
  },
}))
vi.mock('../api/steward', () => ({}))
vi.mock('../api/today', () => ({
  getToday: () => Promise.resolve({ decisions: serverCards, generated_at: 1 } as unknown as TodayResponse),
  listTodayHandled: () => Promise.resolve({ handled: [] }),
  recordTodayAction: (b: { card_key: string }) => {
    calls.push(`audit(${b.card_key})${keepaliveDepth > 0 ? ':keepalive' : ''}`)
    return Promise.resolve({})
  },
}))
vi.mock('../utils/useLiveTopic', () => ({ createLiveTopic: () => ({ start() {}, stop() {} }) }))

const store = new Map<string, string>()
vi.stubGlobal('localStorage', {
  getItem: (k: string) => store.get(k) ?? null,
  setItem: (k: string, v: string) => void store.set(k, v),
})

const today = await import('./today')
const { LAST_OPEN_KEY, TODAY_SEEN_SEC } = await import('../utils/today')

const flush = () => new Promise((r) => setTimeout(r, 0))

function workCard(): TodayCard {
  return {
    key: 'work:w1',
    kind: 'work',
    tag: '等我',
    urgency: 'normal',
    blocks: { score: 0, items: 0 },
    title: 'w1',
    waiting_since: 0,
    activity_at: 0,
    summary: '',
    refs: { work_item_id: 'w1' },
    actions: [{ id: 'report', label: '请求汇报' }],
    advice: null,
  }
}

describe('since-last-open watermark', () => {
  beforeEach(() => store.clear())

  it('advances only after the page was seen, to the start of that visit', () => {
    expect(today.beginTodayVisit(1000)).toBe(0)
    // A quick pass-through does not move the watermark.
    today.endTodayVisit(1000 + TODAY_SEEN_SEC - 1)
    expect(store.get(LAST_OPEN_KEY)).toBeUndefined()

    expect(today.beginTodayVisit(2000)).toBe(0)
    // Re-entering while the visit is open keeps it.
    expect(today.beginTodayVisit(2005)).toBe(0)
    today.endTodayVisit(2000 + TODAY_SEEN_SEC)
    expect(store.get(LAST_OPEN_KEY)).toBe('2000')

    // The next visit shows what happened since the previous visit started.
    expect(today.beginTodayVisit(5000)).toBe(2000)
    expect(today.todaySince.value).toBe(2000)
    today.endTodayVisit(5100)
    expect(store.get(LAST_OPEN_KEY)).toBe('5000')
  })
})

describe('card actions', () => {
  beforeEach(() => {
    calls.length = 0
    failWrite = false
    serverCards = [workCard()]
    today.hiddenKeys.value = new Set()
  })
  afterEach(() => today.undoQueue.flush(false))

  it('records the audit only after the write succeeds, both keepalive on unload', async () => {
    today.actOnCard(workCard(), workCard().actions[0])
    today.undoQueue.flush(true)
    await flush()
    expect(calls).toEqual(['report(w1):keepalive', 'audit(work:w1):keepalive'])

    calls.length = 0
    failWrite = true
    today.actOnCard(workCard(), workCard().actions[0])
    today.undoQueue.flush(true)
    await flush()
    expect(calls).toEqual(['report(w1):keepalive'])
    // A failed write puts the card back.
    expect(today.hiddenKeys.value.has('work:w1')).toBe(false)
  })

  it('shows a card again when the server still returns it after the action committed', async () => {
    today.actOnCard(workCard(), workCard().actions[0])
    expect(today.hiddenKeys.value.has('work:w1')).toBe(true)
    // A refresh while the action waits in the undo window keeps it hidden.
    await today.refreshToday()
    expect(today.hiddenKeys.value.has('work:w1')).toBe(true)
    today.undoQueue.flush(false)
    await flush()
    await flush()
    // The write committed; the next refresh still sees the card (「请求汇报」 does not
    // change needs_me), so it is visible again instead of hidden forever.
    await today.refreshToday()
    expect(today.hiddenKeys.value.has('work:w1')).toBe(false)
    expect(today.visibleCards.value.map((c) => c.key)).toEqual(['work:w1'])
  })
})
