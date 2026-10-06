// 管家（steward，W2b）的 REST 封装：状态 / 启停 / 提问 / 巡检、管家笔记（版本化 Markdown）、
// 设置写入与合并建议。与 client.ts 共用 request（同一份鉴权与错误解析）。
import { ApiError, request } from './client'
import type { ConfigWriteResp } from './types'

export type StewardState = 'not_started' | 'running' | 'idle'

export interface StewardReview {
  id: number
  day: string
  state: string
  trigger?: string
  item_ids?: string
  summary?: string
  job_id?: string
  started_at: number
  ended_at?: number
  error?: string
}

export interface StewardStatus {
  enabled: boolean
  agent: string
  project: string
  state: StewardState
  job_id?: string
  job_agent?: string
  started_at?: number
  last_active_at?: number
  turn_no?: number
  idle_end_min: number
  review_time: string
  event_wake: boolean
  agent_error?: string
  notes_version: number
  notes_bytes: number
  notes_need_slim: boolean
  pending_events: number
  last_review?: StewardReview
}

export interface StewardSettings {
  enabled: boolean
  agent: string
  project: string
  review_time: string
  idle_end_min: number
  review_max_items: number
  event_wake: boolean
  event_throttle_min: number
  review_time_explicit: boolean
}

export interface StewardResp {
  status: StewardStatus
  settings: StewardSettings
}

export interface StewardNotes {
  version: number
  body: string
  by?: string
  at?: number
}

export interface StewardNotesInfo {
  version: number
  bytes: number
  need_slim: boolean
}

export interface MergeSuggestion {
  id: number
  target_id: string
  source_id: string
  reason?: string
  by: string
  at: number
  state: string
}

const JSON_HEADERS = { 'Content-Type': 'application/json' }

export function getSteward(): Promise<StewardResp> {
  return request<StewardResp>('/v1/steward')
}

export function startSteward(): Promise<{ job_id: string; started: boolean; status: StewardStatus }> {
  return request('/v1/steward/start', { method: 'POST' })
}

export function restartSteward(): Promise<{ job_id: string; started: boolean; status: StewardStatus }> {
  return request('/v1/steward/restart', { method: 'POST' })
}

export function stopSteward(): Promise<{ stopped: boolean; status: StewardStatus }> {
  return request('/v1/steward/stop', { method: 'POST' })
}

export function askSteward(text: string): Promise<{ job_id: string; started: boolean }> {
  return request('/v1/steward/ask', { method: 'POST', headers: JSON_HEADERS, body: JSON.stringify({ text }) })
}

export function reviewSteward(force = false): Promise<{ review_id: number; skipped: boolean; reason?: string; job_id?: string; items?: string[]; events: number }> {
  return request('/v1/steward/review', { method: 'POST', headers: JSON_HEADERS, body: JSON.stringify({ force }) })
}

export function getStewardNotes(version?: number): Promise<{ notes: StewardNotes; info: StewardNotesInfo }> {
  const qs = version && version > 0 ? `?version=${version}` : ''
  return request(`/v1/steward/notes${qs}`)
}

export async function getStewardNotesHistory(): Promise<StewardNotes[]> {
  const r = await request<{ history: StewardNotes[] }>('/v1/steward/notes?history=1')
  return r.history ?? []
}

export function putStewardNotes(body: string, version: number): Promise<{ notes: StewardNotes; info: StewardNotesInfo }> {
  return request('/v1/steward/notes', { method: 'PUT', headers: JSON_HEADERS, body: JSON.stringify({ body, version }) })
}

export function putConfigSteward(body: Partial<Omit<StewardSettings, 'review_time_explicit'>>): Promise<ConfigWriteResp> {
  return request<ConfigWriteResp>('/v1/config/steward', { method: 'PUT', headers: JSON_HEADERS, body: JSON.stringify(body) })
}

export async function listMergeSuggestions(): Promise<MergeSuggestion[]> {
  const r = await request<{ suggestions: MergeSuggestion[] }>('/v1/work-items/merge-suggestions')
  return r.suggestions ?? []
}

export function acceptMergeSuggestion(id: number): Promise<unknown> {
  return request(`/v1/work-items/merge-suggestions/${id}/accept`, { method: 'POST' })
}

export function dismissMergeSuggestion(id: number): Promise<unknown> {
  return request(`/v1/work-items/merge-suggestions/${id}/dismiss`, { method: 'POST' })
}

export { ApiError }
