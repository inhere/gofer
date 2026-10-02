import { describe, expect, it } from 'vitest'

const source = Object.values(import.meta.glob('./Board.vue', { eager: true, query: '?raw', import: 'default' }))[0] as string

describe('Board mobile layout', () => {
  it('keeps job labels inline with the title metadata', () => {
    expect(source).toContain('@media (max-width: 640px)')
    expect(source).toContain('.col-job .job-tags')
    expect(source).toContain("content: ' · '")
  })
})
