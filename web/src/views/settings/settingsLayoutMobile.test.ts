import { describe, expect, it } from 'vitest'

const source = Object.values(import.meta.glob('./SettingsLayout.vue', { eager: true, query: '?raw', import: 'default' }))[0] as string

describe('Settings mobile navigation', () => {
  it('scrolls the active tab into view and signals horizontal overflow', () => {
    expect(source).toContain('scrollIntoView')
    expect(source).toContain('mask-image: linear-gradient')
    expect(source).toContain('overflow-x: auto')
  })
})
