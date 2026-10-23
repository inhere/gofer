// 记忆页的纯逻辑（utils/memories.ts，issue gofer-hez1）：URL 契约、旧链接重定向、响应归一与筛选。
import { describe, expect, it } from 'vitest'
import {
  addMemoryTag,
  filterMemories,
  legacyMemoryQuery,
  MEMORY_PAGE_SIZE_KEY,
  memoryDoctorOf,
  memoryEditOf,
  memoryPageCount,
  memoryPageSlice,
  memoryQueryUpdate,
  memoryRowKey,
  memoryRowsOf,
  memorySourceKey,
  memorySourceLabel,
  memoryUpdateBody,
  mergeMemoryBatches,
  mergeMemoryDoctor,
  pageQueryUpdate,
  readMemoryKey,
  readMemoryPage,
  readMemoryProject,
  readMemoryScope,
  readMemorySearch,
  removeMemoryTag,
  searchQueryUpdate,
} from './memories'
import { setLocale } from '../store/locale'

describe('作用域 / 项目 / 关键字的 URL 契约', () => {
  it('reads the scope from ?memory_scope= and falls back to the repository', () => {
    expect(readMemoryScope({ memory_scope: 'global' })).toBe('global')
    expect(readMemoryScope({ memory_scope: 'project' })).toBe('project')
    expect(readMemoryScope({ memory_scope: 'repo' })).toBe('repo')
    // 「全部项目」范围
    expect(readMemoryScope({ memory_scope: 'all' })).toBe('all')
    // 手改过的值 / 没有参数：回落到仓库
    expect(readMemoryScope({ memory_scope: 'nope' })).toBe('repo')
    expect(readMemoryScope({})).toBe('repo')
    expect(readMemoryScope(undefined)).toBe('repo')
  })

  it('reads the project, the deep-linked key and the search keyword', () => {
    expect(readMemoryProject({ memory_project: 'gofer' })).toBe('gofer')
    expect(readMemoryProject({})).toBe('')
    expect(readMemoryKey({ memory: 'release-flow' })).toBe('release-flow')
    expect(readMemorySearch({ q: 'deploy' })).toBe('deploy')
    expect(readMemorySearch({})).toBe('')
  })

  it('writes the scope and keeps the other parameters', () => {
    expect(memoryQueryUpdate({ tracker: 'trk' }, 'global', 'gofer')).toEqual({ tracker: 'trk', memory_scope: 'global' })
    expect(memoryQueryUpdate({}, 'project', 'gofer')).toEqual({ memory_scope: 'project', memory_project: 'gofer' })
    // 项目作用域但没选项目：不写空的 memory_project
    expect(memoryQueryUpdate({}, 'project', '')).toEqual({ memory_scope: 'project' })
    // 换回仓库时去掉项目参数
    expect(memoryQueryUpdate({ memory_project: 'gofer' }, 'repo', 'gofer')).toEqual({ memory_scope: 'repo' })
  })

  it('writes the search keyword into ?q= and drops it when empty', () => {
    expect(searchQueryUpdate({ memory_scope: 'global' }, 'deploy')).toEqual({ memory_scope: 'global', q: 'deploy' })
    expect(searchQueryUpdate({ memory_scope: 'global', q: 'old' }, '')).toEqual({ memory_scope: 'global' })
  })

  it('reads the page from ?page= and keeps it out of the URL on page 1', () => {
    expect(readMemoryPage({ page: '3' })).toBe(3)
    expect(readMemoryPage({ page: '0' })).toBe(1)
    expect(readMemoryPage({ page: 'abc' })).toBe(1)
    expect(readMemoryPage({})).toBe(1)
    expect(pageQueryUpdate({ memory_scope: 'all', q: 'deploy' }, 2)).toEqual({ memory_scope: 'all', q: 'deploy', page: '2' })
    expect(pageQueryUpdate({ memory_scope: 'all', page: '4' }, 1)).toEqual({ memory_scope: 'all' })
  })

  it('slices and counts pages of a flat list', () => {
    const rows = Array.from({ length: 7 }, (_, i) => i)
    expect(memoryPageSlice(rows, 3, 1)).toEqual([0, 1, 2])
    expect(memoryPageSlice(rows, 3, 3)).toEqual([6])
    // 页码超出末页时回到末页，空列表也至少有 1 页
    expect(memoryPageSlice(rows, 3, 99)).toEqual([6])
    expect(memoryPageCount(0, 50)).toBe(1)
    expect(memoryPageCount(51, 50)).toBe(2)
    expect(MEMORY_PAGE_SIZE_KEY).toBe('gofer.memory.pagesize')
  })
})

