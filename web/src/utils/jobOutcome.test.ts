import { describe, expect, it } from 'vitest'
import { agentBudgetExhausted, agentBudgetLine, budgetLine, usageLine } from './jobOutcome'
import { eventDetailText } from './eventMeta'
import { wrapUpText } from './wrapUpDisplay'

describe('budgetLine (N2 §B)', () => {
  it('is empty without a budget', () => {
    expect(budgetLine(undefined, { total_tokens: 5 })).toBe('')
    expect(budgetLine({}, undefined)).toBe('')
  })

  it('lists the limits and, for limited dimensions only, what was used', () => {
    const line = budgetLine(
      { max_tokens: 50000, max_cost_usd: 2, max_turns: 20 },
      { total_tokens: 12300, cost_usd: 0.12, turns: 5, input_tokens: 9 },
    )
    expect(line).toBe('上限 50k tokens / $2 / 20 turns（已用 12.3k tokens / $0.1200 / 5 turns）')
    expect(budgetLine({ max_turns: 3 }, { total_tokens: 99999 })).toBe('上限 3 turns')
  })
})

describe('job.budget_exceeded timeline text', () => {
  it('names the limit, the ceiling and the value that crossed it', () => {
    const text = eventDetailText({
      type: 'job.budget_exceeded',
      detail: JSON.stringify({ limit: 'max_tokens', max: 1000, used: 1100 }),
    } as never)
    expect(text).toBe('max_tokens 上限 1000（已用 1100）')
  })
})

describe('usageLine', () => {
  it('shows tool calls when the agent reports them', () => {
    expect(usageLine({ input_tokens: 3071, output_tokens: 2, total_tokens: 3073, tool_calls: 49, source: 'ndjson:suag' })).toBe(
      'in 3.07k / out 2 / total 3.07k / tools 49 (ndjson:suag)',
    )
    expect(usageLine({ input_tokens: 10 })).not.toContain('tools')
  })
})

describe('agent soft budget (gofer-hnqf)', () => {
  it('shows used / limit for the dimensions the agent was given', () => {
    expect(
      agentBudgetLine({
        agent_budget: { scope: 'turn', limit_tokens: 50000, used_tokens: 51200, limit_cost_usd: 2, used_cost_usd: 0.5, exceeded: true },
      }),
    ).toBe('51.2k / 50k tokens / $0.5000 / $2（turn）')
    expect(agentBudgetLine({ agent_budget: { limit_cost_usd: 1, used_cost_usd: 0.25, estimated: true } })).toBe('$0.2500 / $1（估算）')
    expect(agentBudgetLine({ agent_budget: { limit_tokens: 100 } })).toBe('0 / 100 tokens')
  })

  it('renders nothing without a snapshot or a limit', () => {
    expect(agentBudgetLine(null)).toBe('')
    expect(agentBudgetLine({ total_tokens: 5 })).toBe('')
    expect(agentBudgetLine({ agent_budget: { scope: 'turn', exceeded: true } })).toBe('')
  })

  it('flags the exhausted wrap-up from the snapshot or the wrap_up reason', () => {
    expect(agentBudgetExhausted({ agent_budget: { exceeded: true } })).toBe(true)
    expect(agentBudgetExhausted({ agent_budget: { exceeded: false } }, 'max_steps')).toBe(false)
    expect(agentBudgetExhausted(null, 'budget')).toBe(true)
    expect(agentBudgetExhausted(null, undefined)).toBe(false)
  })

  it('lists the agent limits next to the hard ones in the budget line', () => {
    expect(budgetLine({ max_tokens: 90000, agent_tokens: 50000, agent_cost_usd: 2 }, null)).toBe(
      '上限 90k tokens / agent 50k tokens / agent $2',
    )
  })

  it('names the wrap-up reason and the timeline event', () => {
    expect(wrapUpText('budget')).toContain('预算耗尽收尾')
    expect(
      eventDetailText({
        type: 'job.agent_budget_exhausted',
        detail: JSON.stringify({ limit_tokens: 1000, used_tokens: 1100, scope: 'turn', estimated: true }),
      } as never),
    ).toBe('1100 / 1000 tokens · turn · 估算')
  })
})
