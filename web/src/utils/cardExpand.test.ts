import { describe, expect, it } from 'vitest'
import { pruneExpanded, toggleExpanded } from './cardExpand'

describe('card expand state', () => {
  it('toggles an id open and closed without mutating the previous set', () => {
    const a = new Set<string>()
    const b = toggleExpanded(a, 'x')
    expect([...b]).toEqual(['x'])
    expect(a.size).toBe(0)
    const c = toggleExpanded(b, 'x')
    expect(c.has('x')).toBe(false)
    expect(b.has('x')).toBe(true)
  })

  it('keeps other cards expanded when one toggles', () => {
    let s = toggleExpanded(new Set(), 'a')
    s = toggleExpanded(s, 'b')
    s = toggleExpanded(s, 'a')
    expect([...s]).toEqual(['b'])
  })

  it('prunes ids that vanished from the list', () => {
    const s = pruneExpanded(new Set(['a', 'b', 'c']), ['b', 'c', 'd'])
    expect([...s].sort()).toEqual(['b', 'c'])
  })
})
