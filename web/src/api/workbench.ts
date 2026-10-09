import { request } from './client'
import type {
  WorkbenchStatus,
  WorkbenchLayoutResp,
  WorkbenchReviewInput,
  WorkbenchThreadPatch,
  WorkbenchThreadDiff,
  WorkbenchThreadsResp,
  WorkbenchTurnResult,
} from './types'

export function getWorkbenchLayout(): Promise<WorkbenchLayoutResp> {
  return request<WorkbenchLayoutResp>('/v1/workbench/layout')
}

export function putWorkbenchLayout(version: number, body: unknown): Promise<WorkbenchLayoutResp> {
  return request<WorkbenchLayoutResp>('/v1/workbench/layout', {
    method: 'PUT',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ version, body }),
  })
}

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

export function getWorkbenchThreadDiff(threadID: string): Promise<WorkbenchThreadDiff> {
  return request<WorkbenchThreadDiff>(
    `/v1/workbench/threads/${encodeURIComponent(threadID)}/diff`,
  )
}

export function reviewWorkbenchThread(
  threadID: string,
  input: WorkbenchReviewInput,
): Promise<WorkbenchTurnResult> {
  return request<WorkbenchTurnResult>(
    `/v1/workbench/threads/${encodeURIComponent(threadID)}/review`,
    {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(input),
    },
  )
}
