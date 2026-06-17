// 后端 API 形状（snake_case，全部 /v1 前缀）。与 internal handler 对齐。

export type JobStatus =
  | 'queued'
  | 'running'
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

// SSE 事件（解析后）
export type SSEEventType = 'status' | 'log' | 'end'

export interface SSELogData {
  stream: LogStream
  seq: number
  text: string
}

export interface SSEEvent {
  type: SSEEventType
  // status -> Job; log -> SSELogData; end -> {}
  data: unknown
}
