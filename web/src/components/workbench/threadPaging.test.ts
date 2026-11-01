import { describe, expect, it } from 'vitest'
import type { WorkbenchThread, WorkbenchThreadsResp } from '../../api/types'
import { dicts } from '../../i18n'
import {
  appendOlderPage,
  filtersKey,
  firstPageQuery,
  mergeOlderThreads,
  morePageQuery,
  threadsOf,
  WORKBENCH_ACTIVE_WITHIN_SEC,
  WORKBENCH_PAGE_SIZE,
} from './threadPaging'

function thread(id: string, project = 'p', updated = 10): WorkbenchThread {
  return {
    id,
    kind: 'agent',
    status: 'done',
    title: id,
    project_key: project,
    resumable: true,
    started_at: 1,
    updated_at: updated,
    turns: 1,
  }
}

function resp(threads: WorkbenchThread[], extra: Partial<WorkbenchThreadsResp> = {}): WorkbenchThreadsResp {
  const byProject = new Map<string, WorkbenchThread[]>()
  for (const t of threads) byProject.set(t.project_key, [...(byProject.get(t.project_key) ?? []), t])
  return {
    projects: [...byProject].map(([project_key, list]) => ({
      project_key,
      status: 'idle' as const,
      counts: { total: list.length, blocked: 0, working: 0, review: 0, done: list.length, idle: 0, orphan_blocked: 0, orphan_review: 0 },
      threads: list,
    })),
    attention: [],
    total: threads.length,
    since: 0,
    ...extra,
  }
}

describe('workbench list paging queries', () => {
  it('defaults to the last 7 days with a page limit', () => {
    expect(WORKBENCH_ACTIVE_WITHIN_SEC).toBe(7 * 86400)
    expect(firstPageQuery({})).toEqual({
      project: undefined,
      status: '',
      q: undefined,
      active_within: 7 * 86400,
      limit: WORKBENCH_PAGE_SIZE,
    })
  })

  it('keeps project and status filters on both the first page and load-more', () => {
    const filters = { project: 'alpha', status: 'blocked' as const }
    expect(firstPageQuery(filters)).toMatchObject({ project: 'alpha', status: 'blocked', active_within: 7 * 86400 })
    expect(morePageQuery(filters, 'CUR')).toEqual({
      project: 'alpha',
      status: 'blocked',
      cursor: 'CUR',
      limit: WORKBENCH_PAGE_SIZE,
    })
  })

  it('searches the whole history, unpaged', () => {
    const query = firstPageQuery({ q: '  deploy ' })
    expect(query.q).toBe('deploy')
    expect(query.since).toBe(1)
    expect(query.active_within).toBeUndefined()
    expect(query.limit).toBeUndefined()
  })

  it('changes the filter key when any filter changes', () => {
    const a = filtersKey({ project: 'x', status: '', q: '' })
    expect(filtersKey({ project: 'x', status: '', q: '  ' })).toBe(a)
    expect(filtersKey({ project: 'y', status: '', q: '' })).not.toBe(a)
    expect(filtersKey({ project: 'x', status: 'done', q: '' })).not.toBe(a)
    expect(filtersKey({ project: 'x', status: '', q: 'z' })).not.toBe(a)
  })
})

describe('older pages survive refreshes', () => {
  it('appends a page without duplicates', () => {
    const first = resp([thread('a'), thread('b')])
    const page = resp([thread('b'), thread('c'), thread('d')], { has_more: true, next_cursor: 'N' })
    const loaded = appendOlderPage([], page, new Set(threadsOf(first).map((t) => t.id)))
    expect(loaded.map((t) => t.id)).toEqual(['c', 'd'])
    const again = appendOlderPage(loaded, resp([thread('d'), thread('e')]), new Set(['a', 'b']))
    expect(again.map((t) => t.id)).toEqual(['c', 'd', 'e'])
  })

  it('merges older threads into the freshly refreshed first page', () => {
    const older = [thread('c', 'p', 3), thread('z', 'other', 2)]
    const refreshed = resp([thread('a'), thread('b')])
    const merged = mergeOlderThreads(refreshed, older)
    expect(threadsOf(merged).map((t) => t.id).sort()).toEqual(['a', 'b', 'c', 'z'])
    expect(merged.total).toBe(4)
    expect(merged.projects.map((g) => g.project_key)).toEqual(['other', 'p'])
    // the refreshed response itself is not mutated
    expect(threadsOf(refreshed).map((t) => t.id)).toEqual(['a', 'b'])
  })

  it('lets the refreshed first page win for an id that is in both', () => {
    const refreshed = resp([thread('c', 'p', 99)])
    const merged = mergeOlderThreads(refreshed, [thread('c', 'p', 3)])
    expect(threadsOf(merged)).toHaveLength(1)
    expect(threadsOf(merged)[0].updated_at).toBe(99)
  })

  it('returns the response untouched when nothing older is loaded', () => {
    const refreshed = resp([thread('a')])
    expect(mergeOlderThreads(refreshed, [])).toBe(refreshed)
  })
})

describe('load-more i18n', () => {
  it('has both labels in Chinese and English', () => {
    for (const dict of [dicts['zh-CN'], dicts['en-US']]) {
      expect(dict['workbench.list.loadMore']).toBeTruthy()
      expect(dict['workbench.list.loadingMore']).toBeTruthy()
    }
  })
})
