import { describe, expect, it } from 'vitest'

const source = Object.values(
  import.meta.glob('./Dashboard.vue', { eager: true, query: '?raw', import: 'default' }),
)[0] as string
const system = Object.values(
  import.meta.glob('../components/DashboardSystem.vue', { eager: true, query: '?raw', import: 'default' }),
)[0] as string

describe('Dashboard mobile layout', () => {
  it('collapses the statistics wall to one column at phone width', () => {
    expect(source).toContain('@media (max-width: 640px)')
    expect(source).toContain('@media (max-width: 520px)')
    // 主卡单列、小指标块两列（design: 380px 宽单列、无横向滚动）
    expect(source).toMatch(/\.grid4 \{\s*grid-template-columns: minmax\(0, 1fr\);/)
    expect(source).toMatch(/\.tiles\.grid4 \{\s*grid-template-columns: repeat\(2, minmax\(0, 1fr\)\);/)
    expect(source).toContain('min-width: 0')
  })

  it('keeps the system cards in a two-column grid at phone width', () => {
    expect(system).toContain('@media (max-width: 640px)')
    expect(system).toContain('grid-template-columns: repeat(2, minmax(0, 1fr));')
    expect(system).toContain('.span2')
  })
})