describe('旧链接 /issues?tab=memories → /memory', () => {
  it('carries every parameter over and drops tab', () => {
    expect(
      legacyMemoryQuery({ tab: 'memories', memory_scope: 'repo', tracker: 'trk', memory: 'release-b' }),
    ).toEqual({ memory_scope: 'repo', tracker: 'trk', memory: 'release-b' })
  })

  it('is not a memory link for other tabs or a plain issues visit', () => {
    expect(legacyMemoryQuery({ tab: 'issues' })).toBeNull()
    expect(legacyMemoryQuery({})).toBeNull()
    expect(legacyMemoryQuery(undefined)).toBeNull()
  })
})

describe('接口结果归一与筛选', () => {
  const repoMemory = {
    id: 'm1',
    rev: 2,
    updated_at: '2026-10-20T10:00:00Z',
    body: { key: 'release-flow', content: '# 发版流程\n正文', kind: 'rule', summary: '发版流程' },
  }
  const scopedMemory = {
    scope: 'global' as const,
    key: 'handoff',
    content: 'deploy next',
    tags: ['prime'],
    updated_at: '2026-10-21T10:00:00Z',
    updated_by: 'alice',
  }

  it('turns both response shapes into rows with a body', () => {
    const rows = memoryRowsOf({ memories: [repoMemory, scopedMemory] })
    expect(rows.map((r) => r.data.key)).toEqual(['release-flow', 'handoff'])
    expect(rows[1].data).toEqual({
      key: 'handoff',
      content: 'deploy next',
      tags: ['prime'],
      updated_at: '2026-10-21T10:00:00Z',
      by: 'alice',
      kind: undefined,
      summary: undefined,
    })
  })

  it('drops deleted memories and survives a missing list', () => {
    expect(memoryRowsOf({ memories: [{ ...repoMemory, deleted: true }] })).toEqual([])
    expect(memoryRowsOf({})).toEqual([])
  })

  it('keeps the server doctor map (repository scope only)', () => {
    expect(memoryDoctorOf({ doctor: { m1: [{ slug: 'note-stale' }] } })).toEqual({ m1: [{ slug: 'note-stale' }] })
    expect(memoryDoctorOf({})).toEqual({})
  })

  it('keys a row by body.key, then by id', () => {
    const [row] = memoryRowsOf({ memories: [repoMemory] })
    expect(memoryRowKey(row)).toBe('release-flow')
    const [scoped] = memoryRowsOf({ memories: [scopedMemory] })
    expect(memoryRowKey(scoped)).toBe('handoff')
    expect(memoryRowKey({ id: 'only-id', data: {} })).toBe('only-id')
  })

  it('filters by keyword, kind and the flagged-only switch together', () => {
    const rows = memoryRowsOf({ memories: [repoMemory, scopedMemory] })
    expect(filterMemories(rows, 'deploy', [], false, {})).toHaveLength(1)
    expect(filterMemories(rows, '', ['rule'], false, {}).map((r) => r.data.key)).toEqual(['release-flow', 'handoff'])
    expect(filterMemories(rows, '', [], true, {})).toEqual([])
    // doctor 标记按行的 key 查
    expect(filterMemories(rows, '', [], true, { handoff: [{ slug: 'note-stale' }] }).map((r) => r.data.key)).toEqual(['handoff'])
  })
})

