import { describe, expect, it } from 'vitest'
import type { InboxItem, InboxResponse } from '../api/inbox'
import type { TodayCard, TodayCardKind } from '../api/today'
import type { WaitingItem } from '../api/types'
import {
  POP_KINDS,
  inboxItemOf,
  inboxSeenOf,
  popApproveAction,
  popEnter,
  popExtra,
  popLink,
  popQueueDrop,
  popQueueEnter,
  popQueueReconcile,
  popReplyAction,
  popSnoozeOption,
  shouldPop,
  subjectKeyOf,
  subjectOf,
} from './waitingPop'

function item(key: string, kind: TodayCardKind = 'relay'): WaitingItem {
  return { key, category: 'answer', kind, waiting_since: 100, title: key }
}

function card(over: Partial<TodayCard> = {}): TodayCard {
  return {
    key: 'relay:s1',
    kind: 'relay',
    tag: '会话',
    urgency: 'normal',
    blocks: { score: 0, items: 0 },
    title: '周报',
    waiting_since: 100,
    activity_at: 100,
    summary: '这周做了什么',
    refs: { session_id: 's1', thread_id: 'r:s1' },
    actions: [
      { id: 'reply', label: '回复', needs_text: true },
      { id: 'ack', label: '已读' },
    ],
    advice: null,
    ...over,
  }
}

function inboxItem(key: string, state: InboxItem['state'], over: Partial<InboxItem> = {}): InboxItem {
  return {
    key,
    column: 'action',
    kind: 'relay',
    title: key,
    summary: '',
    source_revision: 'r1',
    state,
    actions: [],
    refs: {},
    activity_at: 0,
    ...over,
  }
}

function inbox(items: InboxItem[]): InboxResponse {
  return { columns: { action: items, fyi: [] }, unread: { action: 0, fyi: 0 }, total_unread: 0, generated_at: 0 }
}

// 弹卡只认「会话主体 + 等回复 / 等批准」这几类。
describe('waitingPop · 哪些弹', () => {
  it.each(['relay', 'acp_reply', 'permission', 'interaction', 'approval'] as TodayCardKind[])(
    'pops the session kinds: %s',
    (kind) => {
      expect(shouldPop(item('session:s1', kind))).toBe(true)
      expect(shouldPop(item('job:j1', kind))).toBe(true)
    },
  )

  it.each([
    'review',
    'job.failed',
    'job.child_wake_failed',
    'caller.token_expiring',
    'server.health',
    'schedule.paused',
    'seat_schedule',
    'todo_awaiting_merge',
    'plan_blocked',
    'mail_held',
    'work',
    'suggestion',
    'merge',
    'capture',
    'worker_enroll',
    'decision',
  ] as TodayCardKind[])('never pops %s', (kind) => {
    expect(shouldPop(item('session:s1', kind))).toBe(false)
    expect(shouldPop(item('job:j1', kind))).toBe(false)
  })

  it('only pops session subjects: a plan / decision / seat is left to the today page and the inbox', () => {
    for (const key of ['decision:d1', 'plan:p1', 'todo:t1', 'seat:seat1', 'caller:c1', 'schedule:sc1']) {
      expect(shouldPop(item(key, 'interaction'))).toBe(false)
    }
    expect(shouldPop(item('session:s1'))).toBe(true)
    expect(shouldPop(item('job:j1'))).toBe(true)
    // 冒号前的整段才算主体（"jobx:" 不是 job）
    expect(subjectOf('jobx:1')).toBe('jobx')
    expect(subjectOf('nocolon')).toBe('')
    expect(POP_KINDS).toEqual(['relay', 'acp_reply', 'permission', 'interaction', 'approval'])
  })
})

// 今天页的卡 / 收件箱条目带自己的 key，推送里的 key 是折叠后的主体 key。
describe('waitingPop · 主体 key', () => {
  it('folds a session card by session id and a job card by job id (same as the server)', () => {
    expect(subjectKeyOf({ kind: 'relay', refs: { session_id: 's1' } })).toBe('session:s1')
    expect(subjectKeyOf({ kind: 'permission', refs: { session_id: 's1' } })).toBe('session:s1')
    expect(subjectKeyOf({ kind: 'acp_reply', refs: { job_id: 'j1' } })).toBe('job:j1')
    expect(subjectKeyOf({ kind: 'interaction', refs: { job_id: 'j1' } })).toBe('job:j1')
    expect(subjectKeyOf({ kind: 'approval', refs: { job_id: 'j1' } })).toBe('job:j1')
    // 不弹的类型与缺 id 的卡不折
    expect(subjectKeyOf({ kind: 'review', refs: { job_id: 'j1' } })).toBe('')
    expect(subjectKeyOf({ kind: 'relay', refs: {} })).toBe('')
  })
})

