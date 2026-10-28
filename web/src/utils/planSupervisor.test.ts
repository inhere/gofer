import { describe, expect, it } from 'vitest'
import { HANDOFF_QUICK_PHRASE, canMessageSession, messageOutcomeText, sessionLabel } from './planSupervisor'

const planDetail = Object.values(import.meta.glob('../views/PlanDetail.vue', { eager: true, query: '?raw', import: 'default' }))[0] as string
const comp = Object.values(import.meta.glob('../components/PlanSupervisor.vue', { eager: true, query: '?raw', import: 'default' }))[0] as string
const client = Object.values(import.meta.glob('../api/client.ts', { eager: true, query: '?raw', import: 'default' }))[0] as string

describe('planSupervisor helpers', () => {
  it('uses the handoff quick phrase', () => {
    expect(HANDOFF_QUICK_PHRASE.value).toBe('请写交接说明并更新 plan handoff')
  })
  it('labels sessions by title or agent + short id', () => {
    expect(sessionLabel({ session_id: 'abcdef123456', title: ' 主会话 ', agent: 'claude', state: 'idle' })).toBe('主会话 · idle')
    expect(sessionLabel({ session_id: 'abcdef123456', agent: 'claude', state: 'running' })).toBe('claude abcdef12 · running')
  })
  it('describes send outcomes', () => {
    expect(messageOutcomeText({ status: 'delivered', channel: 'relay' })).toBe('已送达（relay）')
    expect(messageOutcomeText({ status: 'queued' })).toBe('已排队，等待送达')
    expect(messageOutcomeText({ status: 'failed', error: 'boom' })).toBe('发送失败：boom')
    expect(messageOutcomeText({ status: 'delivering', channel: 'command' })).toBe('送达中（command）')
    expect(messageOutcomeText({ status: 'undelivered', error: '未送达' })).toBe('未送达：未送达')
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
  it('keeps the supervisor session, message input and actions inside one card', () => {
    expect(comp).toContain('data-test="supervisor-card"')
    expect(comp).toContain('data-test="supervisor-session"')
    expect(comp).toContain('data-test="supervisor-actions"')
    expect(comp).toContain('sendSessionMessage(props.sid')
    expect(comp).toContain('<SessionDrawer')
    expect(comp).toContain('setPlanSupervisorSession')
    expect(comp).toContain("bind('')")
    expect(client).toMatch(/supervisor_session_id: sid/)
    const body = comp.slice(comp.indexOf('class="ps-body"'), comp.indexOf('<SessionDrawer'))
    expect(body.indexOf('data-test="supervisor-session"')).toBeGreaterThanOrEqual(0)
    expect(body.indexOf('class="op-input ps-input"')).toBeGreaterThan(body.indexOf('data-test="supervisor-session"'))
    expect(body.indexOf('data-test="supervisor-actions"')).toBeGreaterThan(body.indexOf('class="op-input ps-input"'))
  })

  it('keeps the unbound empty state and bind entry in the same card', () => {
    expect(comp).toContain('v-if="!sid" class="empty mono"')
    expect(comp).toContain("sid ? t('plans.supervisor.change') : t('plans.supervisor.bind')")
    expect(comp).toContain('class="section plan-supervisor" data-test="supervisor-card"')
  })

  it('combines checklist progress and chain controls into one card', () => {
    const card = planDetail.slice(planDetail.indexOf('data-test="plan-controls-card"'), planDetail.indexOf('<!-- 用量：'))
    expect(card).toContain('class="ops-summary"')
    expect(card).toContain('class="chain-ctl mono"')
    expect(card.indexOf('class="ops-summary"')).toBeLessThan(card.indexOf('class="chain-ctl mono"'))
    expect(planDetail).toContain('.chain-ctl {\n  padding: 12px 14px 14px;\n  border-top: 1px solid var(--line);')
  })
})
