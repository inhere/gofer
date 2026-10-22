// 时间线分页（utils/mailPaging.ts）：首屏只放最新的 6 封、往上翻插到上方、没有更早的就停、
// 刷新只补最新的，以及上方插入后的滚动位置修正。
import { describe, expect, it } from 'vitest'
import type { MailMessage } from '../api/types'
import {
  MAIL_TIMELINE_PAGE,
  oldestMailId,
  scrollTopAfterPrepend,
  timelineFirstPage,
  timelineNearBottom,
  timelinePage,
  timelineWithNewest,
  timelineWithOlder,
} from './mailPaging'

// msg 造一封够用的信：id 也可当时间用（服务端 id 随信件增长）。
function msg(id: number): MailMessage {
  return {
    id,
    thread_id: 't',
    from: { kind: 'caller', caller: 'bob' },
    to: { kind: 'session', id: 's1' },
    to_address: 'session:s1',
    body: `letter ${id}`,
    state: 'delivered',
    created_at: id,
  } as MailMessage
}

function ids(mails: MailMessage[]): number[] {
  return mails.map((m) => m.id)
}

describe('timelinePage', () => {
  it('首屏只要最新的一页：desc + 6 封，没有游标', () => {
    expect(timelinePage()).toEqual({ limit: 6, order: 'desc' })
    expect(MAIL_TIMELINE_PAGE).toBe(6)
  })

  it('加载更早的一页带上 before 游标（不再重取已有的）', () => {
    expect(timelinePage(MAIL_TIMELINE_PAGE, 42)).toEqual({ limit: 6, order: 'desc', before: 42 })
  })
})

describe('timelineFirstPage', () => {
  it('把服务端的倒序翻成正序，记下 has_more', () => {
    const state = timelineFirstPage([msg(30), msg(29), msg(28)], true)
    expect(ids(state.mails)).toEqual([28, 29, 30])
    expect(state.hasOlder).toBe(true)
  })

  it('has_more=false 就是没有更早的了', () => {
    const state = timelineFirstPage([msg(3), msg(2), msg(1)], false)
    expect(state.hasOlder).toBe(false)
    expect(state.mails).toHaveLength(3)
  })
})

describe('timelineWithOlder', () => {
  it('更早的一页插到上方，整体仍是时间正序', () => {
    const first = timelineFirstPage([msg(9), msg(8), msg(7)], true)
    const older = timelineWithOlder(first, [msg(6), msg(5), msg(4)], true)
    expect(ids(older.mails)).toEqual([4, 5, 6, 7, 8, 9])
    expect(older.hasOlder).toBe(true)
  })

  it('翻到底时 has_more 归 false', () => {
    const first = timelineFirstPage([msg(9), msg(7)], true)
    const older = timelineWithOlder(first, [msg(2), msg(1)], false)
    expect(ids(older.mails)).toEqual([1, 2, 7, 9])
    expect(older.hasOlder).toBe(false)
  })

  it('两页重叠的信只留一封', () => {
    const first = timelineFirstPage([msg(9), msg(8)], true)
    const older = timelineWithOlder(first, [msg(8), msg(7)], false)
    expect(ids(older.mails)).toEqual([7, 8, 9])
  })
})

describe('timelineWithNewest', () => {
  it('刷新只补最新的信，已加载的页原样留下', () => {
    const first = timelineFirstPage([msg(30), msg(29), msg(28)], true)
    const merged = timelineWithNewest(first, [msg(32), msg(31), msg(30)])
    expect(ids(merged.mails)).toEqual([28, 29, 30, 31, 32])
    // 已加载的更早几页还在，has_more 不变。
    expect(merged.hasOlder).toBe(true)
  })

  it('没有新信时状态不变（同一个对象，页面不重排）', () => {
    const first = timelineFirstPage([msg(30), msg(29)], false)
    expect(timelineWithNewest(first, [msg(30), msg(29)])).toBe(first)
    expect(ids(timelineWithNewest(first, [msg(30), msg(29)]).mails)).toEqual([29, 30])
  })

  it('已加载的那几封只更新状态（打开对话 ack 后显示已读）', () => {
    const first = timelineFirstPage([msg(30), msg(29)], true)
    const read = { ...msg(29), read_at: 100, state: 'read' } as MailMessage
    const merged = timelineWithNewest(first, [read, msg(30)])
    expect(ids(merged.mails)).toEqual([29, 30])
    expect(merged.mails[0].read_at).toBe(100)
    expect(merged.hasOlder).toBe(true)
  })

  it('空时间线直接用这一页顶上', () => {
    const merged = timelineWithNewest({ mails: [], hasOlder: false }, [msg(2), msg(1)])
    expect(ids(merged.mails)).toEqual([1, 2])
  })
})

describe('oldestMailId', () => {
  it('是 before= 游标：已加载最旧那封的 id（空列表 0）', () => {
    expect(oldestMailId([msg(7), msg(9), msg(8)])).toBe(7)
    expect(oldestMailId([])).toBe(0)
  })
})

describe('scrollTopAfterPrepend', () => {
  it('把内容变高的差值补到原滚动位置上（阅读位置不跳）', () => {
    // 插入更早的信后内容高了 320px，原来滚在 500：补成 820，看到的还是同一封信。
    expect(scrollTopAfterPrepend(500, 1000, 1320)).toBe(820)
  })

  it('没有变高（或量不到）时原样返回，负数按 0', () => {
    expect(scrollTopAfterPrepend(300, 1000, 1000)).toBe(300)
    expect(scrollTopAfterPrepend(300, 1000, 900)).toBe(300)
    expect(scrollTopAfterPrepend(-20, 0, 400)).toBe(0)
    expect(scrollTopAfterPrepend(Number.NaN, 0, 400)).toBe(0)
  })
})

describe('timelineNearBottom', () => {
  it('贴底时刷新的新信才把视图带下去', () => {
    expect(timelineNearBottom(1000, 600, 1600)).toBe(true)
    expect(timelineNearBottom(400, 600, 1600)).toBe(false)
    // 量不到高度时按贴底处理（首屏）。
    expect(timelineNearBottom(0, 0, 0)).toBe(true)
  })
})
