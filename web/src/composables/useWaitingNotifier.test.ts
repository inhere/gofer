import { afterEach, describe, expect, it } from 'vitest'
import { nextTick } from 'vue'
import { liveFallback, type LiveClient, type LiveHandler, type LiveMessage } from '../api/live'
import type { WaitingItem } from '../api/types'
import { createWaitingStore, setWaitingStoreForTest, type WaitingStore } from './useWaiting'
import { createWaitingNotifier, type NotifyHost, type WaitingNotifier } from './useWaitingNotifier'
import type { ElectionChannel } from '../utils/waitingNotify'

function fakeClient(): { client: LiveClient; push(data: unknown): void } {
  const handlers = new Set<LiveHandler>();
  return {
    client: {
      subscribe(_topic, h) {
        handlers.add(h)
        return () => {
          handlers.delete(h)
        }
      },
      noteSeq() {},
      status: () => 'connected',
      close() {},
    },
    push: (data) => handlers.forEach((h) => h({ type: 'snap', data } as LiveMessage)),
  }
}

// fakeBus 是 BroadcastChannel 的替身：同源标签页之间投递，但不回声（真实通道同样不送给自己）。
function fakeBus() {
  const entries = new Set<(msg: unknown) => void>()
  return {
    open(): ElectionChannel {
      const mine = new Set<(msg: unknown) => void>()
      const entry = (msg: unknown) => mine.forEach((cb) => cb(msg))
      entries.add(entry)
      return {
        postMessage(msg) {
          for (const other of entries) if (other !== entry) other(msg)
        },
        onMessage(cb) {
          mine.add(cb)
          return () => {
            mine.delete(cb)
          }
        },
        close() {
          entries.delete(entry)
        },
      }
    },
    live: () => entries.size,
  }
}

interface Shown {
  title: string
  body: string
  tag: string
  onClick: () => void
}

interface HostOpts {
  hidden?: boolean
  secure?: boolean
  permission?: string
  hasNotification?: boolean
  title?: string
}

// fakeHost 是 Notification / document 的替身：node 环境没有 window，能力全从 state 里读。
function fakeHost(opts: HostOpts = {}) {
  const state = {
    hidden: opts.hidden ?? true,
    secure: opts.secure ?? true,
    permission: opts.permission ?? 'granted',
    hasNotification: opts.hasNotification ?? true,
    title: opts.title ?? 'gofer',
    focused: 0,
    shown: [] as Shown[],
    visibility: new Set<() => void>(),
  }
  const host: NotifyHost = {
    secure: () => state.secure,
    hasNotification: () => state.hasNotification,
    permission: () => state.permission,
    hidden: () => state.hidden,
    title: () => state.title,
    setTitle: (title) => {
      state.title = title
    },
    show: (title, o) => state.shown.push({ title, body: o.body, tag: o.tag, onClick: o.onClick }),
    focus: () => {
      state.focused++
    },
    onVisibility: (cb) => {
      state.visibility.add(cb)
      return () => {
        state.visibility.delete(cb)
      }
    },
  }
  return { host, state }
}

interface Tab {
  client: { push(data: unknown): void }
  store: WaitingStore
  host: NotifyHost
  state: ReturnType<typeof fakeHost>['state']
  notifier: WaitingNotifier
  // 点通知后的跳转路径（router.push 的替身）。
  pushed: string[]
  push(items: WaitingItem[]): void
}

const tabs: Tab[] = []

function makeTab(
  id: string,
  bus: ReturnType<typeof fakeBus>,
  opts: HostOpts & { subscribed?: boolean } = {},
): Tab {
  const client = fakeClient()
  const store = createWaitingStore({ client: client.client })
  const { host, state } = fakeHost(opts)
  const pushed: string[] = []
  const notifier = createWaitingNotifier({
    store,
    host,
    subscribed: async () => opts.subscribed ?? false,
    openChannel: () => bus.open(),
    newID: () => id,
    router: { push: (to) => pushed.push(to) },
  })
  store.start()
  notifier.start()
  const tab: Tab = {
    client,
    store,
    host,
    state,
    notifier,
    pushed,
    push: (items) => client.push(items),
  }
  tabs.push(tab)
  return tab
}

function item(key: string, category: WaitingItem['category'] = 'answer', title?: string): WaitingItem {
  return { key, category, kind: 'relay', waiting_since: 100, title }
}

