import { describe, expect, it } from 'vitest'

const source = Object.values(import.meta.glob('./Runners.vue', { eager: true, query: '?raw', import: 'default' }))[0] as string

describe('Runners mobile topology', () => {
  it('collapses topology by default on phone widths', () => {
    expect(source).toContain("window.matchMedia('(max-width: 640px)')")
    expect(source).toContain('topologyOpen.value = false')
    expect(source).toContain('.topology-group details[open]')
  })
})
