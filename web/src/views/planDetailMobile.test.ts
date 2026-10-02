import { describe, expect, it } from 'vitest'

const source = Object.values(import.meta.glob('./PlanDetail.vue', { eager: true, query: '?raw', import: 'default' }))[0] as string

describe('Plan detail mobile toolbar', () => {
  it('keeps the plan controls in one horizontally usable toolbar', () => {
    expect(source).toContain('@media (max-width: 640px)')
    expect(source).toContain('.detail-head')
    expect(source).toContain('overflow-x: auto')
    expect(source).toContain('class="more-actions"')
  })
})
