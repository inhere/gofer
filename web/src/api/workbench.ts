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
  // 'summary' trims every thread to its list fields; `full` names the threads (the
  // ones open in the console) that must stay complete.
  detail?: 'summary'
  full?: string[]
}

export function listWorkbenchThreads(query: WorkbenchThreadQuery = {}): Promise<WorkbenchThreadsResp> {
  const params = new URLSearchParams()
  if (query.project) params.set('project', query.project)
  if (query.status) params.set('status', query.status)
  if (query.q) params.set('q', query.q)
  if (query.since != null && query.since >= 0) params.set('since', String(query.since))
  if (query.detail) params.set('detail', query.detail)
  for (const id of query.full ?? []) params.append('full', id)
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

// turnWorkbenchThread 发一个回合：attachmentIDs 是先前 POST /v1/attachments 拿到的附件 id
// （最多 6 个，顺序即送达顺序）；空数组时不带这个字段，与旧服务端保持兼容。
export function turnWorkbenchThread(
  threadID: string,
  text: string,
  attachmentIDs: string[] = [],
): Promise<WorkbenchTurnResult> {
  const body: { text: string; attachments?: string[] } = { text }
  if (attachmentIDs.length > 0) body.attachments = attachmentIDs
  return request<WorkbenchTurnResult>(
    `/v1/workbench/threads/${encodeURIComponent(threadID)}/turn`,
    {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(body),
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
