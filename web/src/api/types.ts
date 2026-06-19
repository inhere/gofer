// 后端 API 形状（snake_case，全部 /v1 前缀）。与 internal handler 对齐。

export type JobStatus =
  | 'queued'
  | 'running'
  | 'pending_interaction'
  | 'done'
  | 'failed'
  | 'cancelled'
  | 'timeout'

export interface Job {
  id: string
  project_key: string
  agent: string
  runner: string
  status: JobStatus
  exit_code: number
  cwd: string
  result_dir: string
  // Unix 秒（后端 int64）
  started_at: number
  ended_at?: number
  error?: string
}

export interface HealthResp {
  ok: boolean
  service: string
  server_time: number
}

export interface ProjectsResp {
  projects: string[]
}

export interface ProjectDetail {
  key: string
  host_path: string
  container_path?: string
  default_agent?: string
  allowed_agents?: string[]
  allowed_runners?: string[]
  allow_exec: boolean
  max_concurrent_jobs?: number
}

export interface AgentInfo {
  key: string
  type: string
  available: boolean
  version?: string
  error?: string
}

export interface AgentsResp {
  agents: AgentInfo[]
}

export interface JobsResp {
  jobs: Job[]
}

export interface ListJobsOpts {
  status?: JobStatus
  project?: string
  limit?: number
}

export type LogStream = 'stdout' | 'stderr'

// 运行中交互：question 文本问答 / choice 选项 / confirmation 确认
export type InteractionType = 'question' | 'choice' | 'confirmation'
export type InteractionStatus = 'pending' | 'answered' | 'cancelled'

export interface InteractionOption {
  value: string
  label?: string
}

export interface Interaction {
  id: string
  job_id: string
  type: InteractionType
  prompt: string
  options?: InteractionOption[]
  status: InteractionStatus
  answer?: string
  // Unix 秒
  created_at: number
  answered_at?: number
}

// SSE 事件（解析后）
// log-rotated：后端日志文件发生轮转（offset 重置），前端需清空该 stream 已缓冲文本后续读。
export type SSEEventType =
  | 'status'
  | 'log'
  | 'log-rotated'
  | 'interaction'
  | 'end'

export interface SSELogData {
  stream: LogStream
  seq: number
  text: string
}

// log-rotated 事件载荷：哪个 stream 轮转了（前端据此清空该 stream 缓冲）。
export interface SSELogRotatedData {
  stream: LogStream
  seq: number
}

export interface SSEInteractionData {
  action: 'open' | 'answered' | 'cancelled'
  interaction: Interaction
}

export interface SSEEvent {
  type: SSEEventType
  // status -> Job; log -> SSELogData; interaction -> SSEInteractionData; end -> {}
  data: unknown
}
