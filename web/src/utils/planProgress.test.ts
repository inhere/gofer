// 计划进度的唯一口径（gofer-j4j1）：完成 / 总数，跳过算完成，受阻 / 失败不算；
// 计划详情顶部、清单进度、计划总览、看板都从这里取数。
import { describe, expect, it } from 'vitest'
import { isTodoComplete, percentOf, planProgress } from './planProgress'

describe('percentOf', () => {
  it('rounds and clamps to 0–100, and has no answer without a total', () => {
    expect(percentOf(1, 3)).toBe(33)
    expect(percentOf(3, 3)).toBe(100)
    expect(percentOf(0, 5)).toBe(0)
    expect(percentOf(5, 0)).toBeNull()
    expect(percentOf(9, 4)).toBe(100)
    expect(percentOf(-1, 4)).toBe(0)
  })
})

describe('planProgress', () => {
  it('counts todo done + skipped as complete and leaves blocked / doing unfinished', () => {
    const progress = planProgress({
      todo_counts: { total: 10, pending: 0, doing: 1, done: 8, skipped: 1 },
      counts: { total: 4, queued: 0, running: 1, done: 2, failed: 1 },
    })
    // 9/10 90%：受阻的那 1 项不进分子，也不会把分母撑大（曾经显示成 9/10 100%）。
    expect(progress).toEqual({ done: 9, total: 10, percent: 90 })
  })

  it('reads the todo list itself when the detail page has it loaded', () => {
    const progress = planProgress({
      todos: [
        { status: 'done' },
        { status: 'skipped' },
        { status: 'doing' },
        { status: 'pending' },
      ],
      // 明细与汇总不一致时以明细为准（看板与清单看的就是这些行）。
      todo_counts: { total: 9, pending: 0, doing: 0, done: 9, skipped: 0 },
    })
    expect(progress).toEqual({ done: 2, total: 4, percent: 50 })
  })

  it('treats done=true on a todo as complete even before the status is done', () => {
    expect(isTodoComplete({ done: true })).toBe(true)
    expect(isTodoComplete({ status: 'skipped' })).toBe(true)
    expect(isTodoComplete({ status: 'doing' })).toBe(false)
    expect(planProgress({ todos: [{ done: true }, { status: 'doing' }] }).done).toBe(1)
  })

  it('falls back to job counts for a plan without todos', () => {
    const progress = planProgress({
      todo_counts: { total: 0, pending: 0, doing: 0, done: 0, skipped: 0 },
      counts: { total: 5, queued: 2, running: 1, done: 1, failed: 1 },
    })
    expect(progress).toEqual({ done: 1, total: 5, percent: 20 })
  })

  it('has nothing to measure for an empty plan', () => {
    expect(planProgress({})).toEqual({ done: 0, total: 0, percent: null })
    expect(planProgress({ todos: [] })).toEqual({ done: 0, total: 0, percent: null })
  })
})
