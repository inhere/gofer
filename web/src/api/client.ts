// Typed API client。统一注入 Authorization；401 -> clearToken + 触发 unauthorized 回调并 throw。
// 非 2xx 解析 {error, detail} 抛出可读错误；text 端点返回字符串。

import { getToken, triggerUnauthorized } from '../store/auth'
import { streamJob } from './sse'
import type {
  AgentsResp,
  Interaction,
  Job,
  JobsResp,
  JobStatus,
  ListJobsOpts,
  LogStream,
  ProjectDetail,
  ProjectsResp,
  RunnersResp,
} from './types'
import type { StreamJobOpts } from './sse'

interface ErrorBody {
  error?: string
  detail?: string
}

function authHeaders(extra?: Record<string, string>): Record<string, string> {
  const headers: Record<string, string> = { ...extra }
  const token = getToken()
  if (token) {
    headers['Authorization'] = `Bearer ${token}`
  }
  return headers
}

async function raiseForStatus(res: Response): Promise<never> {
  let msg = `请求失败：HTTP ${res.status}`
  try {
    const body = (await res.json()) as ErrorBody
    if (body.error || body.detail) {
      msg = [body.error, body.detail].filter(Boolean).join(' - ')
    }
  } catch {
    // 非 JSON 错误体，沿用默认
  }
  throw new Error(msg)
}

async function request<T>(path: string, init?: RequestInit): Promise<T> {
  const res = await fetch(path, {
    ...init,
    headers: authHeaders(init?.headers as Record<string, string> | undefined),
  })
  if (res.status === 401) {
    triggerUnauthorized()
    throw new Error('未授权（401）：token 无效或已失效')
  }
  if (!res.ok) {
    return raiseForStatus(res)
  }
  return (await res.json()) as T
}

async function requestText(path: string, init?: RequestInit): Promise<string> {
  const res = await fetch(path, {
    ...init,
    headers: authHeaders(init?.headers as Record<string, string> | undefined),
  })
  if (res.status === 401) {
    triggerUnauthorized()
    throw new Error('未授权（401）：token 无效或已失效')
  }
  if (!res.ok) {
    return raiseForStatus(res)
  }
  return res.text()
}

export function listProjects(): Promise<ProjectsResp> {
  return request<ProjectsResp>('/v1/projects')
}

export function getProject(key: string): Promise<ProjectDetail> {
  return request<ProjectDetail>(`/v1/projects/${encodeURIComponent(key)}`)
}

export function listAgents(): Promise<AgentsResp> {
  return request<AgentsResp>('/v1/agents')
}

// 运行器舰队状态（worker / peer-http / local）。Runners 视图轮询读取。
export function listRunners(): Promise<RunnersResp> {
  return request<RunnersResp>('/v1/runners')
}

export function listJobs(opts?: ListJobsOpts): Promise<JobsResp> {
  const params = new URLSearchParams()
  if (opts?.status) {
    params.set('status', opts.status)
  }
  if (opts?.project) {
    params.set('project', opts.project)
  }
  if (opts?.limit != null) {
    params.set('limit', String(opts.limit))
  }
  const qs = params.toString()
  return request<JobsResp>(`/v1/jobs${qs ? `?${qs}` : ''}`)
}

export function getJob(id: string): Promise<Job> {
  return request<Job>(`/v1/jobs/${encodeURIComponent(id)}`)
}

export function logsTail(id: string, stream: LogStream): Promise<string> {
  return requestText(`/v1/jobs/${encodeURIComponent(id)}/logs/${stream}`)
}

export function cancelJob(id: string): Promise<Job> {
  return request<Job>(`/v1/jobs/${encodeURIComponent(id)}/cancel`, {
    method: 'POST',
  })
}

// 运行中交互：作答某个 interaction，返回更新后的 Interaction（status=answered）
export function answerInteraction(
  id: string,
  iid: string,
  answer: string,
): Promise<Interaction> {
  return request<Interaction>(
    `/v1/jobs/${encodeURIComponent(id)}/interactions/${encodeURIComponent(iid)}/answer`,
    {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ answer }),
    },
  )
}

// 断线兜底：拉取某 job 的全部交互（SSE 重连前对齐状态）
export function getInteractions(
  id: string,
): Promise<{ interactions: Interaction[] }> {
  return request<{ interactions: Interaction[] }>(
    `/v1/jobs/${encodeURIComponent(id)}/interactions`,
  )
}

// SSE 流（转发到 api/sse.ts）
export function streamJobLogs(id: string, opts: StreamJobOpts): Promise<void> {
  return streamJob(id, opts)
}

// 状态 -> 视觉 token 颜色（供 Board/JobDetail 等复用）
const STATUS_COLOR: Record<JobStatus, string> = {
  running: 'var(--run)',
  pending_interaction: 'var(--phosphor)',
  done: 'var(--done)',
  failed: 'var(--fail)',
  timeout: 'var(--fail)',
  queued: 'var(--queue)',
  cancelled: 'var(--queue)',
}

export function statusColor(status: JobStatus): string {
  return STATUS_COLOR[status] ?? 'var(--queue)'
}
