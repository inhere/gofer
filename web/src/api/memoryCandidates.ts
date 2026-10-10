// 经验候选（gofer-3nxa.2）：开启 knowledge_capture 的 job 交付后，服务端把汇报「## 可复用经验」
// 小节的条目记成候选；人在验收面板「经验」页签逐条接受（写成作用域记忆，来源 job:<id>）或拒绝。
import { request } from './client'

export type MemoryCandidateStatus = 'pending' | 'accepted' | 'rejected'

export interface MemoryCandidate {
  id: number
  job_id: string
  project_key: string
  text: string
  status: MemoryCandidateStatus
  memory_key?: string
  created_at: number
  decided_at?: number
  decided_by?: string
}

export interface AcceptMemoryCandidateInput {
  key: string
  kind?: 'rule' | 'note'
  summary?: string
  global?: boolean
  project?: string
}

// status 省略 = 只列待处理；'all' = 全部状态。
export function listMemoryCandidates(q: { job_id?: string; project?: string; status?: string }): Promise<MemoryCandidate[]> {
  const params = new URLSearchParams()
  for (const [k, v] of Object.entries(q)) {
    if (v) {
      params.set(k, v)
    }
  }
  const qs = params.toString()
  return request<{ candidates: MemoryCandidate[] }>(`/v1/memory-candidates${qs ? `?${qs}` : ''}`).then(
    (r) => r.candidates ?? [],
  )
}

export function acceptMemoryCandidate(id: number, input: AcceptMemoryCandidateInput): Promise<{ candidate: MemoryCandidate }> {
  return request<{ candidate: MemoryCandidate }>(`/v1/memory-candidates/${id}/accept`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(input),
  })
}

export function rejectMemoryCandidate(id: number): Promise<{ candidate: MemoryCandidate }> {
  return request<{ candidate: MemoryCandidate }>(`/v1/memory-candidates/${id}/reject`, { method: 'POST' })
}
