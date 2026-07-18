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
  // 交互式 pty job（后端 omitempty）；详情页据此决定是否展示终端入口。
  interactive?: boolean
  // 仅 job 详情端点计算；list 端点无该字段。
  can_attach?: boolean
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
  // 底层 agent CLI 会话标识（session-capture，后端 omitempty）：claude 注入 / codex 捕获得到，
  // 用于 `gofer job resume`。详情页展示，便于人工续接定位。
  session_id?: string
  // 提交来源（provenance，后端 omitempty）：channel=cli/web/mcp/im（提交渠道），
  // client=来源主机名(CLI)/IP(web)。配合 caller_id 标识"谁/哪台/经哪渠道提交"。
  channel?: string
  client?: string
  caller_id?: string
  role?: string
  origin_agent?: string
  escalate_to?: string
  // plan 编排（plan-orchestration）：客户端可设的归组键；归入某 plan 时非空。
  // 详情页据此展示 plan 链接；session 续跑的新 job 自动继承源 job 的 plan_id。
  plan_id?: string
  // 血缘键（P5，本次追加）：本 job resume/rebuild 自哪个源 job
  source_job_id?: string
}

export interface PtySession {
  pty_session_id: string
  job_id?: string
  session_id?: string
  state: string
  cols: number
  rows: number
  bytes_in: number
  bytes_out: number
  encrypted: boolean
  started_at: number
  ended_at?: number
  has_recording: boolean
}

export interface PtySessionsResp {
  sessions: PtySession[]
}

export type AttachServerFrame =
  | { t: 'hello'; write: boolean; cols: number; rows: number }
  | { t: 'x'; code?: number }

export type AttachClientFrame =
  | { t: 'i'; d: string }
  | { t: 'r'; cols: number; rows: number }

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

