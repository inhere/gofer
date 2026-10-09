import { describe, expect, it } from 'vitest'
import { PLAN_CONTROL_HELP, planChainState, planUsageRows } from './planControls'

const planDetail = Object.values(import.meta.glob('../views/PlanDetail.vue', { eager: true, query: '?raw', import: 'default' }))[0] as string

describe('plan chain controls', () => {
  it('every control has a visible label and an explanation', () => {
    expect(PLAN_CONTROL_HELP.leader.label).toContain('管家主导轮次')
    expect(PLAN_CONTROL_HELP.run.help).toContain('依赖已满足、已指派')
    expect(PLAN_CONTROL_HELP.pause.help).toContain('不会被取消')
    expect(PLAN_CONTROL_HELP.resume.label).toBe('继续')
    for (const c of Object.values(PLAN_CONTROL_HELP)) {
      expect(c.label.length).toBeGreaterThan(0)
      expect(c.help.length).toBeGreaterThan(5)
    }
  })

  it('describes the chain state in words', () => {
    expect(planChainState({ status: 'open' }).text).toContain('自动推进中')
    expect(planChainState({ status: 'open', paused: true })).toMatchObject({ tone: 'paused' })
    expect(planChainState({ status: 'open', paused: true }).text).toContain('已挂起')
    expect(planChainState({ status: 'blocked', blocked_todo: 't1' }, '写测试').text).toContain('阻塞于「写测试」')
    expect(planChainState({ status: 'blocked', blocked_todo: 't1' }).text).toContain('阻塞于「t1」')
    expect(planChainState({ status: 'done' }).tone).toBe('idle')
    expect(planChainState({ status: 'archived' }).text).toContain('已归档')
    expect(planChainState(null).text).toBe('')
  })

  it('PlanDetail renders the labels, help lines and chain state', () => {
    expect(planDetail).toContain('PLAN_CONTROL_HELP.leader.label')
    expect(planDetail).toContain('PLAN_CONTROL_HELP.run.help')
    expect(planDetail).toContain('PLAN_CONTROL_HELP.pause.help')
    expect(planDetail).toContain('class="ctl-help"')
    expect(planDetail).toContain('chainState.text')
    expect(planDetail).toContain('<dt>主 Agent 会话</dt>')
    expect(planDetail).toContain("path: '/sessions', query: { sid: plan.supervisor_session_id }")
    expect(planDetail).toContain('v-for="(r, i) in usageRows"')
  })
})

describe('plan usage block', () => {
  const base = { jobs: 2, total_tokens: 1000, cost_usd: 0.5, by_agent: {} }

  it('is empty when nothing ran and no session usage was attributed', () => {
    expect(planUsageRows(undefined)).toEqual([])
    expect(planUsageRows({ ...base, jobs: 0, total_tokens: 0, cost_usd: 0 })).toEqual([])
  })

  it('jobs only: total + jobs row', () => {
    const rows = planUsageRows({ ...base, overall: { total_tokens: 1000, cost_usd: 0.5 } })
    expect(rows.map((r) => r.label)).toEqual(['合计', 'Jobs（2 个）'])
    expect(rows[0].text).toBe('1k tokens / $0.5000')
  })

  it('splits the supervising session into main / sub agent and by model', () => {
    const rows = planUsageRows({
      ...base,
      session: {
        sessions: 1,
        main: { total_tokens: 3000 },
        sub: { total_tokens: 1000 },
        total: { total_tokens: 4000 },
        by_model: { haiku: { total_tokens: 1000 }, opus: { total_tokens: 3000 } },
      },
      overall: { total_tokens: 5000, cost_usd: 0.5 },
    })
    expect(rows.map((r) => r.label)).toEqual([
      '合计', 'Jobs（2 个）', '主 Agent 会话', '　· 主 agent', '　· 子 agent', '　· opus', '　· haiku',
    ])
    expect(rows[0].text).toBe('5k tokens / $0.5000')
    expect(rows[2].text).toBe('total 4k')
  })

  it('session usage alone (no jobs yet) still renders, and counts several sessions', () => {
    const rows = planUsageRows({
      jobs: 0, total_tokens: 0, cost_usd: 0, by_agent: {},
      session: { sessions: 2, main: { total_tokens: 10 }, sub: {}, total: { total_tokens: 10 } },
    })
    expect(rows.map((r) => r.label)).toEqual(['合计', '主 Agent 会话（2 个会话）', '　· 主 agent'])
    expect(rows[0].text).toBe('10 tokens')
  })
})
