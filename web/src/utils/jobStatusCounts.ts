import type { JobStatus } from '../api/types'

// Jobs 页状态栏只给「进行中」的状态计数：终态（完成 / 失败 / 取消 / 超时 / 拒绝）累计起来
// 数字只会越来越大，看了也没用。计数只看最近 3 天（选了更短的时间范围就按那个），
// 免得几千条历史 job 拖慢这一次拉取。
export const STATUS_COUNT_WINDOW_SEC = 3 * 24 * 3600

// 拉计数用的条数上限：3 天内的 job 一次拉完（默认 200 条会截断，计数就不准了）。
export const STATUS_COUNT_LIMIT = 1000

const COUNTED: ReadonlySet<JobStatus> = new Set<JobStatus>([
  'queued',
  'waiting_dir',
  'running',
  'awaiting_input',
  'recovering',
  'pending_interaction',
  'needs_review',
  'awaiting_approval',
  'paused_for_update',
])

/** 状态栏上这个状态要不要显示数字。 */
export function isCountedStatus(status: JobStatus | ''): boolean {
  return status !== '' && COUNTED.has(status)
}

/** 计数用的 since（秒级时间戳）：所选范围与最近 3 天取较近的那个。 */
export function statusCountSince(nowSec: number, selectedSince?: number): number {
  const floor = nowSec - STATUS_COUNT_WINDOW_SEC
  return selectedSince != null && selectedSince > floor ? selectedSince : floor
}
