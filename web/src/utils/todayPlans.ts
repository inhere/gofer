// 今天页「进行中的计划」块的纯逻辑（设计稿 2026-11-01 §4.3）：只取 open 且没暂停的计划，
// 最近动过的在前；进度数字走 utils/planProgress 的唯一口径，不在这里另算。
import type { Plan } from '../api/types'
import { planProgress } from './planProgress'

export const TODAY_PLANS_LIMIT = 5

export interface TodayPlanRow {
  id: string
  title: string
  done: number
  total: number
  /** 0–100；没有可衡量的条目时为 null */
  percent: number | null
  /** 链到计划详情页 */
  to: string
}

export interface TodayPlanRows {
  rows: TodayPlanRow[]
  /** 超出展示上限、没列出来的进行中计划数 */
  more: number
}

export function todayPlanRows(plans: readonly Plan[] | null | undefined, limit = TODAY_PLANS_LIMIT): TodayPlanRows {
  const active = (plans ?? [])
    .filter((p) => p.status === 'open' && !p.paused)
    .sort((a, b) => (b.updated_at ?? 0) - (a.updated_at ?? 0))
  const rows = active.slice(0, limit).map((p) => {
    const progress = planProgress(p)
    return {
      id: p.plan_id,
      title: p.title?.trim() || p.plan_id,
      done: progress.done,
      total: progress.total,
      percent: progress.percent,
      to: `/plans/${encodeURIComponent(p.plan_id)}`,
    }
  })
  return { rows, more: Math.max(0, active.length - rows.length) }
}
