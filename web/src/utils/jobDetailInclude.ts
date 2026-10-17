import type { Job, JobInclude } from '../api/types'

// 详情页打开时一次聚合请求带回的全部附属数据（Q1）。日志正文（分页 / SSE）不在其中。
// 「消息」(messages) 不在基础集合里：只有会话 job 才请求，见 detailIncludes / showJobMessages（gofer-h7hy）。
export const BASE_DETAIL_INCLUDES: JobInclude[] = [
  'events',
  'comments',
  'deliveries',
  'retries',
  'wakeups',
  'pty_sessions',
  'artifacts',
  'session_jobs',
]

// 「消息」区（gofer-chd9）显示规则（gofer-h7hy）：
// exec / 一次性 cli-agent job 的 prompt / 命令页面上方已渲染，再列一遍是重复；
// 只有 ACP 持续会话 job（job.session === true）或确有多轮会话消息的 job（turn_no > 0）
// 才有「发给 agent / agent 回复」的多轮往返可看。job 未知（首次加载前）时不显示、不请求。
export function showJobMessages(job: Pick<Job, 'session' | 'turn_no'> | null | undefined): boolean {
  if (!job) return false
  return job.session === true || (job.turn_no ?? 0) > 0
}

// 该 job 详情聚合请求要带的 include：会话 job 才附加 messages。
export function detailIncludes(job: Pick<Job, 'session' | 'turn_no'> | null | undefined): JobInclude[] {
  return showJobMessages(job) ? [...BASE_DETAIL_INCLUDES, 'messages'] : BASE_DETAIL_INCLUDES
}
