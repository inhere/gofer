// 「工作项」页的列表口径（counts / 项目收窄 / 停滞 / 文案 key / 请求账本一行）。
import { describe, expect, it, beforeEach } from 'vitest'
import type { WorkItem } from '../api/types'
import { setLocale } from '../store/locale'
import {
  dueLabelKey,
  itemInProject,
  narrowByProject,
  requestKindLabelKey,
  requestStateLabelKey,
  sourceLabelKey,
  stalledDays,
  statusSourceLabelKey,
  suggestionLabelKey,
  workCounts,
  workRequestLine,
} from './workBoard'

function item(over: Partial<WorkItem> = {}): WorkItem {
  return {
    id: 'w-1', title: '一件事', goal: '', status: 'active', status_source: 'auto', source: 'human',
    unsorted: false, rev: 1, created_at: 0, updated_at: 0, last_activity_at: 1000, due: false,
    session_offline: false, sessions: [], session_ids: [], links: [], ...over,
  }
}

const NOW = 1_800_000_000

describe('workCounts', () => {
  it('counts only open items, and takes the unsorted count from the drafts', () => {
    const items = [
      item({ id: 'a', status: 'needs_me' }),
      item({ id: 'b', status: 'active', due: true }),
      item({ id: 'c', status: 'done', due: true }),
      item({ id: 'd', status: 'dropped' }),
      item({ id: 'e', status: 'review', due: true }),
    ]
    const counts = workCounts(items, [item({ id: 'u1' }), item({ id: 'u2' })])
    expect(counts).toEqual({ open: 3, needs_me: 1, due: 2, unsorted: 2 })
  })
})

describe('project narrowing', () => {
  it('empty project keeps everything, a key keeps only that project', () => {
    const items = [item({ id: 'a', project_key: 'self' }), item({ id: 'b', project_key: 'other' }), item({ id: 'c' })]
    expect(itemInProject(items[0], '')).toBe(true)
    expect(narrowByProject(items, '').length).toBe(3)
    expect(narrowByProject(items, 'self').map((i) => i.id)).toEqual(['a'])
    expect(narrowByProject(items, 'other').map((i) => i.id)).toEqual(['b'])
    expect(narrowByProject(items, 'missing')).toEqual([])
  })
})

describe('stalledDays', () => {
  it('flags open items with no activity for two days or more', () => {
    expect(stalledDays(item({ last_activity_at: NOW - 86400 }), NOW)).toBe(0)
    expect(stalledDays(item({ last_activity_at: NOW - 2 * 86400 - 60 }), NOW)).toBe(2)
    expect(stalledDays(item({ last_activity_at: NOW - 5 * 86400 }), NOW)).toBe(5)
  })

  it('ignores closed, parked, unsorted items and ones with an in-flight request', () => {
    expect(stalledDays(item({ status: 'done', last_activity_at: 0 }), NOW)).toBe(0)
    expect(stalledDays(item({ status: 'parked', last_activity_at: 0 }), NOW)).toBe(0)
    expect(stalledDays(item({ unsorted: true, last_activity_at: 0 }), NOW)).toBe(0)
    const waiting = item({
      last_activity_at: 0,
      requests: [{ id: 'r', work_item_id: 'w-1', session_id: 's1', kind: 'report', state: 'sent', by: 'human:me', created_at: 0 }],
    })
    expect(stalledDays(waiting, NOW)).toBe(0)
    // a finished request does not hold the item back
    expect(stalledDays({ ...waiting, requests: [{ ...waiting.requests![0], state: 'answered' }] }, NOW)).toBe(Math.floor(NOW / 86400))
  })
})

describe('label keys', () => {
  it('picks the due label by kind', () => {
    expect(dueLabelKey(item())).toBe('')
    expect(dueLabelKey(item({ due: true }))).toBe('work.card.dueRemind')
    expect(dueLabelKey(item({ due: true, status: 'parked', park_until: 1 }))).toBe('work.card.dueParked')
  })

  it('maps sources, status sources, suggestion fields and request kinds', () => {
    expect(sourceLabelKey('auto')).toBe('work.d.sourceAuto')
    expect(sourceLabelKey('steward')).toBe('work.d.sourceSteward')
    expect(sourceLabelKey('human')).toBe('work.d.sourceHuman')
    expect(statusSourceLabelKey('auto')).toBe('work.d.statusSrcAuto')
    expect(statusSourceLabelKey('human')).toBe('work.d.statusSrcHuman')
    expect(statusSourceLabelKey('report')).toBe('work.d.statusSrcReport')
    expect(statusSourceLabelKey(undefined)).toBe('')
    expect(suggestionLabelKey('goal')).toBe('work.o.suggest.goal')
    expect(suggestionLabelKey('status_hint')).toBe('work.o.suggest.status')
    expect(suggestionLabelKey('whatever')).toBe('work.o.suggest.other')
    expect(requestKindLabelKey('handoff')).toBe('work.o.request.handoff')
    expect(requestKindLabelKey('summarize')).toBe('work.o.request.summarize')
    expect(requestKindLabelKey('report')).toBe('work.o.request.report')
    expect(requestStateLabelKey('sent')).toBe('work.o.request.state.sent')
    expect(requestStateLabelKey('weird')).toBe('work.o.request.state.other')
  })
})

describe('workRequestLine', () => {
  beforeEach(() => setLocale('zh-CN'))

  it('says what a live request is waiting for', () => {
    const base = { id: 'r', work_item_id: 'w-1', session_id: 's1', by: 'human:me', created_at: 0 }
    expect(workRequestLine({ ...base, kind: 'report', state: 'sent', deadline: NOW + 900 }, NOW)).toBe(
      '汇报请求 · 已送达，等会话回复（15 分钟后超时）',
    )
    expect(workRequestLine({ ...base, kind: 'summarize', state: 'pending' }, NOW)).toBe('整理 · 进行中…')
    expect(workRequestLine({ ...base, kind: 'report', state: 'pending' }, NOW)).toBe('汇报请求 · 发送中…')
  })

  it('reports the outcome and appends the error when there is one', () => {
    const base = { id: 'r', work_item_id: 'w-1', session_id: 's1', by: 'system', created_at: 0 }
    expect(workRequestLine({ ...base, kind: 'report', state: 'answered' }, NOW)).toBe('汇报请求 · 会话已回复')
    expect(workRequestLine({ ...base, kind: 'summarize', state: 'answered' }, NOW)).toBe('整理 · 已完成')
    expect(workRequestLine({ ...base, kind: 'handoff', state: 'expired' }, NOW)).toBe('交接请求 · 会话未回应，已改为整理')
    expect(workRequestLine({ ...base, kind: 'report', state: 'failed', error: '整理器不可用' }, NOW)).toBe('汇报请求 · 失败：整理器不可用')
  })

  it('follows the locale', () => {
    try {
      setLocale('en-US')
      expect(workRequestLine({ id: 'r', work_item_id: 'w-1', session_id: 's1', kind: 'report', state: 'answered', by: 'system', created_at: 0 })).toBe(
        'Report request · the session replied',
      )
    } finally {
      setLocale('zh-CN')
    }
  })
})
