import { describe, expect, it } from 'vitest'

const source = Object.values(import.meta.glob('./Agents.vue', { eager: true, query: '?raw', import: 'default' }))[0] as string

describe('Agents mobile cards', () => {
  it('uses a compact three-row card and hides the desktop header', () => {
    expect(source).toContain("grid-template-areas:\n      'key detect'\n      'type type'\n      'health info'")
    expect(source).toContain('.thead {\n    display: none;')
  })
})
