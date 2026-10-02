import { describe, expect, it } from 'vitest'

const source = Object.values(import.meta.glob('./PlanDetail.vue', { eager: true, query: '?raw', import: 'default' }))[0] as string

describe('Plan detail mobile toolbar', () => {
  it('keeps the plan controls in one horizontally usable toolbar', () => {
    expect(source).toContain('@media (max-width: 640px)')
    expect(source).toContain('.detail-head')
    expect(source).toContain('overflow-x: auto')
    expect(source).toContain('class="more-actions"')
  })

  it('keeps the detail toolbar on one left-aligned row on a phone', () => {
    // 监督者验收（M 批）：≤780px 的纵向规则未被覆盖，手机上变成居中堆叠的 3 行。
    expect(source).toMatch(/@media \(max-width: 640px\) \{[\s\S]*?\.detail-head \{[^}]*flex-direction: row/)
  })
})
