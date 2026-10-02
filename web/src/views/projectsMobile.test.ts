import { describe, expect, it } from 'vitest'

const source = Object.values(import.meta.glob('./Projects.vue', { eager: true, query: '?raw', import: 'default' }))[0] as string

describe('Projects mobile layout', () => {
  it('offers a top project picker and stacked key-value details', () => {
    expect(source).toContain('mobile-project-picker')
    expect(source).toContain('.kv {\n    display: block;')
    expect(source).toContain('layout > .list')
  })
})
