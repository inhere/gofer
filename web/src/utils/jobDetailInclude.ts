import type { JobInclude } from '../api/types'

// 详情页打开时一次聚合请求带回的全部附属数据（Q1）。日志正文（分页 / SSE）不在其中。
// messages 是「消息」区：发给 agent 的消息与 agent 回复摘要（gofer-chd9）。
export const DETAIL_INCLUDES: JobInclude[] = [
  'events',
  'comments',
  'deliveries',
  'retries',
  'wakeups',
  'pty_sessions',
  'artifacts',
  'session_jobs',
  'messages',
]
