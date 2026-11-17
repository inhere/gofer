import { describe, expect, it, vi } from 'vitest'
import {
  countFor,
  historyQuery,
  historyTabs,
  type HistoryFilters,
} from '../utils/sessionHistory'
import type { WorkbenchSourceCounts, WorkbenchStatus, WorkbenchThread } from '../api/types'
import {
  defaultSessionAgent,
  defaultSessionRunner,
  rememberedProject,
  sessionSubmitOptions,
} from '../utils/newSessionDefaults'
import { canContinueThread, continueThread } from '../utils/sessionContinue'

// ---------------- 历史页（R13）：计数与列表一致 ----------------
//
// 用一个假的线程接口模拟服务端（同一套过滤条件先算 counts 再按 source 出列表，与
// workbench/filter.go 的 threadPassesFilters / threadCounts 同口径），验证页面拿到的
// 条数在任何页签与筛选下都等于这一页的行数——旧页签「切到别的页签计数变 0」的回归。

type Row = Pick<WorkbenchThread, 'id' | 'agent' | 'runner' | 'project_key' | 'status' | 'interactive' | 'kind'>

const DATASET: Row[] = [
  { id: 's:acp-1', kind: 'agent', status: 'working', project_key: 'p1', agent: 'claude-acp', runner: 'local' },
  { id: 's:acp-2', kind: 'agent', status: 'done', project_key: 'p1', agent: 'claude-acp', runner: 'w-docker' },
  { id: 's:acp-3', kind: 'agent', status: 'idle', project_key: 'p2', agent: 'codex-acp', runner: 'local' },
  { id: 's:pty-1', kind: 'agent', status: 'working', project_key: 'p1', agent: 'claude', runner: 'w-docker', interactive: true },
  { id: 'r:relay-1', kind: 'relay', status: 'blocked', project_key: 'p2', agent: 'claude', runner: 'local' },
]

function matches(row: Row, filters: HistoryFilters): boolean {
  if (filters.project && row.project_key !== filters.project) return false
  if (filters.agent && row.agent !== filters.agent) return false
  if (filters.runner && row.runner !== filters.runner) return false
  if (filters.status && row.status !== filters.status) return false
  if (filters.q && !row.id.includes(filters.q)) return false
  return true
}

function matchesSource(row: Row, source: string): boolean {
  if (!source) return true
  if (source === 'relay') return row.kind === 'relay'
  if (source === 'pty') return row.kind !== 'relay' && !!row.interactive
  return row.kind !== 'relay' && !row.interactive
}

// fakeThreads 按 historyQuery 造出的参数返回页与 counts，口径与服务端一致：
// counts 不受 source 与分页影响。
function fakeThreads(query: ReturnType<typeof historyQuery>, dataset: Row[] = DATASET) {
  const filters: HistoryFilters = {
    project: query.project,
    agent: query.agent,
    runner: query.runner,
    status: query.status as WorkbenchStatus | '',
    q: query.q,
  }
  const passing = dataset.filter((row) => matches(row, filters))
  const counts: WorkbenchSourceCounts = { acp: 0, pty: 0, relay: 0, all: passing.length }
  for (const row of passing) {
    if (row.kind === 'relay') counts.relay++
    else if (row.interactive) counts.pty++
    else counts.acp++
  }
  const limit = query.limit ?? 50
  const offset = query.cursor ? Number(query.cursor) : 0
  const rows = passing.filter((row) => matchesSource(row, query.source ?? ''))
  const page = rows.slice(offset, offset + limit)
  const next = offset + limit < rows.length ? String(offset + limit) : ''
  return {
    rows: page,
    counts: query.counts ? counts : undefined,
    has_more: !!next,
    next_cursor: next,
  }
}

const LABELS = { '': 'All', acp: 'ACP', pty: 'PTY', relay: 'Relay' } as const

describe('session history page', () => {
  it('keeps the per-tab counts equal to the rows each tab lists, under every filter', () => {
    const filterSets: HistoryFilters[] = [
      {},
      { project: 'p1' },
      { agent: 'claude-acp' },
      { runner: 'w-docker' },
      { status: 'done' },
      { project: 'p2', agent: 'codex-acp' },
    ]
    for (const filters of filterSets) {
      // 页面上第一次拉取（带 counts）：条数出自同一份过滤条件。
      const first = fakeThreads(historyQuery(filters, '', { limit: 50, counts: true }))
      expect(first.counts).toBeDefined()
      const tabs = historyTabs(first.counts, LABELS)
      for (const tab of tabs) {
        const source = tab.value as '' | 'acp' | 'pty' | 'relay'
        // 切换页签时的请求：带 counts（计数与 source 无关），列表按该 source 收窄。
        const page = fakeThreads(historyQuery(filters, source, { limit: 50, counts: true }))
        expect(page.rows.length).toBe(countFor(page.counts, source))
        expect(tab.count).toBe(countFor(page.counts, source))
      }
      // 全部页签的条数 = 各来源之和。
      const all = countFor(first.counts, '') ?? 0
      expect(all).toBe(
        (countFor(first.counts, 'acp') ?? 0) +
          (countFor(first.counts, 'pty') ?? 0) +
          (countFor(first.counts, 'relay') ?? 0),
      )
    }
  })

  it('counts cover every page, not just the loaded one', () => {
    const filters: HistoryFilters = {}
    const first = fakeThreads(historyQuery(filters, '', { limit: 2, counts: true }))
    expect(first.rows.length).toBe(2)
    // 第一页只有 2 行，但全部条数仍是整个时间窗（列表与计数口径不同：计数不分页）。
    expect(first.counts?.all).toBe(DATASET.length)
    const second = fakeThreads(historyQuery(filters, '', { limit: 2, cursor: first.next_cursor }))
    expect(second.rows.length).toBe(2)
    expect(second.counts).toBeUndefined()
  })

  it('sends agent, runner, status, query and source as explicit list parameters', () => {
    const query = historyQuery(
      { project: 'p1', agent: 'claude-acp', runner: 'w-docker', status: 'done', q: '  build  ' },
      'acp',
      { limit: 50, counts: true },
    )
    expect(query).toMatchObject({
      project: 'p1',
      agent: 'claude-acp',
      runner: 'w-docker',
      status: 'done',
      q: 'build',
      source: 'acp',
      counts: true,
      limit: 50,
      since: 1,
    })
    // 没有筛选时不发空串（空串会被当成有效过滤值）。
    const empty = historyQuery({}, '', {})
    expect(empty.project).toBeUndefined()
    expect(empty.source).toBeUndefined()
    expect(empty.counts).toBeUndefined()
  })
})

