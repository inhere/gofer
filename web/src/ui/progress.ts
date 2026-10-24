// 分段进度（设计稿 §8 的 ProgressBar）：把 todo / job 的计数折算成 5 段
// （完成 / 进行中 / 待验收 / 受阻 / 未开始）加 done/total 与百分比。
//
// done / total / percent 一律来自 utils/planProgress 的唯一口径（gofer-j4j1）：完成 / 总数，
// 跳过算完成，受阻 / 失败不算；本文件只负责把它们摊成色带的分段，绝不自己另算百分比。
import type { Plan } from '../api/types'
import { percentOf, planProgress, type PlanProgress } from '../utils/planProgress'

export { percentOf }

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

// 列表接口与详情都带 needs_review（待验收 job 数）与 failed_jobs（失败 job 数，gofer-rj5e），
// 所以「待验收」与「受阻」两段按真实数字画；老服务端缺这两个字段时按 0 处理（段宽为 0，
// 不臆造 job 数）。
export function planProgressModel(plan: Plan): ProgressModel {
  const progress = planProgress(plan)
  return { parts: progressParts(plan, progress), ...progress }
}

// progressParts：待验收 / 受阻 / 进行中是 job 或 todo 口径，必须从「未完成」里扣出来，
// 各段之和才等于同一个 total——否则 9 个完成 + 1 个受阻的 10 项计划会摊成 11 段宽
// （曾经显示成 9/11 90%，与顶部 9/10 对不上）。扣不下的溢出部分丢掉，宁可少画一段。
function progressParts(plan: Plan, progress: PlanProgress): ProgressPart[] {
  let remaining = Math.max(0, progress.total - progress.done)
  const take = (n: number | undefined): number => {
    const count = Math.min(Math.max(0, n ?? 0), remaining)
    remaining -= count
    return count
  }
  const blocked = take(plan.failed_jobs ?? plan.counts?.failed)
  const review = take(plan.needs_review)
  const doing = take(plan.todo_counts?.doing ?? plan.counts?.running)
  return [
    { family: 'done', count: progress.done },
    { family: 'doing', count: doing },
    { family: 'review', count: review },
    { family: 'blocked', count: blocked },
    { family: 'todo', count: remaining },
  ]
}
