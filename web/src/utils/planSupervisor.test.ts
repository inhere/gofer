import { describe, expect, it } from 'vitest'
import { HANDOFF_QUICK_PHRASE, canMessageSession, messageOutcomeText, sessionLabel } from './planSupervisor'

const planDetail = Object.values(import.meta.glob('../views/PlanDetail.vue', { eager: true, query: '?raw', import: 'default' }))[0] as string
const comp = Object.values(import.meta.glob('../components/PlanSupervisor.vue', { eager: true, query: '?raw', import: 'default' }))[0] as string
const client = Object.values(import.meta.glob('../api/client.ts', { eager: true, query: '?raw', import: 'default' }))[0] as string

describe('planSupervisor helpers', () => {
  it('uses the handoff quick phrase', () => {
    expect(HANDOFF_QUICK_PHRASE).toBe('请写交接说明并更新 plan handoff')
  })
  it('labels sessions by title or agent + short id', () => {
    expect(sessionLabel({ session_id: 'abcdef123456', title: ' 主会话 ', agent: 'claude', state: 'idle' })).toBe('主会话 · idle')
    expect(sessionLabel({ session_id: 'abcdef123456', agent: 'claude', state: 'running' })).toBe('claude abcdef12 · running')
  })
  it('describes send outcomes', () => {
    expect(messageOutcomeText({ status: 'delivered', channel: 'relay' })).toBe('已送达（relay）')
    expect(messageOutcomeText({ status: 'queued' })).toBe('已排队，等待送达')
    expect(messageOutcomeText({ status: 'failed', error: 'boom' })).toBe('发送失败：boom')
  })
  it('does not message ended sessions', () => {
    expect(canMessageSession({ state: 'ended' })).toBe(false)
    expect(canMessageSession({ state: 'idle' })).toBe(true)
    expect(canMessageSession(null)).toBe(false)
  })
})

describe('PlanDetail supervisor session wiring', () => {
  it('renders the supervisor panel from plan.supervisor_session_id', () => {
    expect(planDetail).toContain('<PlanSupervisor')
    expect(planDetail).toContain('plan.supervisor_session_id')
  })
  it('reuses the session message entry, session drawer and plan PATCH', () => {
    expect(comp).toContain('sendSessionMessage(props.sid')
    expect(comp).toContain('<SessionDrawer')
    expect(comp).toContain('setPlanSupervisorSession')
    expect(comp).toContain("bind('')")
    expect(client).toMatch(/supervisor_session_id: sid/)
  })
})
