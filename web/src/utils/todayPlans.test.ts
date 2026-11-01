import { describe, expect, it } from 'vitest'
import type { Plan } from '../api/types'
import { TODAY_PLANS_LIMIT, todayPlanRows } from './todayPlans'

function plan(id: string, over: Partial<Plan> = {}): Plan {
  return {
    plan_id: id,
    title: `计划 ${id}`,
    status: 'open',
    created_at: 100,
    updated_at: 100,
    todo_counts: { total: 4, pending: 1, doing: 1, done: 2, skipped: 0 },
    ...over,
  } as Plan
}

describe('todayPlanRows', () => {
  it('lists only open, unpaused plans, newest activity first, linked to the plan page', () => {
    const { rows, more } = todayPlanRows([
      plan('p-old', { updated_at: 10 }),
      plan('p-new', { updated_at: 90 }),
      plan('p-done', { status: 'done' }),
      plan('p-blocked', { status: 'blocked' }),
      plan('p-paused', { paused: true }),
      plan('p-archived', { status: 'archived' }),
    ])
    expect(rows.map((r) => r.id)).toEqual(['p-new', 'p-old'])
    expect(rows[0].to).toBe('/plans/p-new')
    expect(more).toBe(0)
  })

  it('takes done / total from the one progress rule and the title falls back to the id', () => {
    const { rows } = todayPlanRows([plan('p1', { title: '  ' })])
    expect(rows[0]).toMatchObject({ id: 'p1', title: 'p1', done: 2, total: 4, percent: 50 })
  })

  it('caps the list and says how many more there are', () => {
    const plans = Array.from({ length: TODAY_PLANS_LIMIT + 3 }, (_, i) => plan(`p${i}`, { updated_at: i }))
    const { rows, more } = todayPlanRows(plans)
    expect(rows).toHaveLength(TODAY_PLANS_LIMIT)
    expect(more).toBe(3)
  })

  it('has nothing to show without plans', () => {
    expect(todayPlanRows([])).toEqual({ rows: [], more: 0 })
    expect(todayPlanRows(null)).toEqual({ rows: [], more: 0 })
    expect(todayPlanRows(undefined)).toEqual({ rows: [], more: 0 })
  })
})
