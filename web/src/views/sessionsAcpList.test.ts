import { describe, expect, it } from 'vitest'

const source = Object.values(import.meta.glob('./Sessions.vue', { eager: true, query: '?raw', import: 'default' }))[0] as string

describe('ACP sessions list', () => {
  it('shows resident ACP jobs with status, turns, runner and last response preview', () => {
    expect(source).toContain("t('sessions.tab.acp')")
    expect(source).toContain('acpSessions')
    expect(source).toContain("t('sessions.card.lastReply')")
    expect(source).toContain("t('sessions.acp.openJob')")
  })
})
