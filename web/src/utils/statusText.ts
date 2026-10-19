// 状态显示的唯一映射（用户 2026-10-19 的口径）：接口里的值不变（open / done / archived /
// blocked / running / …），只在界面上换成中文。各页面、徽标、筛选芯片都从这里取，不要在
// 模板或各页的 statusOptions 里再写一份。
//
// 同一个值在不同实体上是同一个词（open 一律「进行中」），所以这里按值建一张表，
// 各实体只导出带类型的取词函数。
import type { JobStatus, PlanStatus, WorkflowStatus } from '../api/types'

// issue 的状态没有单独的类型（tracker 侧是自由字符串），这里按接口实际取值列出。
export type IssueStatus = 'open' | 'in_progress' | 'blocked' | 'closed'

// STATUS_TEXT：状态值 → 中文文案。job 的状态里「等人」的那几个带符号前缀（与原来的徽标
// 口径一致：符号本身就是"需要人"的信号）。
export const STATUS_TEXT: Record<string, string> = {
  // 通用（plan / issue / 工作项 / thread）
  open: '进行中',
  in_progress: '处理中',
  done: '已完成',
  closed: '已关闭',
  archived: '已归档',
  blocked: '受阻',
  // job / workflow
  queued: '排队中',
  running: '运行中',
  recovering: '恢复中',
  failed: '失败',
  cancelled: '已取消',
  timeout: '超时',
  rejected: '已拒绝',
  awaiting_input: '等待输入',
  pending_interaction: '⚠ 待应答',
  needs_review: '⚠ 待验收',
  waiting_dir: '⏳ 等目录',
  awaiting_approval: '⏸ 待批准',
  paused_for_update: '⏳ 等更新',
  // plan
  draft: '草稿',
  // work item
  active: '进行中',
  needs_me: '等我',
  waiting_resource: '等资源',
  needs_onsite: '需现场',
  review: '待验收',
  parked: '已搁置',
  dropped: '已放弃',
}

// statusText：状态值 → 显示文案；没有登记的值原样显示（新状态不会在界面上变成空白）。
export function statusText(status: string | undefined | null): string {
  if (!status) return ''
  return STATUS_TEXT[status] ?? status
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
  return [{ value: '', label: '全部' }, ...values.map((v) => ({ value: v, label: statusText(v) }))]
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
