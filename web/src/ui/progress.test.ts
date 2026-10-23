// 分段进度（设计稿 §8 ProgressBar）：计划总览的进度口径 + 组件渲染。
import { describe, expect, it } from 'vitest'
import { createSSRApp, h } from 'vue'
import { renderToString } from 'vue/server-renderer'
import ProgressBar from './ProgressBar.vue'
import { percentOf, planProgressModel } from './progress'
import { locale } from '../store/locale'
import type { Plan } from '../api/types'

function plan(patch: Partial<Plan>): Plan {
  return {
    plan_id: 'gofer-hez1',
    status: 'open',
    created_at: 1_700_000_000,
    updated_at: 1_700_000_000,
    ...patch,
  }
}

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

describe('planProgressModel', () => {
  it('counts todos as done (incl. skipped) / doing / rest and keeps the fixed family order', () => {
    const model = planProgressModel(
      plan({
        todo_counts: { total: 8, pending: 3, doing: 2, done: 2, skipped: 1 },
        counts: { total: 4, queued: 1, running: 1, done: 1, failed: 1 },
      }),
    )
    expect(model.parts.map((p) => p.family)).toEqual(['done', 'doing', 'review', 'blocked', 'todo'])
    expect(model.parts.map((p) => p.count)).toEqual([3, 2, 0, 1, 3])
    expect(model.total).toBe(9)
    expect(model.done).toBe(3)
    expect(model.percent).toBe(33)
  })

  it('falls back to job counts when the plan has no todos', () => {
    const model = planProgressModel(
      plan({ counts: { total: 5, queued: 2, running: 1, done: 1, failed: 1 } }),
    )
    expect(model.parts).toEqual([
      { family: 'done', count: 1 },
      { family: 'doing', count: 1 },
      { family: 'review', count: 0 },
      { family: 'blocked', count: 1 },
      { family: 'todo', count: 2 },
    ])
    expect(model.total).toBe(5)
    expect(model.percent).toBe(20)
  })

  it('fills the needs-review and blocked segments from the server job figures (gofer-rj5e)', () => {
    const model = planProgressModel(
      plan({
        todo_counts: { total: 6, pending: 1, doing: 2, done: 3, skipped: 0 },
        counts: { total: 5, queued: 1, running: 1, done: 1, failed: 2 },
        needs_review: 2,
        failed_jobs: 2,
      }),
    )
    // 待验收 / 受阻是 job 口径：不再恒为 0，也不再只靠 counts.failed 揣摩。
    expect(model.parts.find((p) => p.family === 'review')?.count).toBe(2)
    expect(model.parts.find((p) => p.family === 'blocked')?.count).toBe(2)
  })

  it('treats a missing needs_review / failed_jobs as zero instead of guessing', () => {
    const model = planProgressModel(
      plan({ todo_counts: { total: 2, pending: 2, doing: 0, done: 0, skipped: 0 } }),
    )
    expect(model.parts.find((p) => p.family === 'review')?.count).toBe(0)
    expect(model.parts.find((p) => p.family === 'blocked')?.count).toBe(0)
  })

  it('prefers the server completion figure (single progress authority)', () => {
    const model = planProgressModel(
      plan({
        todo_counts: { total: 4, pending: 1, doing: 1, done: 2, skipped: 0 },
        completion: { basis: 'todos', done: 2, total: 4, percent: 50 },
      }),
    )
    expect(model.percent).toBe(50)
  })

  it('has nothing to measure for an empty plan', () => {
    const model = planProgressModel(plan({}))
    expect(model.total).toBe(0)
    expect(model.percent).toBeNull()
    expect(model.parts.every((p) => p.count === 0)).toBe(true)
  })
})

async function render(comp: unknown, props: Record<string, unknown>): Promise<string> {
  return renderToString(createSSRApp({ render: () => h(comp as never, props) }))
}

describe('ProgressBar', () => {
  const parts = [
    { family: 'done' as const, count: 3 },
    { family: 'doing' as const, count: 1 },
    { family: 'todo' as const, count: 4 },
  ]

  it('renders one segment per non-empty family with proportion widths', async () => {
    const html = await render(ProgressBar, { parts })
    expect(html).toContain('role="img"')
    expect(html.match(/data-count=/g)?.length).toBe(3)
    expect(html.match(/style="width:/g)?.length).toBe(3)
    expect(html).toContain('data-total="8"')
    expect(html).toContain('data-done="3"')
    expect(html).toContain('3/8')
    expect(html).toContain('38%')
  })

  it('shows the legend with words and counts (state is not colour-only)', async () => {
    const before = locale.value
    locale.value = 'zh-CN'
    try {
      const html = await render(ProgressBar, { parts })
      expect(html).toContain('data-test="progress-legend"')
      expect(html).toContain('完成')
      expect(html).toContain('未开始')
      expect(html).toContain('进行中')
    } finally {
      locale.value = before
    }
  })

  it('renders an empty bar for a plan with nothing to measure', async () => {
    const html = await render(ProgressBar, { parts: [] })
    expect(html).toContain('seg--empty')
    expect(html).toContain('0/0')
  })
})
