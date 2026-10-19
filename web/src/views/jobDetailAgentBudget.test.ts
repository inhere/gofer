import { describe, expect, it } from 'vitest'
import jobDetail from './JobDetail.vue?raw'

// gofer-hnqf：job 详情的用量区显示 agent 软预算「已用 / 上限」，用尽收尾时给「预算耗尽收尾」徽标。
// 判定与文案在 utils/jobOutcome（纯函数，有单测），这里只钉接线。
describe('job detail agent budget wiring', () => {
  it('computes the line and the badge from the shared helpers', () => {
    expect(jobDetail).toMatch(/agentBudgetExhausted, agentBudgetLine/)
    expect(jobDetail).toMatch(/const agentBudgetText = computed<string>\(\(\) => agentBudgetLine\(usage\.value\)\)/)
    expect(jobDetail).toMatch(/agentBudgetExhausted\(usage\.value, job\.value\?\.wrap_up\)/)
  })

  it('shows the usage block for an agent budget alone and renders line + badge', () => {
    expect(jobDetail).toMatch(/v-if="usageText \|\| budgetText \|\| agentBudgetText"/)
    expect(jobDetail).toMatch(/v-if="agentBudgetDone" class="budget-done-badge mono"[^>]*>预算耗尽收尾</)
    expect(jobDetail).toMatch(/v-if="agentBudgetText"[^>]*>预算 \{\{ agentBudgetText \}\}</)
  })

  it('colours the badge through the theme token, not a literal', () => {
    const rule = jobDetail.match(/\.budget-done-badge \{[^}]*\}/)?.[0] ?? ''
    expect(rule).toContain('var(--run)')
    expect(rule).not.toMatch(/#[0-9a-fA-F]{3,6}/)
  })
})
