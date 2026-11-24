import { afterEach, describe, expect, it, vi } from 'vitest'
import { nextTick } from 'vue'
import { liveFallback, type LiveClient, type LiveHandler, type LiveMessage } from '../api/live'
import type { TodayCard } from '../api/today'
import type { WaitingItem } from '../api/types'
import { createWaitingStore, setWaitingStoreForTest, useWaiting, waitingItemOf } from './useWaiting'

function fakeClient(): { client: LiveClient; push(topic: string, m: LiveMessage): void; subs(): number } {
  const handlers = new Map<string, Set<LiveHandler>>()
  return {
    client: {
      subscribe(topic, h) {
        let set = handlers.get(topic)
        if (!set) {
          set = new Set()
          handlers.set(topic, set)
        }
        set.add(h)
        return () => {
          set!.delete(h)
        }
      },
      noteSeq() {},
      status: () => 'connected',
      close() {},
    },
    push(topic, m) {
      handlers.get(topic)?.forEach((h) => h(m))
    },
    subs: () => handlers.get('waiting')?.size ?? 0,
  }
}

function item(key: string, category: WaitingItem['category'] = 'answer'): WaitingItem {
  return { key, category, kind: 'relay', waiting_since: 100, title: key }
}

// collectPoller captures the poller useLiveTopic builds so a test can drive it by hand.
function collectPoller() {
  const made: { start: () => void; stop: () => void; fn: () => void | Promise<void> }[] = []
  const makePoller = (fn: () => void | Promise<void>) => {
    const p = { start: vi.fn(), stop: vi.fn(), fn }
    made.push(p)
    return p
  }
  return { made, makePoller }
}

describe('useWaiting', () => {
  afterEach(() => {
    setWaitingStoreForTest(null)
    liveFallback.value = false
  })

  it('is a singleton: two callers share one store and one subscription', () => {
    const f = fakeClient()
    setWaitingStoreForTest(createWaitingStore({ client: f.client }))
    const a = useWaiting()
    const b = useWaiting()
    expect(a).toBe(b)
    expect(f.subs()).toBe(1)
    f.push('waiting', { type: 'snap', data: [item('session:s1')] })
    expect(a.items.value.map((it) => it.key)).toEqual(['session:s1'])
    expect(b.items.value).toBe(a.items.value)
  })

  it('calls onEnter once per new key, and again after it left and came back', () => {
    const f = fakeClient()
    const store = createWaitingStore({ client: f.client })
    setWaitingStoreForTest(store)
    const entered: string[] = []
    store.onEnter((it) => entered.push(it.key))
    store.start()

    // The first frame is only the baseline: what waited before the page opened is not "new".
    f.push('waiting', { type: 'snap', data: [item('session:s1')] })
    expect(entered).toEqual([])

    // A second frame with the same set says nothing.
    f.push('waiting', { type: 'snap', data: [item('session:s1')] })
    expect(entered).toEqual([])

    // A new key enters.
    f.push('waiting', { type: 'snap', data: [item('session:s1'), item('job:j1', 'review')] })
    expect(entered).toEqual(['job:j1'])

    // The same key again (still listed) is not a second entry.
    f.push('waiting', { type: 'snap', data: [item('session:s1'), item('job:j1', 'review')] })
    expect(entered).toEqual(['job:j1'])

    // It leaves ...
    f.push('waiting', { type: 'snap', data: [item('session:s1')] })
    expect(entered).toEqual(['job:j1'])
    // ... and comes back: that is a new entry.
    f.push('waiting', { type: 'snap', data: [item('session:s1'), item('job:j1')] })
    expect(entered).toEqual(['job:j1', 'job:j1'])
  })

  it('keeps the list when the server sends a frame it cannot read', () => {
    const f = fakeClient()
    const store = createWaitingStore({ client: f.client })
    setWaitingStoreForTest(store)
    store.start()
    f.push('waiting', { type: 'snap', data: [item('session:s1')] })
    f.push('waiting', { type: 'snap', data: { not: 'an array' } })
    expect(store.items.value.map((it) => it.key)).toEqual(['session:s1'])
  })

  it('falls back to GET /v1/today and yields the same shape', async () => {
    const f = fakeClient()
    const card: TodayCard = {
      key: 'job:j9',
      kind: 'review',
      tag: 't',
      urgency: 'normal',
      blocks: { score: 0, items: 0 },
      title: '评审一下',
      project_key: 'p1',
      waiting_since: 42,
      activity_at: 42,
      summary: 'must not surface',
      refs: { job_id: 'j9' },
      actions: [],
      waiting: 'review',
      advice: null,
    }
    const { made, makePoller } = collectPoller()
    const store = createWaitingStore({
      client: f.client,
      makePoller,
      fetchToday: async () => ({ waiting: [card] }),
    })
    setWaitingStoreForTest(store)
    store.start()
    // The WS pushed nothing yet; the snapshot and the REST pull must agree.
    liveFallback.value = true
    await nextTick()
    expect(made.length).toBe(1)
    await made[0].fn()
    expect(store.items.value).toEqual([waitingItemOf(card)])
    expect(store.items.value[0]).toEqual({
      key: 'job:j9',
      category: 'review',
      kind: 'review',
      project_key: 'p1',
      waiting_since: 42,
      title: '评审一下',
    })
    expect(store.byKey('job:j9')?.title).toBe('评审一下')
    expect(store.byKey('job:nope')).toBeUndefined()
  })

  it('unsubscribes and clears when the store is replaced', () => {
    const f = fakeClient()
    const store = createWaitingStore({ client: f.client })
    setWaitingStoreForTest(store)
    useWaiting()
    expect(f.subs()).toBe(1)
    setWaitingStoreForTest(null)
    expect(f.subs()).toBe(0)
  })

  it('starts once no matter how often useWaiting is called', () => {
    const f = fakeClient()
    const store = createWaitingStore({ client: f.client })
    const spy = vi.spyOn(store, 'start')
    setWaitingStoreForTest(store)
    useWaiting()
    useWaiting()
    expect(f.subs()).toBe(1)
    expect(spy).toHaveBeenCalled()
  })
})
