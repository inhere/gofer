import { describe, expect, it } from 'vitest'
import type { WorkItem, WorkSessionBrief } from '../api/types'
import {
  groupByStatus,
  groupByWorkspace,
  journalByLabel,
  linkTags,
  localInputToUnix,
  primarySession,
  reportRequestBlock,
  runningSessionIds,
  sortCards,
  statusLabel,
  unixToLocalInput,
  unsortedItems,
  visibleItems,
  whenPresets,
  workspaceLabel,
} from './work'

function item(over: Partial<WorkItem> = {}): WorkItem {
  return {
    id: 'w-1', title: 't', goal: '', status: 'active', status_source: 'auto', source: 'human', unsorted: false,
    rev: 1, created_at: 1, updated_at: 1, last_activity_at: 100, due: false, sessions: [], session_ids: [],
    session_offline: false, links: [], ...over,
  }
}
function sess(over: Partial<WorkSessionBrief> = {}): WorkSessionBrief {
  return { session_id: 's1', role: 'current', state: 'running', offline: false, ...over }
}

describe('status columns', () => {
  it('lists the six board columns in design order and appends closed ones on demand', () => {
    const cols = groupByStatus([], { showClosed: false })
    expect(cols.map((c) => c.status)).toEqual(['active', 'needs_me', 'waiting_resource', 'needs_onsite', 'review', 'parked'])
    expect(groupByStatus([], { showClosed: true }).map((c) => c.status).slice(-2)).toEqual(['done', 'dropped'])
    expect(statusLabel('needs_onsite')).toBe('需现场')
  })

  it('puts cards in their column, drafts only in the unsorted area, due cards first', () => {
    const items = [
      item({ id: 'a', status: 'needs_me', last_activity_at: 10 }),
      item({ id: 'b', status: 'needs_me', last_activity_at: 50 }),
      item({ id: 'c', status: 'needs_me', last_activity_at: 5, due: true }),
      item({ id: 'd', status: 'active', unsorted: true }),
    ]
    const cols = groupByStatus(items, { showClosed: false })
    const needs = cols.find((c) => c.status === 'needs_me')!
    expect(needs.items.map((i) => i.id)).toEqual(['c', 'b', 'a'])
    expect(cols.find((c) => c.status === 'active')!.items).toEqual([])
    expect(unsortedItems(items).map((i) => i.id)).toEqual(['d'])
    // filter mode keeps drafts in the columns
    expect(groupByStatus(items, { showClosed: false, includeUnsorted: true }).find((c) => c.status === 'active')!.items.map((i) => i.id)).toEqual(['d'])
  })
})

describe('filters', () => {
  const items = [
    item({ id: 'a', status: 'needs_me', title: '修登录' }),
    item({ id: 'b', status: 'done' }),
    item({ id: 'c', due: true, status: 'parked' }),
    item({ id: 'd', unsorted: true, goal: '导出订单' }),
  ]
  it('hides finished items unless asked and applies the badge filters', () => {
    expect(visibleItems(items, { filter: 'all', q: '', showClosed: false }).map((i) => i.id)).toEqual(['a', 'c', 'd'])
    expect(visibleItems(items, { filter: 'all', q: '', showClosed: true })).toHaveLength(4)
    expect(visibleItems(items, { filter: 'needs_me', q: '', showClosed: false }).map((i) => i.id)).toEqual(['a'])
    expect(visibleItems(items, { filter: 'due', q: '', showClosed: false }).map((i) => i.id)).toEqual(['c'])
    expect(visibleItems(items, { filter: 'unsorted', q: '', showClosed: false }).map((i) => i.id)).toEqual(['d'])
  })
  it('searches title, goal and ids case-insensitively', () => {
    expect(visibleItems(items, { filter: 'all', q: '登录', showClosed: false }).map((i) => i.id)).toEqual(['a'])
    expect(visibleItems(items, { filter: 'all', q: '导出', showClosed: false }).map((i) => i.id)).toEqual(['d'])
  })
})

