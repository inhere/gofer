// 常驻 agent 区的接线（设计 2026-10-22 mail-agent-address「D6」）：组件本身是独立的，Mail.vue 只
// 挂载它——这两点用源码断言钉住，避免以后又把逻辑塞回 Mail.vue 或漏掉挂载。
import { describe, expect, it } from 'vitest'

const raw = import.meta.glob('../components/mail/ResidentAgents.vue', { eager: true, query: '?raw', import: 'default' }) as Record<string, string>
const mailRaw = import.meta.glob('./Mail.vue', { eager: true, query: '?raw', import: 'default' }) as Record<string, string>
const clientRaw = import.meta.glob('../api/client.ts', { eager: true, query: '?raw', import: 'default' }) as Record<string, string>

const component = Object.values(raw)[0] ?? ''
const page = Object.values(mailRaw)[0] ?? ''
const client = Object.values(clientRaw)[0] ?? ''

describe('the resident-agent section of the mailbox', () => {
  it('is its own component, mounted by Mail.vue', () => {
    expect(page).toContain("import MailResidentAgents from '../components/mail/ResidentAgents.vue'")
    expect(page).toContain('<MailResidentAgents />')
  })

  it('loads and ends sessions through the API client', () => {
    expect(client).toContain('export function listMailAgents(')
    expect(client).toContain("'/v1/mail/agents'")
    expect(client).toContain('export function endMailAgent(')
    expect(component).toContain('listMailAgents()')
    expect(component).toContain('endMailAgent(r.name')
  })

  it('shows name, project, job link, letters, idle time and an end button', () => {
    expect(component).toContain('data-test="mail-resident-table"')
    expect(component).toContain('data-test="mail-resident-job"')
    expect(component).toContain('data-test="mail-resident-end"')
    expect(component).toContain('data-test="mail-resident-cap"')
    expect(component).toContain('r.letters')
    expect(component).toContain('idleOf(r)')
    expect(component).toContain('/jobs/')
  })
})
