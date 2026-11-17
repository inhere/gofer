import { describe, expect, it } from 'vitest'
import { planDriverBadge } from '../utils/planDriver'

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

  // 推进引擎（本版只有「只记录」）：启用的 plan 在行上带一个等级徽标，没有 driver 字段就不渲染。
  it('shows a level badge only on plans whose driver is on', () => {
    expect(planDriverBadge('record')).toBe('只记录')
    // 未启用时服务端不发 driver 字段（omitempty），徽标文案为空、模板里的 v-if 也就不渲染它。
    expect(planDriverBadge(undefined)).toBe('')
    expect(planDriverBadge('')).toBe('')
    // 新版服务端的未知等级原样显示，不显示空名字（与详情页页签同一个兜底）。
    expect(planDriverBadge('some_future_level')).toBe('some_future_level')
    expect(row).toContain('v-if="plan.driver"')
    expect(row).toContain('data-test="plan-driver-badge"')
    expect(row).toContain('planDriverBadge(plan.driver)')
  })
})
