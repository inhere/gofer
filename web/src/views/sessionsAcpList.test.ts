import { describe, expect, it } from 'vitest'

const source = Object.values(import.meta.glob('./Sessions.vue', { eager: true, query: '?raw', import: 'default' }))[0] as string
const acp = Object.values(import.meta.glob('../components/SessionsAcpTab.vue', { eager: true, query: '?raw', import: 'default' }))[0] as string

describe('ACP sessions list', () => {
  it('lists one card per ACP thread and opens it in the workbench', () => {
    expect(source).toContain("import SessionsAcpTab from '../components/SessionsAcpTab.vue'")
    expect(acp).toContain("source: 'acp'")
    expect(acp).toContain('acpSessions')
    expect(acp).toContain('/workbench?thread=')
    expect(acp).toContain('next_cursor')
    expect(acp).toContain('data-test="acp-cards"')
    expect(acp).toContain("t('sessions.card.lastReply')")
    expect(acp).toContain("t('sessions.acp.openJob')")
    expect(acp).toContain("t('sessions.acp.openSession')")
  })
})
