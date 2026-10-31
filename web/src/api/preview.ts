// job 预览（docs/design/2026-10-28-job-preview-design.md §3.4）的接口：
// POST/GET /v1/jobs/{id}/previews、DELETE …/{preview_id}、POST …/{preview_id}/renew。
// 字段是小写 snake_case，与 docs/reference/http-api.md 一致。
//
// 创建响应里的 url 是带一次性 token 的 bearer URL，只在这一次响应里出现（列表不返回）：
// 调用方只能把它放在内存里、用新标签页打开；它不是 API 地址，绝不能经 request() 发出去
// （那会给它加上 Bearer 头），也不能写进 localStorage / sessionStorage。
import { request } from './client'

export interface JobPreview {
  id: string
  job_id?: string
  // 浏览器要打开的预览端口（也在 url 里）；target_port 是 job 里应用监听的端口
  port: number
  target_port?: number
  // active | expired | closed | job_ended | worker_lost …（后端可能新增，界面按原值兜底）
  state?: string
  caller_id?: string
  worker_id?: string
  created_at?: number
  expires_at: number
  close_reason?: string
  // 仅创建响应才有
  url?: string
}

export function createJobPreview(jobId: string, body: { port: number; ttl_sec?: number }): Promise<JobPreview> {
  return request<JobPreview>(`/v1/jobs/${encodeURIComponent(jobId)}/previews`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(body),
  })
}

export async function listJobPreviews(jobId: string): Promise<JobPreview[]> {
  const resp = await request<{ previews?: JobPreview[] }>(`/v1/jobs/${encodeURIComponent(jobId)}/previews`)
  return resp?.previews ?? []
}

export function revokeJobPreview(jobId: string, previewId: string): Promise<JobPreview> {
  return request<JobPreview>(`/v1/jobs/${encodeURIComponent(jobId)}/previews/${encodeURIComponent(previewId)}`, {
    method: 'DELETE',
  })
}

export function renewJobPreview(jobId: string, previewId: string, ttlSec: number): Promise<JobPreview> {
  return request<JobPreview>(
    `/v1/jobs/${encodeURIComponent(jobId)}/previews/${encodeURIComponent(previewId)}/renew`,
    {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ ttl_sec: ttlSec }),
    },
  )
}
