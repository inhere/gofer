import { describe, expect, it } from 'vitest'

const source = Object.values(import.meta.glob('./NewJob.vue', { eager: true, query: '?raw', import: 'default' }))[0] as string

describe('New job mobile form', () => {
  it('folds advanced fields and keeps submit reachable', () => {
    expect(source).toContain('<details class="mobile-advanced"')
    expect(source).toContain('高级选项')
    expect(source).toContain('env(safe-area-inset-bottom)')
  })
})
