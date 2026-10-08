import { describe, expect, it } from 'vitest'
import { budgetLine } from './jobOutcome'
import { eventDetailText } from './eventMeta'

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
