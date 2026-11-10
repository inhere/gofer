import { describe, expect, it } from 'vitest'
import { includeIDs, needsReload } from './deepLink'

describe('includeIDs', () => {
  it('路由目标排第一，其后是 wanted', () => {
    expect(includeIDs('t9', ['a', 'b'])).toEqual(['t9', 'a', 'b'])
  })
  it('路由参数已清空时仍保留 wanted 里打开的旧线程', () => {
    expect(includeIDs(undefined, ['old-1'])).toEqual(['old-1'])
    expect(includeIDs('', ['old-1'])).toEqual(['old-1'])
  })
  it('去空、去重', () => {
    expect(includeIDs('a', ['', 'a', 'b', 'b'])).toEqual(['a', 'b'])
  })
  it('截到 8 个且目标不被截掉', () => {
    const wanted = Array.from({ length: 12 }, (_, i) => `w${i}`)
    const out = includeIDs('target', wanted)
    expect(out).toHaveLength(8)
    expect(out[0]).toBe('target')
  })
  it('非字符串路由值被忽略', () => {
    expect(includeIDs(['x'], ['a'])).toEqual(['a'])
    expect(includeIDs(null, [])).toEqual([])
    expect(includeIDs(42, ['a'])).toEqual(['a'])
  })
})

describe('needsReload', () => {
  it('目标不在已加载集合里才重拉', () => {
    expect(needsReload('t1', new Set(['t2']))).toBe(true)
    expect(needsReload('t1', new Set(['t1']))).toBe(false)
  })
  it('空值与非字符串不重拉', () => {
    expect(needsReload('', new Set())).toBe(false)
    expect(needsReload(undefined, new Set())).toBe(false)
    expect(needsReload(['t1'], new Set())).toBe(false)
  })
})
