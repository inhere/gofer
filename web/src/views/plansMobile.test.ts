import { describe, expect, it } from 'vitest'

const source = Object.values(import.meta.glob('./Plans.vue', { eager: true, query: '?raw', import: 'default' }))[0] as string

describe('Plans mobile filters', () => {
  it('provides a horizontal status row and collapsible filter drawer', () => {
    expect(source).toContain('class="filter-drawer"')
    expect(source).toContain('filter-drawer-toggle')
    expect(source).toContain('overflow-x: auto')
  })
})
