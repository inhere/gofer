import { request } from './client'
import type {
  WorkbenchStatus,
  WorkbenchThreadPatch,
  WorkbenchThreadsResp,
  WorkbenchTurnResult,
} from './types'

export interface WorkbenchThreadQuery {
  project?: string
  status?: WorkbenchStatus | ''
  q?: string
  since?: number
}

export function listWorkbenchThreads(query: WorkbenchThreadQuery = {}): Promise<WorkbenchThreadsResp> {
  const params = new URLSearchParams()
  if (query.project) params.set('project', query.project)
  if (query.status) params.set('status', query.status)
  if (query.q) params.set('q', query.q)
  if (query.since != null && query.since >= 0) params.set('since', String(query.since))
  const suffix = params.size > 0 ? `?${params.toString()}` : ''
  return request<WorkbenchThreadsResp>(`/v1/workbench/threads${suffix}`)
}

export function patchWorkbenchThread(
  threadID: string,
  patch: WorkbenchThreadPatch,
): Promise<{ thread_id: string; title: string; seen_at: number; pinned: boolean }> {
  return request<{ thread_id: string; title: string; seen_at: number; pinned: boolean }>(
    `/v1/workbench/threads/${encodeURIComponent(threadID)}`,
    {
    method: 'PATCH',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(patch),
    },
  )
}

export function markAllWorkbenchThreadsSeen(): Promise<{ seen_baseline: number }> {
  return request<{ seen_baseline: number }>('/v1/workbench/threads/seen-all', {
    method: 'POST',
  })
}

export function turnWorkbenchThread(threadID: string, text: string): Promise<WorkbenchTurnResult> {
  return request<WorkbenchTurnResult>(
    `/v1/workbench/threads/${encodeURIComponent(threadID)}/turn`,
    {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ text }),
    },
  )
}
