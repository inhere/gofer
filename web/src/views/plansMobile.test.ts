import { describe, expect, it } from 'vitest'

const source = Object.values(import.meta.glob('./Plans.vue', { eager: true, query: '?raw', import: 'default' }))[0] as string
const row = Object.values(
  import.meta.glob('../ui/PlanOverviewRow.vue', { eager: true, query: '?raw', import: 'default' }),
)[0] as string

// 结构 / 行为断言（设计稿 §13：*Mobile 测试不再拿正则在 .vue 里匹配 CSS 细节）。
describe('Plans mobile filters', () => {
  it('filters with the shared chip + drawer primitives instead of its own details panel', () => {
    expect(source).toContain('<FilterChips')
    expect(source).toContain('<FilterDrawer')
    expect(source).toContain('<ProjectFilter')
    expect(source).not.toContain('<details class="filter-drawer"')
  })

  it('turns every plan row into a card on phones', () => {
    expect(row).toContain('@media (max-width: 760px)')
    expect(row).toContain('grid-template-columns: 1fr')
    expect(row).toContain('border-radius: var(--r-panel)')
  })
})
