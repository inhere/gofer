import { beforeEach, describe, expect, it } from 'vitest'
import type { WaitingItem } from '../api/types'
import { locale } from '../store/locale'
import {
  LEASE_MS,
  PING_MS,
  createTabElection,
  liveTabs,
  notifyMode,
  senderOf,
  shouldNotifyOnEnter,
  stripCount,
  titleWithCount,
  waitingNotification,
  waitingTitle,
  waitingURL,
  type ElectionChannel,
  type NotifyEnv,
  type SeenTab,
} from './waitingNotify'

function item(key: string, category: WaitingItem['category'], title?: string): WaitingItem {
  return { key, category, kind: 'relay', waiting_since: 100, title }
}

function env(over: Partial<NotifyEnv> = {}): NotifyEnv {
  return { pushSubscribed: false, secure: true, hasNotification: true, permission: 'granted', hidden: true, ...over }
}

// fakeBus 是 BroadcastChannel 的替身：postMessage 只送到**别的**标签页（真实通道不回声）。
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
          return () => mine.delete(cb)
        },
        close() {
          entries.delete(entry)
        },
      }
    },
    live: () => entries.size,
  }
}

describe('waitingNotify · which channel', () => {
  beforeEach(() => {
    locale.value = 'zh-CN'
  })

  it('subscribed browsers leave the job to the server (Web Push)', () => {
    // 订阅了就服务端推（T2），前端一条都不发，标题也不加计数。
    expect(notifyMode(env({ pushSubscribed: true }))).toBe('push')
    expect(shouldNotifyOnEnter(env({ pushSubscribed: true }))).toBe(false)
  })

  it('secure + granted + hidden sends locally; visible never does', () => {
    expect(notifyMode(env())).toBe('system')
    expect(shouldNotifyOnEnter(env())).toBe(true)
    expect(shouldNotifyOnEnter(env({ hidden: false }))).toBe(false)
  })

  it('insecure context / no Notification / permission not granted fall back to the title counter', () => {
    expect(notifyMode(env({ secure: false }))).toBe('title')
    expect(notifyMode(env({ hasNotification: false }))).toBe('title')
    expect(notifyMode(env({ permission: 'default' }))).toBe('title')
    expect(notifyMode(env({ permission: 'denied' }))).toBe('title')
    expect(shouldNotifyOnEnter(env({ secure: false }))).toBe(false)
  })

  it('counts the title only above zero and strips our own prefix', () => {
    expect(titleWithCount(0, 'gofer')).toBe('gofer')
    expect(titleWithCount(3, 'gofer')).toBe('(3) gofer')
    // 再叠一层会把「(2)」变成「(2) (1) gofer」，基准标题必须先剥前缀。
    expect(stripCount('(2) gofer')).toBe('gofer')
    expect(stripCount('gofer')).toBe('gofer')
    expect(stripCount('(12) workbench')).toBe('workbench')
  })
})

describe('waitingNotify · one tab sends', () => {
  it('picks the smallest id among hidden tabs and ignores visible ones', () => {
    expect(senderOf([{ id: 'b', hidden: true }, { id: 'a', hidden: true }])).toBe('a')
    // 可见的标签页不参与选主，也不挡住隐藏的那个。
    expect(senderOf([{ id: 'a', hidden: false }, { id: 'b', hidden: true }])).toBe('b')
    expect(senderOf([{ id: 'a', hidden: false }])).toBe('')
    expect(senderOf([])).toBe('')
  })

  it('drops peers that stopped sending heartbeats, so the next tab takes over', () => {
    const both = new Map<string, SeenTab>([
      ['a', { hidden: true, at: 0 }],
      ['b', { hidden: true, at: 0 }],
    ])
    // 还在租约内：id 小的 a 发。
    expect(senderOf(liveTabs(both, PING_MS))).toBe('a')
    // a 三个心跳都没出声（关掉了 / 崩了），b 还在：只剩 b，它接管。
    const stale = new Map<string, SeenTab>([
      ['a', { hidden: true, at: 0 }],
      ['b', { hidden: true, at: LEASE_MS }],
    ])
    expect(liveTabs(stale, LEASE_MS + 1000).map((tab) => tab.id)).toEqual(['b'])
    expect(senderOf(liveTabs(stale, LEASE_MS + 1000))).toBe('b')
  })

  it('handshake makes both tabs know each other, and bye hands over at once', () => {
    const bus = fakeBus()
    const a = createTabElection({ id: 'a', channel: bus.open(), hidden: () => true })
    const b = createTabElection({ id: 'b', channel: bus.open(), hidden: () => true })
    a.start()
    b.start()
    // 启动时互通了一声：两边都认得对方，结论一致。
    expect(a.sender()).toBe('a')
    expect(b.sender()).toBe('a')
    expect(b.isSender()).toBe(false)
    b.stop()
    expect(a.sender()).toBe('a')
    a.stop()
    expect(bus.live()).toBe(0)
  })
})

describe('waitingNotify · payload', () => {
  beforeEach(() => {
    locale.value = 'zh-CN'
  })

  it('titles carry the name and the category only', () => {
    expect(waitingTitle(item('session:s1', 'answer', '周报'))).toBe('周报 在等你回答')
    expect(waitingTitle(item('job:j1', 'approval', '周报'))).toBe('周报 在等你批准')
    expect(waitingTitle(item('job:j1', 'review', '周报'))).toBe('周报 在等你验收')
    // 没有标题时退到主体 key，标题绝不会是空的。
    expect(waitingTitle(item('plan:p1', 'plan_stuck'))).toBe('plan:p1 的计划卡住了')
  })

  it('tag is the subject key and the body stays empty', () => {
    const n = waitingNotification(item('session:s1', 'answer', '周报'))
    expect(n.tag).toBe('session:s1')
    expect(n.body).toBe('')
    expect(n.title).not.toContain('回合')
  })

  it('deeplinks: session thread, job detail, inbox for the rest', () => {
    expect(waitingURL(item('session:sess-9', 'answer'))).toBe('/workbench?thread=r%3Asess-9')
    expect(waitingURL(item('job:j-1', 'review'))).toBe('/jobs/j-1')
    expect(waitingURL(item('plan:p-1', 'plan_stuck'))).toBe('/inbox')
  })
})
