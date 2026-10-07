import { describe, expect, it } from 'vitest'
import {
  clampPage, descendantIds, normalizePageSize, pageCheckState, pageWindow, paginateEntries, summarizeBatch,
  toggleId, togglePage, toggleWithDescendants,
} from './issuePaging'

const e = (id: string, depth = 0) => ({ id, depth })
const depth = (x: { depth: number }) => x.depth
const ids = (p: Array<{ id: string }>) => p.map((x) => x.id)

describe('paginateEntries', () => {
  it('slices flat rows by count', () => {
    const rows = Array.from({ length: 5 }, (_, i) => e(`r${i}`))
    expect(paginateEntries(rows, 2, depth).map(ids)).toEqual([['r0', 'r1'], ['r2', 'r3'], ['r4']])
  })

  it('never splits a parent from its children', () => {
    // root a has 2 kids, then b, then c with 1 kid; size 4
    const rows = [e('a'), e('a1', 1), e('a2', 1), e('b'), e('c'), e('c1', 1)]
    const pages = paginateEntries(rows, 4, depth)
    expect(pages.map(ids)).toEqual([['a', 'a1', 'a2', 'b'], ['c', 'c1']])
    // a group never straddles pages
    for (const p of pages) if (p[0].depth !== 0) throw new Error('page starts mid-tree')
  })

  it('gives an oversized tree its own page', () => {
    const rows = [e('x'), e('big'), e('k1', 1), e('k2', 1), e('k3', 1), e('y')]
    expect(paginateEntries(rows, 2, depth).map(ids)).toEqual([['x'], ['big', 'k1', 'k2', 'k3'], ['y']])
  })

  it('handles empty input and clamps the page', () => {
    expect(paginateEntries([], 50, depth)).toEqual([])
    expect(clampPage(9, 3)).toBe(3)
    expect(clampPage(0, 3)).toBe(1)
    expect(clampPage(2, 0)).toBe(1)
  })

  it('stays fast and exact on 1000 rows', () => {
    const rows = Array.from({ length: 1000 }, (_, i) => e(`r${i}`, i % 5 === 0 ? 0 : 1))
    const pages = paginateEntries(rows, 50, depth)
    expect(pages.flat()).toHaveLength(1000)
    expect(pages.every((p) => p[0].depth === 0)).toBe(true)
  })
})

describe('page size and window', () => {
  it('only accepts the offered sizes', () => {
    expect(normalizePageSize('100')).toBe(100)
    expect(normalizePageSize(7)).toBe(50)
    expect(normalizePageSize(null)).toBe(50)
  })
  it('elides the middle of long page lists', () => {
    expect(pageWindow(1, 3)).toEqual([1, 2, 3])
    expect(pageWindow(10, 20)).toEqual([1, null, 8, 9, 10, 11, 12, null, 20])
  })
})

describe('selection', () => {
  it('toggles single ids without mutating the input', () => {
    const base = new Set(['a'])
    const next = toggleId(base, 'b')
    expect([...base]).toEqual(['a'])
    expect([...next].sort()).toEqual(['a', 'b'])
    expect([...toggleId(next, 'a')]).toEqual(['b'])
  })

  it('reports none / some / all for the page checkbox', () => {
    expect(pageCheckState(new Set(), ['a', 'b'])).toBe('none')
    expect(pageCheckState(new Set(['a']), ['a', 'b'])).toBe('some')
    expect(pageCheckState(new Set(['a', 'b', 'z']), ['a', 'b'])).toBe('all')
    expect(pageCheckState(new Set(['a']), [])).toBe('none')
  })

  it('select-page keeps other pages and toggles off only this page', () => {
    const other = new Set(['z'])
    const all = togglePage(other, ['a', 'b'])
    expect([...all].sort()).toEqual(['a', 'b', 'z'])
    expect([...togglePage(new Set(['z', 'a']), ['a', 'b'])].sort()).toEqual(['a', 'b', 'z']) // partial => fill
    expect([...togglePage(all, ['a', 'b'])]).toEqual(['z'])
  })

  it('parent selection does not imply children; the family toggle does', () => {
    const nodes = [{ id: 'p' }, { id: 'c1', parent: 'p' }, { id: 'c2', parent: 'p' }, { id: 'g', parent: 'c1' }, { id: 'o' }]
    expect([...toggleId(new Set(), 'p')]).toEqual(['p'])
    const fam = descendantIds('p', nodes)
    expect(fam.sort()).toEqual(['c1', 'c2', 'g'])
    expect([...toggleWithDescendants(new Set(), 'p', fam)].sort()).toEqual(['c1', 'c2', 'g', 'p'])
    expect([...toggleWithDescendants(new Set(['p', 'c1', 'c2', 'g', 'o']), 'p', fam)]).toEqual(['o'])
  })

  it('descendants honour the visible filter and survive cycles', () => {
    const nodes = [{ id: 'a', parent: 'b' }, { id: 'b', parent: 'a' }, { id: 'c', parent: 'a' }]
    expect(descendantIds('a', nodes).sort()).toEqual(['b', 'c'])
    expect(descendantIds('a', nodes, new Set(['c']))).toEqual(['c'])
  })
})

describe('summarizeBatch', () => {
  it('reports all-ok', () => {
    const s = summarizeBatch([{ id: 'a', ok: true }, { id: 'b', ok: true }])
    expect(s).toMatchObject({ total: 2, ok: 2, failed: 0, text: '已处理 2 项' })
  })
  it('lists each failure and keeps the successes', () => {
    const s = summarizeBatch([{ id: 'a', ok: true }, { id: 'b', ok: false, error: 'issue not found' }, { id: 'c', ok: false }])
    expect(s.okIds).toEqual(['a'])
    expect(s.failures).toEqual([{ id: 'b', error: 'issue not found' }, { id: 'c', error: '未知错误' }])
    expect(s.text).toBe('成功 1 项，失败 2 项')
  })
})
