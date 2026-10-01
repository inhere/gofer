import { describe, expect, it } from 'vitest'

const source = Object.values(import.meta.glob('./Sessions.vue', { eager: true, query: '?raw', import: 'default' }))[0] as string

describe('ACP sessions list', () => {
  it('shows resident ACP jobs with status, turns, runner and last response preview', () => {
    expect(source).toContain('ACP 持续会话')
    expect(source).toContain('acpSessions')
    expect(source).toContain('最后一条回复')
    expect(source).toContain('查看过程')
  })
})