// 只读总览统计（/v1/stats）。
export interface Stats {
  jobs: {
    total: number
    by_status: Record<string, number>
  }
  workflows: {
    running: number
    total: number
  }
  schedules: {
    total: number
    enabled: number
  }
  runners: {
    workers_connected: number
    workers_total: number
    peers_up: number
  }
  drivers: {
    online: number
    supervisors: number
  }
  escalations_pending: number
  projects: number
  server_time: number
  version?: string
  uptime_sec?: number
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

// 脱敏配置总览（GET /v1/config）。字段逐项对齐 internal/httpapi/config_handler.go
// 的 configView；secret 值均已在后端 bool 化为 *_set，env 仅暴露 key 名。
export interface ConfigView {
  server: ServerConfigView
  storage: StorageConfigView
  projects: ProjectDetail[]
  agents: ConfigAgentView[]
  runners: ConfigRunnerView[]
  roles: ConfigRoleView[]
  supervisor?: SupervisorView
  presence: ConfigPresenceView
  schedule: ConfigScheduleView
}

export interface ServerConfigView {
  addr: string
  path_view: string
  allow_empty_token: boolean
  web_enabled: boolean
  token_set: boolean
  governance: GovernanceView
  callers: CallerConfigView[]
  workers: WorkerConfigView[]
  runner_probe: RunnerProbeConfigView
  notification?: NotificationView
  metrics: MetricsConfigView
}

export interface GovernanceView {
  default_caller_max_concurrent: number
  default_rate_limit: number
  default_rate_burst: number
  require_answer_capability: boolean
  require_admin_capability: boolean
}

export interface CallerConfigView {
  id: string
  token_set: boolean
  can_answer: boolean
  can_admin: boolean
  max_concurrent_jobs?: number
  rate_limit?: number
  rate_burst?: number
}

export interface WorkerConfigView {
  id: string
  token_set: boolean
  labels: string[]
}

export interface RunnerProbeConfigView {
  interval_seconds: number
  timeout_seconds: number
}

export interface MetricsConfigView {
  enabled: boolean
  token_set: boolean
}

export interface NotificationView {
  webhooks: WebhookView[]
  allow_hosts: string[]
  allow_http: boolean
  max_attempts: number
}

export interface WebhookView {
  url: string
  events: string[]
  secret_set: boolean
  projects: string[]
}

export interface StorageConfigView {
  default_exchange_subdir: string
  default_result_subdir: string
  root: string
  db_path: string
  retention: RetentionView
  cast: CastView
}

export interface RetentionView {
  max_age_days: number
  max_count: number
  prune_interval_minutes: number
  workflow_max_age_days: number
}

export interface CastView {
  enabled: boolean
  retention_ttl_hours: number
  encryption_enabled: boolean
}

export interface ConfigAgentView {
  key: string
  type: string
  interactive: boolean
  command?: string
  args: string[]
  env_keys: string[]
  allow_raw_cmd: boolean
  detect: DetectConfigView
  session_inject: string[]
  session_capture?: string
  session_resume: string[]
  system_inject: string[]
  mcp_server_name?: string
}

export interface DetectConfigView {
  command?: string
  args: string[]
}

export interface ConfigRunnerView {
  key: string
  type: string
  base_url?: string
  token_set: boolean
  worker_id?: string
}

export interface ConfigRoleView {
  key: string
  agent: string
  system_prompt?: string
  project?: string
  tags: string[]
  env_keys: string[]
}

export interface SupervisorView {
  enabled: boolean
  interval_sec: number
  auto_answer: boolean
  escalate_to?: string
  max_rounds_per_job: number
  allow_prompt_regex: string[]
  owner_answer_timeout_sec: number
  desired_supervisors: number
  reconcile_runner?: string
  reconcile_interval_sec: number
  reconcile_prompt?: string
  reconcile_job_timeout_sec: number
}

export interface ConfigPresenceView {
  ttl_sec: number
  message_ttl_sec: number
  prune_interval_sec: number
}

export interface ConfigScheduleView {
  sweep_interval_sec: number
  miss_grace_sec: number
}

export interface ProjectWriteReq {
  key: string
  host_path: string
  container_path?: string
  default_agent?: string
  allowed_agents?: string[]
  allowed_runners?: string[]
  allow_exec: boolean
  max_concurrent_jobs?: number
}

export interface ProjectWriteResp extends ProjectDetail {
  warnings?: string[]
}

// 项目 git 状态（E20，design §6.3）：GET /v1/projects/{key}/git。
// 字段与 internal/project/browse.go 的 GitStatus / Commit JSON tag 对齐。
// is_git_repo=false 表示非 git 工作树 / 非本地可达 / git 缺失（其余字段为零值）。
export interface GitCommit {
  hash: string
  subject: string
  author: string
  // committer date，Unix 秒
  ts: number
}

export interface GitStatus {
  is_git_repo: boolean
  branch: string
  dirty: boolean
  // 始终非 null（后端保证 []）；最多 10 条
  recent_commits: GitCommit[]
}

// 子 git 仓发现项（E32，design §6.4）：GET /v1/projects/{key}/repos。
// rel_path 根仓为 "."，子仓为相对 ExecPath 的斜杠分隔路径。
export interface RepoInfo {
  rel_path: string
  branch: string
  dirty: boolean
}

export interface ReposResp {
  repos: RepoInfo[]
}

// 关键文件内容（E32，design §6.4）：GET /v1/projects/{key}/file?path=<rel>。
// 字段与 internal/project/browse.go 的 FileContent JSON tag 对齐。
// truncated=true 表示文件超过 256KB，content 为前缀切片。
export interface FileContent {
  name: string
  size: number
  content: string
  truncated: boolean
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

// Agent 在线状态（/v1/agents/presence）。
export interface Presence {
  agent_id: string
  name: string
  role?: string
  project_key?: string
  client?: string
  status: string
  last_seen_at: number
}

export interface PresenceResp {
  agents: Presence[]
}

// Agent 收件箱消息（/v1/agents/{id}/inbox）。
export interface InboxMessage {
  id: string
  from_agent: string
  to_spec?: string
  kind: string
  body?: string
  ref?: string
  created_at: number
}

export interface InboxResp {
  messages: InboxMessage[]
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
  // plan 归组过滤（?plan=<id>，后端 P1 已落）：只列该 plan 下的 job。
  plan?: string
  // session 会话链过滤（?session=<sid>，后端早已落：job/list.go:29-31,118 + job_handler.go:140，
  // 但前端此前从未透传）。T9.6 用它列出「同一 agent 会话下的全部 job」（resume 链）。
  session?: string
  // 血缘反查（P5，本次追加）：列出某 job 派生出的所有 job
  source_job?: string
  limit?: number
  offset?: number
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
  needs_human?: number
  answer?: string
  // Unix 秒
  created_at: number
  escalated_at?: number
  answered_at?: number
  answered_by?: string
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

// worker/runner 上报的 typed agent 能力（P4）。type: cli-agent|exec；
// interactive 省略即 false。前端级联据 type/interactive 收窄 agent 下拉。
export interface AgentBrief {
  key: string
  type?: string
  interactive?: boolean
}

// worker 连接明细。heartbeat_age_ms 由后端读取时即时计算。
export interface RunnerWorker {
  // Unix 毫秒
  last_heartbeat: number
  heartbeat_age_ms: number
  in_flight: number
  labels?: string[]
  projects?: string[]
  agents?: string[]
  // typed agent 能力（key/type/interactive）与节点信息（P4，可观测面板）。
  agent_caps?: AgentBrief[]
  os?: string
  arch?: string
  // worker 自报的机器 hostname（标识机器；remote_addr 经 NAT/网桥可能失真）
  hostname?: string
  // hub 侧看到的连接来源地址（观测辅助）
  remote_addr?: string
  gofer_version?: string
  // worker 进程启动时间（Unix 秒）
  started_at?: number
}

// 运行器能力摘要（projects + typed agents）：local 行由服务端配置合成、
// worker 行同源 .worker（P4）。前端据此对所选 runner 级联 project→agent。
export interface RunnerCapabilities {
  projects?: string[]
  agent_caps?: AgentBrief[]
}

export interface Runner {
  name: string
  type: RunnerType
  status: RunnerStatus
  // 能力摘要（local 合成 / worker 上报；peer-http 无）
  capabilities?: RunnerCapabilities
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
  // 与 allowed_agents 相互独立的两道闸（后端 job/config.go）：交互 job 的 agent 必须在
  // interactive_allowed_agents 内（为空=该 project 不支持交互）；exec 型 agent 在 LOCAL
  // runner 上还需 allow_exec（worker/peer 由执行侧自己把关）。
  //
  // ⚠️ 两者都是 optional：控制台从磁盘热更（--web-dir），二进制另走一条发布线，故新前端
  // 可能跑在旧 server 上。**undefined = 该 server 没有这个字段**（不是"闸为假"）——此时必须
  // 退回不收窄，否则会把 exec 全藏起来 / 交互下拉清空。后端对 allow_exec 去掉了 omitempty，
  // 就是为了让 false 与 undefined 可区分。
  interactive_allowed_agents?: string[]
  allow_exec?: boolean
  default_agent?: string
  // 联邦（follow-up）：仅在线 worker 上报、host 无配置的 project。仅在选定该 worker 后可选；
  // 本地/工作流等不能本地运行的消费方按此标记过滤掉。worker-only 项 allowlists 为空。
  worker_only?: boolean
}

export interface MetaAgent {
  key: string
  // cli-agent | exec（前端据此切换 prompt 文本域 / command 输入）
  type: string
  // 交互式 agent（P4）——级联据此过滤；省略即 false
  interactive?: boolean
}

export interface MetaRunner {
  name: string
  type: RunnerType
  // type=worker 时 config 里 pin 的目标 worker（提交时 worker_id 留空即回落到它）。
  // 级联据此在用户未手选 worker 时也能按该 worker 的能力收窄。
  worker_id?: string
}

export interface MetaWorker {
  id: string
  labels?: string[]
  projects?: string[]
  agents?: string[]
  // typed agent 能力（P4）：级联收窄目标 worker 的 agent 下拉
  agent_caps?: AgentBrief[]
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
  system_prompt?: string
  cmd?: string[]
  // per-job cli-agent flags（xu64.12 §14）：追加到 agent argv 末尾；仅 cli-agent 生效，
  // 后端拒绝 exec+agent_args。与 JobRequest.AgentArgs（model.go:16）对齐。
  agent_args?: string[]
  cwd?: string
  timeout_sec?: number
  title?: string
  worker_id?: string
  worker_labels?: string[]
  sync?: boolean
  interactive?: boolean
  cols?: number
  rows?: number
  record_pty?: boolean
  // E5：自由标签（逗号分隔输入解析为数组），支持详情/list 的 ?tag= 检索。
  tags?: string[]
  // 提交渠道（provenance）：web 控制台提交固定 "web"；client(来源 IP)由 server 盖章。
  channel?: string
}

// 提交结果：Job 快照 + async 标记（202 命中服务端等待上限退回异步）。
export interface SubmitJobResult {
  job: Job
  async: boolean
}

// 定时调度（AUTO-02）。GET/POST /v1/schedules，字段与 internal/httpapi/
// schedule_handler.go 的 scheduleView 对齐：enabled/catch_up 是 1/0 整型；
// next_run_at/last_run_at 为 Unix 秒（0 表示未排定/从未触发）；request 是被调度
// 的 JobRequest 快照（复用 SubmitJobReq 形状，触发时 channel 覆盖为 cron）。
export interface Schedule {
  id: string
  name: string
  type?: 'cron' | 'once'
  cron: string
  enabled: number
  catch_up: number
  next_run_at: number
  last_run_at: number
  last_job_id: string
  project_key: string
  request: SubmitJobReq
}

export interface SchedulesResp {
  schedules: Schedule[]
}

// POST /v1/schedules 载荷。enabled/catch_up 省略时后端默认 true（1）。
// request.caller_id 由服务端覆盖，前端不发。
export interface CreateScheduleReq {
  name: string
  type?: 'cron' | 'once'
  cron: string
  delay_sec?: number
  run_at?: number
  request: SubmitJobReq
  enabled?: boolean
  catch_up?: boolean
}

// 工作流（job 链）。状态取值是 JobStatus 的子集（running/done/failed/cancelled），
// 复用 StatusBadge 渲染。与 internal/httpapi/workflow_handler.go 的
// workflowSummary（列表/提交/取消，仅头部）/ workflowDetail（详情，头部+steps）对齐。
export type WorkflowStatus = 'running' | 'done' | 'failed' | 'cancelled'

// 工作流中的一步：1-based 序号 + 步骤名 + 对应 step-job 的 id/status。
// 尚未起的步骤无 job 行（链严格串行），故未到的步骤不在列表中。
// attempt/fan_index 是 v2 维度（P1 重试 / P2 并行）：一个 step 可有多行——
// 重试每 attempt 一行、fan-out 每 fan 一行；v1 单 job 步均为 0/省略。
export interface WorkflowStep {
  step_index: number
  // 后端 omitempty：1-based 重试 attempt（v1 单跑为 0/省略）
  attempt?: number
  // 后端 omitempty：1-based fan-out 并行序号（非 fan 为 0/省略）
  fan_index?: number
  // 后端 omitempty：步骤名（来自 spec name / 原始请求 title）
  name?: string
  // 后端 omitempty：该步 step-job id（未起为空；workflow 型步无 job）
  job_id?: string
  // 后端 omitempty：该 step-job 当前状态（workflow 型步取子工作流状态）
  status?: JobStatus
  // 后端 omitempty：步类型，"workflow" 表示子工作流嵌套步（P3 UI 修复）
  type?: string
  // 后端 omitempty：workflow 型步的子工作流 id（链入子 wf 详情）
  child_workflow_id?: string
}

// 工作流生命周期事件（P1 workflow_events）：seq 游标 + 类型 + 可选 detail_json + 时间。
// 类型如 workflow.submitted / step.started / step.fanout / step.retry / step.skipped /
// subworkflow.started / workflow.terminal / workflow.cancelled。
export interface WorkflowEvent {
  seq: number
  workflow_id: string
  type: string
  // 后端 omitempty：detail_json 原文（已是 JSON 字符串）
  detail?: string
  // Unix 秒
  at: number
}

export interface WorkflowEventsResp {
  events: WorkflowEvent[]
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

// plan 编排（plan-orchestration，design §5/§10）。归组容器：把陆续产生的独立 job
// 归到一个计划 + 跟进 todo。与 workflow（静态串行引擎）正交。字段严格对齐
// internal/httpapi/plan_handler.go 的 planView/todoView/planDetail 及 jobstore.PlanCounts。
export type PlanStatus = 'open' | 'active' | 'done' | 'archived'

// plan 下 jobs 的实时状态聚合（查询期算，detail 恒有；list 经 T10 内联）。
export interface PlanCounts {
  total: number
  queued: number
  running: number
  done: number
  failed: number
}

// plan 待办项（P3）。job_id 为空=纯待办；非空=绑某次 job 执行（元数据，done 纯手动）。
export interface Todo {
  todo_id: string
  plan_id: string
  // 后端 omitempty
  job_id?: string
  title: string
  done: boolean
  // 后端 omitempty
  sort?: number
  // Unix 秒
  created_at: number
  updated_at: number
}

// plan 头部（list/create/attach 返回）。counts 在 detail 恒有、list 经 T10 内联（故 optional）。
export interface Plan {
  plan_id: string
  // 后端 omitempty
  title?: string
  description?: string
  status: PlanStatus
  owner?: string
  progress?: number
  // Unix 秒
  created_at: number
  updated_at: number
  // list（T10 后）与 detail 均带；老 list 响应缺省时为 undefined，渲染做兜底
  counts?: PlanCounts
}

// plan 详情（GET /v1/plans/{id}）：头部 + counts + 其下 jobs + todos。
export interface PlanDetail extends Plan {
  counts: PlanCounts
  jobs: Job[]
  todos: Todo[]
}

export interface PlansResp {
  plans: Plan[]
}

export interface WorkflowRetryPolicy {
  max_attempts: number
  backoff_sec?: number[]
  on_exit_codes?: number[]
}

// POST /v1/workflows body. 字段名严格对齐 internal/job/workflow/types.go
// Spec / StepSpec 的 json tag；CLI-only file 字段是 json:"-"，Web 不发送。
export interface WorkflowSpec {
  title?: string
  steps: WorkflowStepSpec[]
}

export interface WorkflowStepSpec {
  name?: string
  project_key: string
  agent: string
  runner: string
  prompt?: string
  cmd?: string[]
  cwd?: string
  timeout_sec?: number
  tags?: string[]
  on_failure?: 'fail' | 'continue' | 'retry' | string
  retry?: WorkflowRetryPolicy
  fan_out?: number
  join?: 'all' | 'any' | 'quorum' | string
  type?: 'job' | 'workflow' | string
  sub_workflow?: WorkflowSpec
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

// GET /v1/jobs/{id}/request 的脱敏结果（rebuild 预填仅用于「显示」——env 值恒为占位、
// request_id/caller_id/session_id 已清）。字段对齐脱敏后的 job.JobRequest。
export interface RedactedRequest {
  project_key: string
  agent: string
  runner: string
  prompt?: string
  system_prompt?: string
  cmd?: string[]
  agent_args?: string[]
  cwd?: string
  timeout_sec?: number
  title?: string
  tags?: string[]
  env?: Record<string, string> // 值恒为 ***REDACTED*** 占位（明文不出服务端）
  env_files?: string[]
  plan_id?: string
  interactive?: boolean
  cols?: number
  rows?: number
  worker_id?: string
  worker_labels?: string[]
}

// POST /v1/jobs/{id}/rebuild 提交体：只发用户改动过的字段（未改字段服务端继承源真值）。
// env_set 新增/改值（值不出现在 GET，只在此处提交新值）；env_unset 删除 key。
export interface RebuildRequest {
  project_key?: string
  agent?: string
  runner?: string
  prompt?: string
  system_prompt?: string
  cmd?: string[]
  agent_args?: string[]
  cwd?: string
  title?: string
  tags?: string[]
  timeout_sec?: number
  interactive?: boolean
  cols?: number
  rows?: number
  worker_id?: string
  worker_labels?: string[]
  plan_id?: string
  channel?: string
  env_set?: Record<string, string>
  env_unset?: string[]
}

export type RebuildBody = RebuildRequest