describe('「全部项目」范围：多路结果合并、去重与来源', () => {
  const older = { scope: 'global' as const, key: 'deploy-flow', content: '全局的部署流程', tags: [], updated_at: '2026-10-01T00:00:00Z', updated_by: 'alice' }
  const newer = { scope: 'project' as const, scope_key: 'gofer', key: 'deploy-flow', content: '项目里的部署流程', tags: ['deploy'], updated_at: '2026-10-22T00:00:00Z', updated_by: 'bob' }
  const repoMemory = {
    id: 'release-flow',
    rev: 4,
    updated_at: '2026-10-20T00:00:00Z',
    body: { key: 'release-flow', content: '# 发版流程', kind: 'rule', summary: '发版流程' },
  }

  it('keeps same-key memories of different sources apart and labels each one', () => {
    const rows = mergeMemoryBatches([
      { source: { scope: 'global' }, result: { memories: [older] } },
      { source: { scope: 'project', project: 'gofer' }, result: { memories: [newer] } },
      { source: { scope: 'repo', trackerId: 'trk', label: 'gofer / gofer' }, result: { memories: [repoMemory] } },
    ])
    // 最近更新的排前面
    expect(rows.map((r) => memorySourceKey(r))).toEqual([
      'project:gofer:deploy-flow',
      'repo:trk:release-flow',
      'global:deploy-flow',
    ])
    expect(rows.map((r) => memorySourceLabel(r.source))).toEqual(['项目 · gofer', '仓库 · gofer / gofer', '全局'])
  })

  it('drops an exact duplicate (same source and key) and survives deleted rows', () => {
    const rows = mergeMemoryBatches([
      { source: { scope: 'global' }, result: { memories: [older, { ...older, deleted: true }, older] } },
    ])
    expect(rows).toHaveLength(1)
  })

  it('search and kind filters work on the merged set', () => {
    const rows = mergeMemoryBatches([
      { source: { scope: 'global' }, result: { memories: [older] } },
      { source: { scope: 'project', project: 'gofer' }, result: { memories: [newer] } },
      { source: { scope: 'repo', trackerId: 'trk', label: 'gofer / gofer' }, result: { memories: [repoMemory] } },
    ])
    // 关键字搜 key + 摘要 + 内容，且在全部范围上生效
    expect(filterMemories(rows, '项目里的部署', [], false, {}).map((r) => r.data.key)).toEqual(['deploy-flow'])
    expect(filterMemories(rows, 'release', [], false, {}).map((r) => r.data.key)).toEqual(['release-flow'])
    // 类型 chip
    expect(filterMemories(rows, '', ['rule'], false, {}).map((r) => r.data.key)).toEqual(['release-flow'])
  })

  it('keys the doctor map by source so same-key memories do not share findings', () => {
    const batches = [
      { source: { scope: 'repo' as const, trackerId: 'a', label: 'a' }, result: { doctor: { dup: [{ slug: 'note-stale' }] } } },
      { source: { scope: 'repo' as const, trackerId: 'b', label: 'b' }, result: {} },
    ]
    const doctor = mergeMemoryDoctor(batches)
    expect(doctor).toEqual({ 'repo:a:dup': [{ slug: 'note-stale' }] })
  })
})

describe('详情抽屉的编辑态', () => {
  it('takes the initial form state from the row', () => {
    const [row] = memoryRowsOf({ memories: [{ scope: 'project', scope_key: 'gofer', key: 'k', content: 'body', tags: ['a', 'b'], summary: 'sum', updated_at: '2026-10-20T00:00:00Z' }] })
    expect(memoryEditOf(row)).toEqual({ content: 'body', summary: 'sum', tags: ['a', 'b'] })
  })

  it('adds and drops tag chips without duplicates or blanks', () => {
    expect(addMemoryTag(['a'], ' b ')).toEqual(['a', 'b'])
    expect(addMemoryTag(['a'], 'a')).toEqual(['a'])
    expect(addMemoryTag([], '  ')).toEqual([])
    expect(removeMemoryTag(['a', 'b'], 'a')).toEqual(['b'])
  })

  it('builds the save body from content, summary and tags', () => {
    expect(memoryUpdateBody({ content: '# 标题\n正文', summary: '  摘要  ', tags: [' rule ', 'rule', ''] })).toEqual({
      content: '# 标题\n正文',
      summary: '摘要',
      tags: ['rule'],
    })
    const body = memoryUpdateBody({ content: 'x', summary: '', tags: [] })
    expect(Object.keys(body).sort()).toEqual(['content', 'summary', 'tags'])
  })
})

describe('文案随语言切', () => {
  it('renders the source labels in English after a locale switch', () => {
    setLocale('en-US')
    try {
      expect(memorySourceLabel({ scope: 'global' })).toBe('Global')
      expect(memorySourceLabel({ scope: 'project', project: 'gofer' })).toBe('Project · gofer')
      expect(memorySourceLabel({ scope: 'repo', trackerId: 'trk', label: 'gofer / gofer' })).toBe('Repository · gofer / gofer')
      expect(memorySourceLabel(undefined)).toBe('')
    } finally {
      setLocale('zh-CN')
    }
  })
})