function shownCount(list: Tab[]): number {
  return list.reduce((n, tab) => n + tab.state.shown.length, 0)
}

describe('useWaitingNotifier', () => {
  afterEach(() => {
    for (const tab of tabs.splice(0)) tab.notifier.stop()
    setWaitingStoreForTest(null)
    liveFallback.value = false
  })

  it('two hidden tabs raise one notification: the smallest id sends', async () => {
    const bus = fakeBus()
    const second = makeTab('b-tab', bus)
    const first = makeTab('a-tab', bus)
    await Promise.all([second.notifier.refresh(), first.notifier.refresh()])

    // 首帧只建立基线（打开页面时已经在等的不弹）。
    second.push([item('session:s1')])
    first.push([item('session:s1')])
    // 新出现一条：两个标签页都收到，但只有一个发。
    second.push([item('session:s1'), item('job:j1', 'review')])
    first.push([item('session:s1'), item('job:j1', 'review')])

    expect(shownCount([second, first])).toBe(1)
    expect(first.state.shown.map((n) => n.tag)).toEqual(['job:j1'])
    expect(second.state.shown).toEqual([])
  })

  it('hands over to the other tab once the sender is closed', async () => {
    const bus = fakeBus()
    const second = makeTab('b-tab', bus)
    const first = makeTab('a-tab', bus)
    await Promise.all([second.notifier.refresh(), first.notifier.refresh()])
    second.push([])
    first.push([])

    // b-tab 先发（另一轮里最小 id 是 b-tab 时的情况由上面的用例覆盖）；这里让 a-tab 关掉。
    first.notifier.stop()
    second.push([item('job:j1', 'review')])
    expect(second.state.shown.map((n) => n.tag)).toEqual(['job:j1'])
  })

  it('stays silent when this browser has a Web Push subscription', async () => {
    const bus = fakeBus()
    const tab = makeTab('only', bus, { subscribed: true })
    await tab.notifier.refresh()
    expect(tab.notifier.mode()).toBe('push')
    tab.push([])
    tab.push([item('session:s1', 'answer', '周报')])
    expect(tab.state.shown).toEqual([])
    // 服务端会推，标题也不必计数。
    await nextTick()
    expect(tab.state.title).toBe('gofer')
  })

  it('stays silent while the page is visible', async () => {
    const bus = fakeBus()
    const tab = makeTab('only', bus, { hidden: false })
    await tab.notifier.refresh()
    tab.push([])
    tab.push([item('session:s1', 'answer', '周报')])
    expect(tab.state.shown).toEqual([])
  })

  it('falls back to the tab title counter without a secure context', async () => {
    const bus = fakeBus()
    const tab = makeTab('only', bus, { secure: false, hidden: false })
    await tab.notifier.refresh()
    expect(tab.notifier.mode()).toBe('title')
    tab.push([item('session:s1'), item('job:j1', 'review')])
    await nextTick()
    expect(tab.state.title).toBe('(2) gofer')
    // 处理掉一件：计数跟着变。
    tab.push([item('session:s1')])
    await nextTick()
    expect(tab.state.title).toBe('(1) gofer')
    // 全部处理完：恢复原标题。
    tab.push([])
    await nextTick()
    expect(tab.state.title).toBe('gofer')
    expect(tab.state.shown).toEqual([])
  })

  it('sends the subject key as tag, the category in the title, and no turn text', async () => {
    const bus = fakeBus()
    const tab = makeTab('only', bus)
    await tab.notifier.refresh()
    tab.push([])
    // 服务端不推回合正文；万一多带了字段，也不许进通知。
    const withExtra = { ...item('session:s1', 'answer', '周报'), summary: 'TURN TEXT MUST NOT LEAVE' } as WaitingItem
    tab.push([withExtra])

    expect(tab.state.shown).toHaveLength(1)
    const shown = tab.state.shown[0]
    expect(shown.tag).toBe('session:s1')
    expect(shown.title).toBe('周报 在等你回答')
    expect(shown.body).toBe('')
    expect(JSON.stringify(shown.title)).not.toContain('TURN TEXT')

    // 点通知：聚焦窗口并跳到那条会话的工作台线程。
    shown.onClick()
    expect(tab.state.focused).toBe(1)
    expect(tab.pushed).toEqual(['/workbench?thread=r%3As1'])
  })
})
