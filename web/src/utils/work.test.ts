import { describe, expect, it } from 'vitest'
import type { WorkItem, WorkSessionBrief } from '../api/types'
import {
  linkedTodo,
  groupByStatus,
  groupByWorkspace,
  actorKind,
  actorLabel,
  fieldSourceText,
  inflightRequests,
  journalByLabel,
  lastFinishedRequest,
  linkTags,
  localInputToUnix,
  pendingSuggestions,
  primarySession,
  requestLine,
  reportRequestBlock,
  runningSessionIds,
  summarizeBlock,
  sortCards,
  statusLabel,
  suggestionLabel,
  suggestionValueText,
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
  it('lets "ask it to report" through for any current session (not running => tidied up instead)', () => {
    expect(reportRequestBlock(item({ sessions: [sess()] }))).toBe('')
    expect(reportRequestBlock(item({ sessions: [sess({ state: 'offline', offline: true })] }))).toBe('')
    expect(reportRequestBlock(item({ sessions: [sess({ role: 'past' })] }))).toContain('没有关联会话')
    expect(reportRequestBlock(item())).toContain('没有关联会话')
    expect(summarizeBlock(item({ sessions: [sess()] }))).toBe('')
    expect(summarizeBlock(item({ sessions: [sess({ missing: true })] }))).toContain('没有可读取的会话')
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

describe('speaker labels (W2a)', () => {
  it('classifies and names every speaker form', () => {
    expect(actorKind('human:alice')).toBe('human')
    expect(actorKind('human')).toBe('human')
    expect(actorKind('session:abcdef0123456789(claude)')).toBe('session')
    expect(actorKind('summarizer(claude)')).toBe('summarizer')
    expect(actorKind('steward(codex-acp)')).toBe('steward')
    expect(actorKind('job:j1')).toBe('job')
    expect(actorKind('')).toBe('system')
    expect(actorKind('mcp')).toBe('other')
    expect(actorLabel('human:alice')).toBe('我')
    expect(actorLabel('session:abcdef0123456789(claude)')).toBe('会话 abcdef01 (claude)')
    expect(actorLabel('summarizer(claude)')).toBe('整理器 (claude)')
    expect(actorLabel('steward(codex-acp)')).toBe('管家 (codex-acp)')
    expect(actorLabel('steward')).toBe('管家')
    expect(journalByLabel('job:abcdef0123456')).toBe('job abcdef01')
  })

  it('says who wrote a field and when', () => {
    const it1 = item({ field_sources: { goal: { by: 'summarizer(claude)', at: 940 }, next: { by: 'human:me', at: 990 } } })
    expect(fieldSourceText(it1, 'goal', 1000)).toBe('整理器 (claude) · 1m前')
    expect(fieldSourceText(it1, 'next', 1000)).toContain('我')
    expect(fieldSourceText(it1, 'blocker', 1000)).toBe('')
    expect(fieldSourceText(item(), 'goal', 1000)).toBe('')
  })
})

describe('suggestions and the request ledger (W2a)', () => {
  const req = (over: Record<string, unknown> = {}) => ({
    id: 'wr-1', work_item_id: 'w-1', session_id: 's1', kind: 'report', state: 'sent', by: 'human:me', created_at: 100, deadline: 1900, ...over,
  }) as never

  it('lists only pending suggestions and renders the status hint as a label', () => {
    const it1 = item({
      suggestions: [
        { field: 'goal', value: 'g', by: 'summarizer(claude)', at: 1, state: 'pending' },
        { field: 'status_hint', value: 'needs_onsite', by: 'summarizer(claude)', at: 1, state: 'pending' },
        { field: 'next', value: 'n', by: 'summarizer(claude)', at: 1, state: 'dismissed' },
      ],
    })
    const sg = pendingSuggestions(it1)
    expect(sg.map((s) => s.field)).toEqual(['goal', 'status_hint'])
    expect(suggestionLabel('blocker_kind')).toBe('阻塞类型')
    expect(suggestionValueText(sg[1])).toBe('需现场')
    expect(suggestionValueText(sg[0])).toBe('g')
  })

  it('tells in-flight requests from the last finished one and phrases each state', () => {
    const it1 = item({ requests: [req(), req({ id: 'wr-2', state: 'expired', created_at: 50 }), req({ id: 'wr-3', kind: 'summarize', state: 'answered', created_at: 70 })] })
    expect(inflightRequests(it1).map((r) => r.id)).toEqual(['wr-1'])
    expect(lastFinishedRequest(it1)?.id).toBe('wr-3')
    expect(requestLine(req(), 1000)).toBe('汇报请求 · 已送达，等会话回复（15 分钟后超时）')
    expect(requestLine(req({ state: 'pending', kind: 'handoff' }), 1000)).toBe('交接请求 · 发送中…')
    expect(requestLine(req({ state: 'expired' }))).toBe('汇报请求 · 会话未回应，已改为整理')
    expect(requestLine(req({ kind: 'summarize', state: 'pending' }))).toBe('整理 · 进行中…')
    expect(requestLine(req({ kind: 'summarize', state: 'failed', error: '整理器不可用' }))).toBe('整理 · 失败：整理器不可用')
    expect(requestLine(req({ state: 'answered' }))).toBe('汇报请求 · 会话已回复')
  })
})

describe('linkedTodo', () => {
  it('finds the converted todo and its plan', () => {
    expect(linkedTodo([{ kind: 'issue', ref: 'I-1' }])).toBeNull()
    expect(linkedTodo([{ kind: 'todo', ref: 't1' }, { kind: 'plan', ref: 'p1' }])).toEqual({ todoId: 't1', planId: 'p1' })
    expect(linkedTodo([{ kind: 'todo', ref: 't1' }])).toEqual({ todoId: 't1', planId: '' })
  })
})
