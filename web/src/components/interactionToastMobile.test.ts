import { describe, expect, it } from 'vitest'

const source = Object.values(import.meta.glob('./InteractionToast.vue', { eager: true, query: '?raw', import: 'default' }))[0] as string

describe('interaction toast mobile placement', () => {
  it('moves the toast below the mobile top bar and keeps it dismissible', () => {
    expect(source).toContain('@media (max-width: 640px)')
    expect(source).toMatch(/@media \(max-width: 640px\) \{[\s\S]*\.toast[\s\S]*top:/)
    expect(source).toMatch(/@media \(max-width: 640px\) \{[\s\S]*\.toast[\s\S]*bottom:\s*auto/)
    expect(source).toContain('aria-label="关闭提示"')
  })
})