// 队列按进入顺序，其余 +N；当前这件消失就换下一件。
describe('waitingPop · 队列与 +N', () => {
  const keys = (...k: string[]) => k

  it('queues in arrival order and never twice', () => {
    let q = popQueueEnter([], 'session:s1')
    q = popQueueEnter(q, 'job:j1')
    q = popQueueEnter(q, 'session:s1')
    expect(q).toEqual(keys('session:s1', 'job:j1'))
  })

  it('shows +N for the rest behind the current one', () => {
    expect(popExtra([])).toBe(0)
    expect(popExtra(['session:s1'])).toBe(0)
    expect(popExtra(['session:s1', 'job:j1', 'job:j2'])).toBe(2)
  })

  it('switches to the next one when the current item leaves the waiting list', () => {
    let q = popEnter([], item('session:s1'), false)
    q = popEnter(q, item('job:j1', 'approval'), false)
    expect(q).toEqual(['session:s1', 'job:j1'])
    // 第一件被答复 / 别人处理了：列表里没有它了
    q = popQueueReconcile(q, new Set(['job:j1']))
    expect(q).toEqual(['job:j1'])
    // 排队的项消失后也不再弹
    expect(popQueueReconcile(q, new Set())).toEqual([])
  })

  it('drops the current one right away when it is answered / snoozed / opened', () => {
    expect(popQueueDrop(['session:s1', 'job:j1'], 'session:s1')).toEqual(['job:j1'])
    expect(popQueueDrop(['job:j1'], 'session:s1')).toEqual(['job:j1'])
  })

  it('does not queue non-session or already-seen items', () => {
    expect(popEnter([], item('plan:p1', 'interaction'), false)).toEqual([])
    expect(popEnter([], item('job:j1', 'review'), false)).toEqual([])
    // 另一个浏览器已经关过（收件箱里是 seen）：不再弹、也不把 +N 撑大
    expect(popEnter([], item('session:s1'), true)).toEqual([])
  })
})

// 关闭写的是收件箱的个人状态，seen 的多端同步靠这一条。
describe('waitingPop · 已 seen 的不弹', () => {
  it('reads the inbox state of a subject, both columns included', () => {
    const data = inbox([inboxItem('relay:s1', 'unread', { kind: 'relay', refs: { session_id: 's1' } })])
    expect(inboxSeenOf(data, 'session:s1')).toBe('unread')
    expect(inboxSeenOf(data, 'session:s9')).toBe('unknown')
    expect(inboxSeenOf(null, 'session:s1')).toBe('unknown')

    const seen = inbox([inboxItem('relay:s1', 'seen', { kind: 'relay', refs: { session_id: 's1' } })])
    expect(inboxSeenOf(seen, 'session:s1')).toBe('seen')
    const cleared = inbox([inboxItem('relay:s1', 'cleared', { kind: 'relay', refs: { session_id: 's1' } })])
    expect(inboxSeenOf(cleared, 'session:s1')).toBe('seen')

    // 「仅知会」栏也要看
    const fyi: InboxResponse = {
      columns: { action: [], fyi: [inboxItem('relay:s1', 'seen', { column: 'fyi', refs: { session_id: 's1' } })] },
      unread: { action: 0, fyi: 0 },
      total_unread: 0,
      generated_at: 0,
    }
    expect(inboxSeenOf(fyi, 'session:s1')).toBe('seen')
  })

  it('finds the inbox entry behind a subject so close() writes the right key', () => {
    const data = inbox([inboxItem('approval:j1', 'unread', { kind: 'approval', refs: { job_id: 'j1' } })])
    expect(inboxItemOf(data, 'job:j1')?.key).toBe('approval:j1')
    expect(inboxItemOf(data, 'job:nope')).toBeNull()
    expect(inboxItemOf(null, 'job:j1')).toBeNull()
  })
})

// 弹卡上的按钮就是对今天页卡片动作的白名单。
describe('waitingPop · 按钮对应的动作与跳转', () => {
  it('takes the reply action that wants text and the approve action styled ok', () => {
    const c = card()
    expect(popReplyAction(c)?.id).toBe('reply')
    expect(popApproveAction(c)).toBeNull()
    const approval = card({ kind: 'approval', actions: [{ id: 'approve', label: '批准', style: 'ok' }, { id: 'reject', label: '拒绝', style: 'bad' }] })
    expect(popApproveAction(approval)?.id).toBe('approve')
    expect(popReplyAction(approval)).toBeNull()
    expect(popApproveAction(null)).toBeNull()
    expect(popReplyAction(null)).toBeNull()
  })

  it('opens the thread of a session and the job detail of a job, with or without the card', () => {
    expect(popLink(item('session:s1'), card())).toBe('/workbench?thread=r%3As1')
    expect(popLink(item('session:s1'), null)).toBe('/workbench?thread=r%3As1')
    expect(popLink(item('job:j1'), null)).toBe('/jobs/j1')
    expect(popLink(item('plan:p1'), null)).toBe('')
  })

  it('snoozes with the first (1 hour) option of the today page', () => {
    const opt = popSnoozeOption(card())
    expect(opt?.id).toBe('hour')
    expect(opt?.until_at).toBeGreaterThan(Math.floor(Date.now() / 1000))
  })
})
