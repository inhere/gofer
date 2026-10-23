// 状态显示的唯一映射（用户 2026-10-19 的口径）：接口里的值不变（open / done / archived /
// blocked / running / …），只在界面上换成当前语言的文案。各页面、徽标、筛选芯片都从这里取，
// 不要在模板或各页的 statusOptions 里再写一份。
//
// 同一个值在不同实体上是同一个词（open 一律「进行中」），所以这里按值建一张表，
// 各实体只导出带类型的取词函数。文案在 i18n 字典的 status.<值>（P6-b 起走 t()）。
import type { JobStatus, PlanStatus, WorkflowStatus } from '../api/types'
import { t, te } from '../i18n'

// issue 的状态没有单独的类型（tracker 侧是自由字符串），这里按接口实际取值列出。
export type IssueStatus = 'open' | 'in_progress' | 'blocked' | 'closed'

// 「等人」的那几个状态带符号前缀（与原来的徽标口径一致：符号本身就是"需要人"的信号）。
// 符号不是文案、不进字典；词本身照常翻译。
const STATUS_SYMBOL: Record<string, string> = {
  pending_interaction: '⚠ ',
  needs_review: '⚠ ',
  waiting_dir: '⏳ ',
  awaiting_approval: '⏸ ',
  paused_for_update: '⏳ ',
}

// statusText：状态值 → 显示文案；没有登记的值原样显示（新状态不会在界面上变成空白）。
export function statusText(status: string | undefined | null): string {
  if (!status) return ''
  const key = `status.${status}`
  if (!te(key)) return status
  return `${STATUS_SYMBOL[status] ?? ''}${t(key)}`
}

export function planStatusText(status: PlanStatus): string {
  return statusText(status)
}

export function issueStatusText(status: IssueStatus): string {
  return statusText(status)
}

export function jobStatusText(status: JobStatus): string {
  return statusText(status)
}

export function workflowStatusText(status: WorkflowStatus): string {
  return statusText(status)
}

export interface StatusOption<T extends string> {
  value: T | ''
  label: string
}

// 筛选下拉 / 芯片的选项：值与文案成对给出，「全部」排第一（空值 = 不过滤）。
function optionsOf<T extends string>(values: readonly T[]): Array<StatusOption<T>> {
  return [{ value: '', label: t('act.all') }, ...values.map((v) => ({ value: v, label: statusText(v) }))]
}

export const PLAN_STATUS_VALUES: PlanStatus[] = ['open', 'done', 'archived', 'blocked']
export const ISSUE_STATUS_VALUES: IssueStatus[] = ['open', 'in_progress', 'blocked', 'closed']
export const WORKFLOW_STATUS_VALUES: WorkflowStatus[] = ['running', 'done', 'failed', 'cancelled']

export function planStatusOptions(): Array<StatusOption<PlanStatus>> {
  return optionsOf(PLAN_STATUS_VALUES)
}

export function workflowStatusOptions(): Array<StatusOption<WorkflowStatus>> {
  return optionsOf(WORKFLOW_STATUS_VALUES)
}

// job 状态筛选：除了全部 job 状态，还带一条「全部时间」之外的语义——这里只按状态列。
export const JOB_STATUS_VALUES: JobStatus[] = [
  'queued',
  'running',
  'waiting_dir',
  'recovering',
  'pending_interaction',
  'needs_review',
  'awaiting_approval',
  'paused_for_update',
  'done',
  'failed',
  'cancelled',
  'timeout',
  'rejected',
]

export function jobStatusOptions(): Array<StatusOption<JobStatus>> {
  return optionsOf(JOB_STATUS_VALUES)
}
