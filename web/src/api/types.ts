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
  // 可选的人类可读任务名（后端 omitempty；来自原始请求，经 request_json 回放）
  title?: string
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

// 运行器（/v1/runners，C6）。三类：worker / peer-http / local。
// 字段与 internal/httpapi/runner_handler.go 的 runnerView/probeView/workerView 对齐。
export type RunnerType = 'worker' | 'peer-http' | 'local'

// up/down：peer-http；connected/disconnected：worker；local 恒 up；unknown：尚无信号。
export type RunnerStatus =
  | 'up'
  | 'down'
  | 'connected'
  | 'disconnected'
  | 'unknown'

// peer-http 探活明细（毫秒时间戳/延迟/错误）。
export interface RunnerProbe {
  // Unix 毫秒（后端 server_time 同制）；0 表示从未探活
  checked_at: number
  latency_ms: number
  error?: string
}

// worker 连接明细。heartbeat_age_ms 由后端读取时即时计算。
export interface RunnerWorker {
  // Unix 毫秒
  last_heartbeat: number
  heartbeat_age_ms: number
  in_flight: number
  labels?: string[]
}

export interface Runner {
  name: string
  type: RunnerType
  status: RunnerStatus
  // peer-http
  base_url?: string
  probe?: RunnerProbe
  // worker
  worker_id?: string
  worker?: RunnerWorker
}

export interface RunnersResp {
  runners: Runner[]
}

// /v1/meta（G4，design §6.4）：提交表单一次取齐的选项聚合。
// 字段与 internal/httpapi/meta_handler.go 的 metaResp/metaProject/... 对齐。
export interface MetaProject {
  key: string
  allowed_agents: string[]
  allowed_runners: string[]
  default_agent?: string
}

export interface MetaAgent {
  key: string
  // cli-agent | exec（前端据此切换 prompt 文本域 / command 输入）
  type: string
}

export interface MetaRunner {
  name: string
  type: RunnerType
}

export interface MetaWorker {
  id: string
  labels?: string[]
  connected: boolean
}

export interface MetaResp {
  projects: MetaProject[]
  agents: MetaAgent[]
  runners: MetaRunner[]
  workers: MetaWorker[]
}

// POST /v1/jobs 提交载荷（与 internal/job/model.go 的 JobRequest 对齐）。
// caller_id 由服务端覆盖，前端不发。
export interface SubmitJobReq {
  project_key: string
  agent: string
  runner: string
  prompt?: string
  cmd?: string[]
  cwd?: string
  timeout_sec?: number
  title?: string
  worker_id?: string
  worker_labels?: string[]
  sync?: boolean
}

// 提交结果：Job 快照 + async 标记（202 命中服务端等待上限退回异步）。
export interface SubmitJobResult {
  job: Job
  async: boolean
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
