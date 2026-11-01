// 收件箱分栏的接口（design docs/design/2026-10-28-inbox-triage-design.md §3.5 / §6a）：
// GET /v1/inbox 返回「要处理（action）」「仅知会（fyi）」两栏与未读计数，
// POST /v1/inbox/state 写个人的已读 / 清理 / 恢复状态（不改源事项）。
// 卡片上的业务动作不走这里，仍调用各自现有的写接口。
import { request } from './client'
import type { TodayAction, TodayRefs, WaitingCategory } from './today'

export type InboxColumn = 'action' | 'fyi'
export type InboxItemState = 'unread' | 'seen' | 'cleared'

export interface InboxItem {
  key: string
  column: InboxColumn
  // Today 卡的 kind，另有 job.terminal（你本人提交的任务完成，fyi）与 job.failed（你提交的任务失败，action）
  kind: string
  title: string
  summary: string
  source_revision: string
  state: InboxItemState
  actions: TodayAction[]
  refs: TodayRefs
  deep_link?: string
  project_key?: string
  activity_at: number
  // 「等我」类别与文案（仅 action 栏条目）
  waiting?: WaitingCategory
  waiting_label?: string
}

export interface InboxResponse {
  columns: Record<InboxColumn, InboxItem[]>
  unread: Record<InboxColumn, number>
  // 顶栏徽标：只算 action 栏的未读
  total_unread: number
  generated_at: number
}

export type InboxStateAction = 'seen' | 'clear' | 'restore'

export interface InboxStateRequest {
  action: InboxStateAction
  // 省略 keys = 对当前（column 内）全部条目生效；restore 必须带 keys
  keys?: string[]
  column?: InboxColumn
}

export function getInbox(opts: { includeCleared?: boolean; includeExec?: boolean } = {}): Promise<InboxResponse> {
  const q = new URLSearchParams()
  if (opts.includeCleared) q.set('include_cleared', '1')
  if (opts.includeExec) q.set('include_exec', '1')
  const qs = q.toString()
  return request<InboxResponse>(`/v1/inbox${qs ? `?${qs}` : ''}`)
}

export function postInboxState(body: InboxStateRequest): Promise<{ updated: number; action: InboxStateAction }> {
  return request<{ updated: number; action: InboxStateAction }>('/v1/inbox/state', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(body),
  })
}
