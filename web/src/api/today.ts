// N3「今天」决策首页的接口（design docs/design/2026-10-09-n3-today-decision-home-design.md §2）。
// 卡片上的操作不走这里：它们直接调用各自现有的写接口（interaction / decision / session /
// job / work-item / plan），这里只有聚合读、处理审计与「已处理」列表。
import { request } from './client'

export type TodayCardKind =
  | 'interaction'
  | 'decision'
  | 'relay'
  | 'review'
  | 'work'
  | 'suggestion'
  | 'merge'
  | 'plan_blocked'
  | 'memory'

export type TodayUrgency = 'now' | 'blocking' | 'normal'

export interface TodayAction {
  id: string
  label: string
  style?: string
  value?: string
  needs_text?: boolean
}

export interface TodayRefs {
  job_id?: string
  interaction_id?: string
  decision_id?: string
  // relay 卡：该会话所有未读的 open turn（「已读」全部确认）
  decision_ids?: string[]
  session_id?: string
  thread_id?: string
  work_item_id?: string
  plan_id?: string
  todo_id?: string
  field?: string
  merge_id?: number
  // memory 卡（P4 记忆整理建议）：建议 id 与它针对的仓库 tracker 记忆
  memory_suggestion_id?: number
  tracker_id?: string
  memory_key?: string
}

// 记忆整理建议的提议值：只有对应动作的字段
export interface MemoryPayload {
  into?: string
  content?: string
  kind?: string
  summary?: string
  keywords?: string[]
}

export type MemoryAction = 'archive' | 'merge' | 'kind' | 'summary' | 'when'

// memory 卡的「详情」：提议 + 记忆现状
export interface TodayMemoryCard {
  tracker_id: string
  key: string
  action: MemoryAction
  payload: MemoryPayload
  current_kind?: string
  current_summary?: string
  age?: string
  content?: string
}

export interface TodayCard {
  key: string
  kind: TodayCardKind
  tag: string
  urgency: TodayUrgency
  blocks: { score: number; items: number; text?: string }
  title: string
  project_key?: string
  agent?: string
  waiting_since: number
  expires_at?: number
  activity_at: number
  summary: string
  review?: { commits: number; adds: number; dels: number; verify?: string; digest?: string }
  suggestions?: Array<{ field: string; value: string; text: string }>
  memory?: TodayMemoryCard
  refs: TodayRefs
  actions: TodayAction[]
  // 管家建议（T4）：action_id 是某个操作的 actionKey；digest ≤5 行，进「详情」
  advice: { text: string; action_id?: string; digest?: string; by?: string; at?: number } | null
  // 从「稍后」回来的卡（T3）：到点 / 相关 job 结束 / 有新动静
  woke?: boolean
  woke_reason?: TodayWokeReason
}

export type TodayWokeReason = 'time' | 'job' | 'activity'

export interface TodayStatus {
  usage_today: { jobs: number; total_tokens: number; cost_usd: number; session_tokens: number }
  steward_today: { enabled: boolean; state?: string; notes: number; summaries: number; advice?: number; review?: string }
  runners: { online: number; total: number; offline?: string[]; running_jobs: number }
  version: string
  alerts: string[]
}

export interface TodayResponse {
  digest: {
    since_last: { since: number; jobs_done: number; jobs_failed: number; commits: number }
    title: string
    text: string
    commentary?: string
  }
  decisions: TodayCard[]
  snoozed: number
  status: TodayStatus
  generated_at: number
}

export interface TodayHandled {
  at: number
  actor?: string
  card_key: string
  kind?: string
  title?: string
  action_id: string
  label?: string
  advice_action_id?: string
  advice_text?: string
  advice_label?: string
  via_advice?: boolean
}

export function getToday(opts: { since?: number; includeExec?: boolean } = {}): Promise<TodayResponse> {
  const q = new URLSearchParams()
  if (opts.since && opts.since > 0) q.set('since', String(Math.floor(opts.since)))
  if (opts.includeExec) q.set('include_exec', '1')
  const qs = q.toString()
  return request<TodayResponse>(`/v1/today${qs ? `?${qs}` : ''}`)
}

export function recordTodayAction(body: {
  card_key: string
  action_id: string
  advice_action_id?: string
  advice_text?: string
  advice_label?: string
  via_advice?: boolean
  title?: string
  label?: string
  kind?: string
}): Promise<TodayHandled> {
  return request<TodayHandled>('/v1/today/actions', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(body),
  })
}

export function listTodayHandled(days = 7): Promise<{ handled: TodayHandled[]; days: number }> {
  return request<{ handled: TodayHandled[]; days: number }>(`/v1/today/handled?days=${days}`)
}

// 「稍后」（T3，design §2.3）：until_at 与 until_job_id 二选一；服务端同时记 today.action 审计。
export interface TodaySnoozed {
  card_key: string
  kind: string
  tag?: string
  title: string
  project_key?: string
  until_at?: number
  until_job_id?: string
  expires_at?: number
  created_at: number
}

export function snoozeTodayCard(body: { card_key: string; until_at?: number; until_job_id?: string }): Promise<TodaySnoozed> {
  return request<TodaySnoozed>('/v1/today/snooze', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(body),
  })
}

export function unsnoozeTodayCard(cardKey: string): Promise<{ card_key: string; unsnoozed: boolean }> {
  return request<{ card_key: string; unsnoozed: boolean }>(`/v1/today/snooze/${encodeURIComponent(cardKey)}`, {
    method: 'DELETE',
  })
}

export function listTodaySnoozed(includeExec = false): Promise<{ snoozed: TodaySnoozed[] }> {
  return request<{ snoozed: TodaySnoozed[] }>(`/v1/today/snoozed${includeExec ? '?include_exec=1' : ''}`)
}

// P4 记忆整理建议：采纳 = 把改动写到 server 上的记忆副本（各仓库下次 repo sync 时拿到）；忽略后 30 天内不再提。
export function adoptMemorySuggestion(id: number): Promise<unknown> {
  return request(`/v1/memory-suggestions/${id}/adopt`, { method: 'POST' })
}

export function dismissMemorySuggestion(id: number): Promise<unknown> {
  return request(`/v1/memory-suggestions/${id}/dismiss`, { method: 'POST' })
}
