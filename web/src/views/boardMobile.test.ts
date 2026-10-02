import { describe, expect, it } from 'vitest'

const source = Object.values(import.meta.glob('./Board.vue', { eager: true, query: '?raw', import: 'default' }))[0] as string

describe('Board mobile layout', () => {
  it('keeps job labels inline with the title metadata', () => {
    expect(source).toContain('@media (max-width: 640px)')
    expect(source).toContain('.col-job .job-tags')
    expect(source).toContain("content: ' · '")
  })

  it('keeps the title row on one line so the id never runs under the status', () => {
    // 监督者验收（M 批）：内联排布溢出到状态列，id 与状态重叠。
    expect(source).toMatch(/\.col-job \.job-title \{[^}]*text-overflow: ellipsis/)
    expect(source).toMatch(/\.col-job \{[^}]*overflow: hidden/)
  })
})