// ---------------- 新建会话默认值（R13） ----------------

const ACP_AGENTS = [
  { key: 'codex-acp', type: 'acp-agent' },
  { key: 'claude-acp', type: 'acp-agent' },
]
const RUNNERS = [
  { name: 'local', type: 'local' as const },
  { name: 'w-docker', type: 'worker' as const, worker_id: 'w-docker' },
  { name: 'w-plain', type: 'worker' as const, worker_id: 'w-plain' },
]

describe('new session defaults', () => {
  it('prefers claude-acp when the project allows it', () => {
    expect(defaultSessionAgent(ACP_AGENTS)).toBe('claude-acp')
    expect(defaultSessionAgent([{ key: 'codex-acp', type: 'acp-agent' }])).toBe('codex-acp')
    expect(defaultSessionAgent([])).toBe('')
  })

  it('picks an online container worker that supports the agent, else the local runner', () => {
    const workers = [
      { id: 'w-docker', connected: true, labels: ['container'], agents: ['claude-acp'] },
      // 带 container 但离线
      { id: 'w-plain', connected: false, labels: ['container'], agents: ['claude-acp'] },
    ]
    expect(defaultSessionRunner(RUNNERS, workers, 'claude-acp')).toBe('w-docker')

    // 在线的 container worker 不支持这个 agent → 本机
    const unsupported = [
      { id: 'w-docker', connected: true, labels: ['container'], agents: ['codex-acp'] },
      { id: 'w-plain', connected: true, labels: ['container'], agents: ['codex-acp'] },
    ]
    expect(defaultSessionRunner(RUNNERS, unsupported, 'claude-acp')).toBe('local')

    // 没有 container 标签的在线 worker 也不算
    const noLabel = [{ id: 'w-docker', connected: true, labels: ['gpu'], agents: ['claude-acp'] }]
    expect(defaultSessionRunner(RUNNERS, noLabel, 'claude-acp')).toBe('local')

    // 没有 local runner 时回落到第一个可用 runner
    expect(defaultSessionRunner([{ name: 'w-docker', type: 'worker', worker_id: 'w-docker' }], [], 'claude-acp')).toBe('w-docker')
  })

  it('remembers the last project only while it is still offered', () => {
    expect(rememberedProject(['a', 'b'], 'b')).toBe('b')
    expect(rememberedProject(['a', 'b'], 'gone')).toBe('')
    expect(rememberedProject(['a', 'b'], null)).toBe('')
  })

  it('maps the two new options to the submit fields', () => {
    expect(sessionSubmitOptions({ type: 'acp', tier: 'auto', keepDirLock: false })).toEqual({})
    expect(sessionSubmitOptions({ type: 'acp', tier: 'ask', keepDirLock: false })).toEqual({ permission_tier: 'ask' })
    expect(sessionSubmitOptions({ type: 'acp', tier: 'strict', keepDirLock: true })).toEqual({
      permission_tier: 'strict',
      keep_dir_lock: true,
    })
    // 终端会话不认这两个字段
    expect(sessionSubmitOptions({ type: 'pty', tier: 'ask', keepDirLock: true })).toEqual({})
  })
})

// ---------------- 接着聊（R14） ----------------

describe('continue an ended session', () => {
  const endedThread = {
    id: 's:acp-1',
    kind: 'agent' as const,
    status: 'done' as const,
    raw_status: 'done',
    resumable: true,
    latest_job_id: 'job-1',
  }

  it('only offers the action on an ended, resumable ACP thread', () => {
    expect(canContinueThread(endedThread)).toBe(true)
    expect(canContinueThread({ ...endedThread, raw_status: 'awaiting_input' })).toBe(false)
    expect(canContinueThread({ ...endedThread, resumable: false })).toBe(false)
    expect(canContinueThread({ ...endedThread, interactive: true })).toBe(false)
    expect(canContinueThread({ ...endedThread, kind: 'relay' })).toBe(false)
    expect(canContinueThread({ ...endedThread, latest_job_id: undefined })).toBe(false)
  })

  it('calls the existing resume endpoint in session mode with no prompt', async () => {
    vi.resetModules()
    const resumeJob = vi.fn().mockResolvedValue({ id: 'job-2', session_id: 'acp-1' })
    vi.doMock('../api/client', () => ({ resumeJob }))
    const mod = await import('../utils/sessionContinue')
    const job = await mod.continueThread(endedThread)
    expect(resumeJob).toHaveBeenCalledWith('job-1', '', { mode: 'session' })
    expect(job.session_id).toBe('acp-1')
    vi.doUnmock('../api/client')
    vi.resetModules()
  })

  it('refuses a thread without a job', async () => {
    await expect(continueThread({ latest_job_id: undefined })).rejects.toThrow()
  })
})