describe('group by workspace', () => {
  it('groups one workspace together, needs_me workspaces first, statuses ordered inside', () => {
    const items = [
      item({ id: 'a', workspace: '/ws/alpha', status: 'active', last_activity_at: 9 }),
      item({ id: 'b', workspace: '/ws/alpha', status: 'needs_me', last_activity_at: 1 }),
      item({ id: 'c', workspace: '/ws/beta', status: 'active', last_activity_at: 99 }),
      item({ id: 'd', status: 'parked', project_key: 'self', last_activity_at: 1 }),
    ]
    const groups = groupByWorkspace(items)
    expect(groups.map((g) => g.label)).toEqual(['ws/alpha', 'ws/beta', '（self）'])
    expect(groups[0].items.map((i) => i.id)).toEqual(['b', 'a'])
  })
  it('labels windows paths and empty workspaces', () => {
    expect(workspaceLabel('D:\\work\\proj\\app')).toBe('proj/app')
    expect(workspaceLabel('')).toBe('（未指定工作区）')
  })
})

describe('sessions on a card', () => {
  it('finds running sessions and prefers one as the primary', () => {
    const it1 = item({ sessions: [sess({ session_id: 'old', state: 'ended', offline: true, last_seen_at: 9 }), sess({ session_id: 'live', state: 'idle', last_seen_at: 5 }), sess({ session_id: 'past', role: 'past' })] })
    expect(runningSessionIds(it1)).toEqual(['live'])
    expect(primarySession(it1)?.session_id).toBe('live')
    expect(primarySession(item())).toBeUndefined()
  })
  it('greys "ask it to report" out with a reason unless a session is running', () => {
    expect(reportRequestBlock(item({ sessions: [sess()] }))).toBe('')
    expect(reportRequestBlock(item({ sessions: [sess({ state: 'offline', offline: true })] }))).toContain('二期')
    expect(reportRequestBlock(item())).toContain('没有关联会话')
  })
  it('summarises links as small tags', () => {
    const it1 = item({ links: [{ kind: 'issue', ref: 'A' }, { kind: 'issue', ref: 'B' }, { kind: 'job', ref: 'j' }] })
    expect(linkTags(it1)).toEqual(['issue ×2', 'job'])
  })
})

describe('time helpers', () => {
  it('offers quick presets and skips "tonight" after 18:00', () => {
    const morning = whenPresets(new Date(2026, 9, 5, 10, 0, 0))
    expect(morning.map((p) => p.key)).toEqual(['1h', '3h', 'tonight', 'tomorrow', 'monday'])
    const evening = whenPresets(new Date(2026, 9, 5, 20, 0, 0))
    expect(evening.map((p) => p.key)).not.toContain('tonight')
    const tomorrow = morning.find((p) => p.key === 'tomorrow')!
    expect(new Date(tomorrow.at * 1000).getDate()).toBe(6)
    expect(new Date(tomorrow.at * 1000).getHours()).toBe(9)
    const monday = morning.find((p) => p.key === 'monday')!
    expect(new Date(monday.at * 1000).getDay()).toBe(1)
  })
  it('round-trips datetime-local values in local time', () => {
    const sec = localInputToUnix('2026-10-08T09:30')
    expect(unixToLocalInput(sec)).toBe('2026-10-08T09:30')
    expect(localInputToUnix('')).toBe(0)
    expect(localInputToUnix('nonsense')).toBe(0)
    expect(unixToLocalInput(0)).toBe('')
  })
  it('names journal authors', () => {
    expect(journalByLabel('human:me')).toBe('我')
    expect(journalByLabel('session:abcdef0123456789')).toBe('会话 abcdef01')
    expect(journalByLabel('system')).toBe('系统')
  })
})

describe('sortCards', () => {
  it('is stable on ties by id', () => {
    const s = sortCards([item({ id: 'b', last_activity_at: 1 }), item({ id: 'a', last_activity_at: 1 })])
    expect(s.map((i) => i.id)).toEqual(['a', 'b'])
  })
})
