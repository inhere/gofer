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
  // runner=worker 时实际执行该 job 的 worker（后端 omitempty；显式或按 labels 选中）
  worker_id?: string
  // Unix 秒（后端 int64）
  started_at: number
  ended_at?: number
  error?: string
  // 产出与审计（job-outcomes-audit）：均为 JSON 字符串，前端 JSON.parse。
  // 渲染命令 {command,args,env_keys}（E15，后端 omitempty）。
  rendered_command?: string
  // 结构化结果，<result_dir>/result.json 原文（E6，后端 omitempty）。
  result_json?: string
  // git diff --stat 截断摘要（E12，P3 起填充；纯文本，非 JSON）。
  diff_summary?: string
  // 执行来源（P4，后端 omitempty）：""(本机) / "worker:<id>" / "peer:<name>"。
  // 远端执行时填充，产出面板据此标注「在 worker/peer 执行」，远端产物文件留执行机。
  source?: string
  // 自由标签（E5，后端 omitempty）：来自提交时的 tags，支持 ?tag= 检索，行内渲染徽标。
  tags?: string[]
}

// 产物清单项（E1，P2）：<result_dir>/artifacts/ 下文件元数据。name 为相对路径
// （可含子目录，'/' 分隔），下载经 GET /v1/jobs/{id}/artifacts/{name}。
export interface Artifact {
  name: string
  size: number
  mtime: number
}

export interface ArtifactsResp {
  artifacts: Artifact[]
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
  // E5 检索维度：tag（标签精确元素）/ agent / runner / since（unix 秒）/ caller。
  tag?: string
  agent?: string
  runner?: string
  since?: number
  caller?: string
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

// job 生命周期事件（E13，append-only）。GET /v1/jobs/{id}/events 与 SSE event 帧。
// detail 是后端 detail_json 原文（JSON 字符串，可空）；前端按需 JSON.parse。
// seq 单调递增，用于去重/排序（SSE 增量与初始拉取合并）。
export type JobEventType =
  | 'job.submitted'
  | 'job.dispatched'
  | 'job.running'
  | 'job.terminal'
  | 'job.cancelled'
  | 'interaction.created'
  | 'interaction.answered'

export interface JobEvent {
  seq: number
  type: JobEventType | string
  detail?: string
  // Unix 秒
  at: number
}

export interface JobEventsResp {
  events: JobEvent[]
}

// webhook 投递（E14）。GET /v1/jobs/{id}/deliveries。字段与 jobstore.Delivery 对齐。
// status：pending（待投/重试中）/ delivered（成功）/ failed（超上限放弃）。
// attempts 已尝试次数；next_retry_at 下次到期投递时间（unix 秒，已 delivered/failed 时不再用）。
// last_error 最近一次失败原因（不含密钥）；event_seq 关联 job_events.seq。
export type DeliveryStatus = 'pending' | 'delivered' | 'failed'

export interface Delivery {
  id: number
  event_seq: number
  job_id: string
  target: string
  status: DeliveryStatus | string
  attempts: number
  next_retry_at: number
  last_error?: string
  created_at: number
  updated_at: number
}

export interface DeliveriesResp {
  deliveries: Delivery[]
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
  // E5：自由标签（逗号分隔输入解析为数组），支持详情/list 的 ?tag= 检索。
  tags?: string[]
}

// 提交结果：Job 快照 + async 标记（202 命中服务端等待上限退回异步）。
export interface SubmitJobResult {
  job: Job
  async: boolean
}

// 工作流（job 链）。状态取值是 JobStatus 的子集（running/done/failed/cancelled），
// 复用 StatusBadge 渲染。与 internal/httpapi/workflow_handler.go 的
// workflowSummary（列表/提交/取消，仅头部）/ workflowDetail（详情，头部+steps）对齐。
export type WorkflowStatus = 'running' | 'done' | 'failed' | 'cancelled'

// 工作流中的一步：1-based 序号 + 步骤名 + 对应 step-job 的 id/status。
// 尚未起的步骤无 job 行（链严格串行），故未到的步骤不在列表中。
export interface WorkflowStep {
  step_index: number
  // 后端 omitempty：步骤名（来自 spec name / 原始请求 title）
  name?: string
  // 后端 omitempty：该步 step-job id（未起为空）
  job_id?: string
  // 后端 omitempty：该 step-job 当前状态
  status?: JobStatus
}

// 工作流头部 + 步骤链。列表/提交/取消仅返回头部（steps 省略）；详情内联 steps。
export interface Workflow {
  id: string
  // 后端 omitempty
  title?: string
  status: WorkflowStatus
  // 1-based 当前活跃步
  current_step: number
  total_steps: number
  // 后端 omitempty
  caller_id?: string
  // 后端 omitempty：status=failed 时的失败原因
  error?: string
  // Unix 秒
  created_at: number
  updated_at: number
  // 仅详情（getWorkflow）内联；列表为空
  steps?: WorkflowStep[]
}

export interface WorkflowsResp {
  workflows: Workflow[]
}

// SSE 事件（解析后）
// log-rotated：后端日志文件发生轮转（offset 重置），前端需清空该 stream 已缓冲文本后续读。
export type SSEEventType =
  | 'status'
  | 'log'
  | 'log-rotated'
  | 'interaction'
  | 'event'
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

// event 帧载荷（E13）：与 JobEvent 同形（seq/type/detail/at）。
export type SSEJobEventData = JobEvent

export interface SSEEvent {
  type: SSEEventType
  // status -> Job; log -> SSELogData; interaction -> SSEInteractionData;
  // event -> SSEJobEventData; end -> {}
  data: unknown
}
