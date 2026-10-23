// 记忆页的纯逻辑（utils/memories.ts，issue gofer-hez1）：URL 契约、旧链接重定向、响应归一与筛选。
import { describe, expect, it } from 'vitest'
import {
  filterMemories,
  legacyMemoryQuery,
  memoryDoctorOf,
  memoryQueryUpdate,
  memoryRowKey,
  memoryRowsOf,
  readMemoryKey,
  readMemoryProject,
  readMemoryScope,
  readMemorySearch,
  searchQueryUpdate,
} from './memories'

describe('作用域 / 项目 / 关键字的 URL 契约', () => {
  it('reads the scope from ?memory_scope= and falls back to the repository', () => {
    expect(readMemoryScope({ memory_scope: 'global' })).toBe('global')
    expect(readMemoryScope({ memory_scope: 'project' })).toBe('project')
    expect(readMemoryScope({ memory_scope: 'repo' })).toBe('repo')
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
