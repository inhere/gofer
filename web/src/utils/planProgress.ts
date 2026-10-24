// 计划进度的唯一口径（gofer-j4j1）：完成 / 总数——跳过算完成，受阻 / 失败 / 待验收都不算，
// 百分比永远由这一对数字折算（不再单独沿用服务端的 percent，否则会和显示出来的分子分母对不上）。
//
// 所有显示计划进度的地方都从这里取数：计划详情顶部（ui/progress 的 planProgressModel）、
// 详情操作条的清单进度、计划总览列表、看板列头与「今天」页的并行泳道。
//
// 服务端事实：todo 生命周期 pending → ready → doing → done|skipped（skipped = 没干活但算了结）；
// 计划没有任何待办时回落到它的 job 计数（done / total，job 含重试与失败尝试，只作兜底）。
import type { PlanCounts, PlanTodoCounts } from '../api/types'

export interface PlanProgress {
  done: number
  total: number
  /** 0–100 的整数；没有可衡量的条目时为 null（"没得量" ≠ 0%） */
  percent: number | null
}

// 进度明细里的一条：计划的 todos 与看板/清单的行都结构兼容。
export interface ProgressTodo {
  status?: string
  done?: boolean
}

// 三种输入任给其一：明细（详情页 / 看板）、todo 汇总（总览列表）、job 汇总（无待办的计划）。
export interface PlanProgressSource {
  /** 明细：给了就按条目实际状态数（计划详情的 todos 已全量返回） */
  todos?: readonly ProgressTodo[]
  todo_counts?: PlanTodoCounts
  counts?: PlanCounts
}

// 一条待办是否算完成：跳过也算完成（人工标记的终态）。
export function isTodoComplete(todo: ProgressTodo): boolean {
  return todo.done === true || todo.status === 'done' || todo.status === 'skipped'
}

// percentOf 是唯一的百分比折算：total <= 0 时没有答案（null），结果夹在 0–100。
export function percentOf(done: number, total: number): number | null {
  if (!Number.isFinite(total) || !Number.isFinite(done) || total <= 0) return null
  const pct = Math.round((done / total) * 100)
  return Math.min(100, Math.max(0, pct))
}

export function planProgress(source: PlanProgressSource): PlanProgress {
  const { done, total } = progressCounts(source)
  return { done, total, percent: percentOf(done, total) }
}

function progressCounts(source: PlanProgressSource): { done: number; total: number } {
  const todos = source.todos
  if (todos) {
    return { done: todos.filter(isTodoComplete).length, total: todos.length }
  }
  const tc = source.todo_counts
  if (tc && tc.total > 0) {
    return { done: tc.done + tc.skipped, total: tc.total }
  }
  const jc = source.counts
  if (jc && jc.total > 0) {
    return { done: jc.done, total: jc.total }
  }
  return { done: 0, total: 0 }
}
