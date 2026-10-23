// 分段进度（设计稿 §8 的 ProgressBar）：把 todo / job 的计数折算成 5 段
// （完成 / 进行中 / 待验收 / 受阻 / 未开始）加 done/total 与百分比。
// 服务端的 completion 是唯一的进度权威（todo 优先、无 todo 回落 job）；没有它
// （老服务端）时按同一口径自己算，前端不引入第二套进度语义。
import type { Plan } from '../api/types'

export type ProgressFamily = 'done' | 'doing' | 'review' | 'blocked' | 'todo'

// 段的固定顺序：完成 → 进行中 → 待验收 → 受阻 → 未开始
export const PROGRESS_FAMILIES: ProgressFamily[] = ['done', 'doing', 'review', 'blocked', 'todo']

export interface ProgressPart {
  family: ProgressFamily
  count: number
}

export interface ProgressModel {
  parts: ProgressPart[]
  total: number
  done: number
  /** 0–100 的整数；无法衡量时为 null（例如没有任何 todo 与 job） */
  percent: number | null
}

export function percentOf(done: number, total: number): number | null {
  if (!Number.isFinite(total) || total <= 0) return null
  const pct = Math.round((done / total) * 100)
  return Math.min(100, Math.max(0, pct))
}

// 列表接口与详情都带 needs_review（待验收 job 数）与 failed_jobs（失败 job 数，gofer-rj5e），
// 所以「待验收」与「受阻」两段按真实数字画；老服务端缺这两个字段时按 0 处理（段宽为 0，
// 不臆造 job 数）。
export function planProgressModel(plan: Plan): ProgressModel {
  const todos = plan.todo_counts
  const jobs = plan.counts
  // 待验收 / 受阻是 job 口径（一个 job 等验收就是它那条待办在等验收）。
  const review = Math.max(0, plan.needs_review ?? 0)
  const blocked = Math.max(0, plan.failed_jobs ?? jobs?.failed ?? 0)
  let parts: ProgressPart[]

  if (todos && todos.total > 0) {
    const done = todos.done + todos.skipped
    const doing = todos.doing
    const rest = Math.max(0, todos.total - done - doing)
    parts = [
      { family: 'done', count: done },
      { family: 'doing', count: doing },
      { family: 'review', count: review },
      { family: 'blocked', count: blocked },
      { family: 'todo', count: rest },
    ]
  } else if (jobs && jobs.total > 0) {
    parts = [
      { family: 'done', count: jobs.done },
      { family: 'doing', count: jobs.running },
      { family: 'review', count: review },
      { family: 'blocked', count: blocked },
      { family: 'todo', count: jobs.queued },
    ]
  } else {
    parts = PROGRESS_FAMILIES.map((family) => ({ family, count: 0 }))
  }

  const total = parts.reduce((sum, part) => sum + part.count, 0)
  const done = parts[0].count
  const percent = plan.completion?.percent ?? percentOf(done, total)
  return { parts, total, done, percent }
}
