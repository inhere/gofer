import { describe, expect, it } from 'vitest'

const source = Object.values(
  import.meta.glob('./Dashboard.vue', { eager: true, query: '?raw', import: 'default' }),
)[0] as string

describe('Dashboard mobile layout', () => {
  it('keeps summary cards in a two-column grid at phone width', () => {
    expect(source).toContain('@media (max-width: 640px)')
    expect(source).toContain('grid-template-columns: repeat(2, minmax(0, 1fr));')
    expect(source).toContain('.span2')
  })
})
