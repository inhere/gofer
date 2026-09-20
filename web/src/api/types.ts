// 后端 API 形状（snake_case，全部 /v1 前缀）。与 internal handler 对齐。

export type JobStatus =
  | 'queued'
  | 'running'
  | 'pending_interaction'
  | 'done'
  | 'failed'
  | 'cancelled'
  | 'timeout'
  // RECOV-01：worker 连接断开后 job 被 held（非终态），等同一 worker 进程在窗口内重连续接；
  // 窗口超时/换实例 → failed(worker_lost)。枚举尾部追加，与后端 job.StatusRecovering 对齐。
  | 'recovering'
  // GATE-01 S3：人工验收。needs_review = agent 正常完成但等人 accept/reject（非终态、
  // 但进程已结束：它和 pending_interaction 一样"等人"，只是等的不是 agent 继续跑）；
  // rejected = 人拒绝（终态），与 failed 一样算失败但不会被自动重试/续投。
  | 'needs_review'
  | 'rejected'

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
  // 只读 job（bd h-aii-0ql3，后端 omitempty）：cli-agent 走 read_only_args 沙箱参数、
  // acp-agent 走 session/set_mode；列表打 [只读] 徽章、详情展示一行。
  read_only?: boolean
  // 人工验收（GATE-01 S3，后端 omitempty）：require_review=该 job 要人验收（正常完成
  // 落在 needs_review）；reviewed_by/at/note=已经做出的裁决（谁/何时/为什么）。needs_review
  // 时后者为空，正说明还没人裁。
  require_review?: boolean
  reviewed_by?: string
  reviewed_at?: number
  review_note?: string
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
  // RECOV-01：进入 recovering 的 unix 秒（后端 omitempty，0/缺省=不在 recovering）。详情页据此
  // 展示 job 在等 worker 重连了多久。
  recovering_since?: number
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
  // WT-01 受管 worktree（后端 omitempty）：--worktree job 在独立 worktree 里执行，
  // 交付物是该分支上的提交。详情页展示路径/分支/基线/领先提交数；rm/ls 走
  // `gofer job worktree` 与 /v1/jobs/{id}/worktree。
  worktree_path?: string
  worktree_branch?: string
  worktree_base_sha?: string
  worktree_head_sha?: string
  commits_ahead?: number
  // plan todo 联动 + 提交采集（SUP-01 C，后端 omitempty）：todo_id=该 job 挂接的
  // checklist 项；base_sha=开跑时的 HEAD；commits=它产出的提交（新→旧，git log
  // --oneline 的 sha+subject）。详情页「提交」块与 plan 视图据此渲染。
  todo_id?: string
  base_sha?: string
  commits?: JobCommit[]
  // 验证步骤（SUP-01 P2，后端 omitempty）：agent 正常结束后在执行机同 cwd/env 跑的验收命令
  // 结果。failed/timeout 正是该 job 失败的原因；skipped=agent 没正常结束，故没跑。
  // 列表页对失败的 job 打 verify 小徽标，详情页有独立块。
  verify?: JobVerify
  // 用量/成本（SUP-01 E，后端 omitempty）：agent 自报的 token/成本结算（远端 job 由执行机
  // 采集后随 Outcome 回传）。没采集到就没有该字段，详情页不显示用量块。
  usage?: JobUsage
}

// 一次验证步骤的结果（SUP-01 P2）。command=提交时的 argv（未经 shell），
// status=passed|failed|timeout|skipped，duration_ms=实际耗时，reason=跳过/超时的原因。
export interface JobVerify {
  command?: string[]
  status: 'passed' | 'failed' | 'timeout' | 'skipped'
  exit_code: number
  duration_ms: number
  reason?: string
}

// 一次运行的 token/成本结算（SUP-01 E）。缺省的计数器 = agent 没报这一项（不是 0），
// 渲染时省略；后端在总数缺失时按四项求和。source 标明这串数字从哪来：
// ndjson:omp / ndjson:claude / codex:stderr / acp:usage_update。
export interface JobUsage {
  input_tokens?: number
  output_tokens?: number
  cache_read_tokens?: number
  cache_write_tokens?: number
  total_tokens?: number
  cost_usd?: number
  source?: string
}

// 该 job 产出的一个提交（SUP-01 C）。
export interface JobCommit {
  sha: string
  subject: string
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

// 会话中继（SESS-01）：hook 登记的 agent 会话（claude/codex 等），与 pty 会话无关。
// state：running 执行中 / idle 已停且未开中继 / waiting_reply 开中继且等人回复 /
// needs_attention 需人工注意（如 hook 报错）/ handed_off 已被 web 用 --resume 起的新进程
// 接管（原终端不再中继，见 §9.1 B）/ ended 已结束。
export type AgentSessionState =
  | 'running'
  | 'idle'
  | 'waiting_reply'
  | 'needs_attention'
  | 'handed_off'
  | 'ended'

// AgentSessionRelayMode 是会话中继开关的三态（R1）。
export type AgentSessionRelayMode = 'auto' | 'on' | 'off'

export interface AgentSession {
  session_id: string
  agent: string
  project_key?: string
  runner?: string
  cwd?: string
  title?: string
  // transcript 只存路径，不读内容
  transcript?: string
  tmux_pane?: string
  state: AgentSessionState
  // 中继开关（R1，三态）：on 每次停下都等 web 回复；off 从不等；auto 由 server 按
  // 键盘空闲 / 距上次人工输入的时长判定（session.auto_relay_idle_sec /
  // auto_relay_turn_sec）。新会话默认 auto。
  relay_mode: AgentSessionRelayMode
  // relay 是 server 派生值：本次 Stop 会不会等（on，或 auto 的判据成立）。旧客户端读它。
  relay: boolean
  // wait_reason 是当前判定依据：mode_on（显式开关）/ idle_probe（键盘空闲）/
  // turn_age（探测不到键盘，距上次人工输入够久）；空 = 不等。
  wait_reason?: 'mode_on' | 'idle_probe' | 'turn_age' | ''
  // wait_reason_detail 解释【当前不等】的原因（SUP-01 D）：caller 还有在跑的 job 时
  // 不自动布防，例如 "supervising 2 jobs"。wait_reason 非空时必为空。
  wait_reason_detail?: string
  // caller_id 是注册该会话的认证 caller（会话的 owner）：谁能作答，以及按谁的在跑
  // job 判定"监督中不布防"。空 = 老会话（无 owner）或未配置 token。
  caller_id?: string
  // 空闲自动布防（SR-A5）：仅键盘空闲判据成立（wait_reason = idle_probe），
  // idle_sec 是 hook 最近上报的系统输入空闲秒数，-1 = 未知。
  auto_armed: boolean
  idle_sec: number
  // 最近一次【人】在这个会话里输入的时间（unix 秒，0 = 还没见过人）：容器里探测不到
  // 键盘时，server 用它算“安静了多久”（turn_age 判据）。
  last_human_at?: number
  turn_no: number
  last_message?: string
  last_event?: string
  // Unix 秒
  last_seen_at: number
  started_at: number
  ended_at?: number
  // 接管（§9.1 B）：会话被 web 用 `--resume` 起的新 pty job 接管时，指向那个 job
  // （web 跳到 /jobs/<id>?attach=1 继续对话）；解除接管后清空。
  handed_off_job_id?: string
  handed_off_at?: number
  // notice 是给【原终端】的一行提示（hook 打到 stderr）：会话已被 web 接管、本终端
  // 中继已停用。仅 handed_off 时非空。
  notice?: string
}

export interface AgentSessionsResp {
  sessions: AgentSession[]
}

// GET /v1/sessions/{sid}?turns=N：turns 最新在前；每条 turn 是一个 Decision
// （question = agent 停下时最后一条消息，answer = 人的回复）。
export interface SessionDetailResp {
  session: AgentSession
  turns: Decision[]
}

// POST /v1/sessions/{sid}/deliver（设计 §9.1 选路）：回复去了哪里。
// path='turn' = 答了当前 OPEN turn（decision_id 是该 turn）；path='tmux' = 被内部
// job 敲进终端（job_id 是那个 job，decision_id 是审计行）；path='takeover' = 会话没有
// 可用的 tmux pane，已用 `--resume` 起了新进程接管（§9.1 B，job_id 是那个交互 job，
// web 跳到 /jobs/<id>?attach=1）。
export interface SessionDeliverResult {
  path: 'turn' | 'tmux' | 'takeover'
  job_id?: string
  decision_id?: string
}

export type AttachServerFrame =
  | { t: 'hello'; write: boolean; cols: number; rows: number }
  // pty 尺寸变更广播（tools-3xy）：写者 resize 后 serve 推给所有 viewer，客户端跟随。
  | { t: 'r'; cols: number; rows: number }
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
  // Server DB：元数据库文件/页几何 + 各表行数（表名以实际 schema 为准，缺失的表不出现）。
  db: {
    path: string
    size_bytes: number
    wal_size_bytes: number
    page_size: number
    page_count: number
    tables: Record<string, number>
    // partial=true = 行数查询超预算被截断（已得部分照常返回，文件/页字段始终完整）。
    partial: boolean
  }
  // Sessions：agent 会话数（按状态/中继三态分布）、待回复的 relay turn 数、近 1h 活跃数。
  sessions: {
    total: number
    by_state: Partial<Record<AgentSessionState, number>>
    by_relay_mode: Record<AgentSessionRelayMode, number>
    waiting_turns: number
    seen_within_1h: number
  }
  // 用量/成本（SUP-01 E）：按 24h/7d 窗口给出各 agent 的 job 数、token 与成本。
  // windows 里缺某个窗口 = 该窗口没算（partial=true 表示预算耗尽），不能当 0 用量读。
  usage: {
    windows: Record<string, UsageWindow>
    partial: boolean
  }
  escalations_pending: number
  projects: number
  server_time: number
  version?: string
  uptime_sec?: number
}

// 一个用量窗口（SUP-01 E）：by_agent 按 agent 键给出各自结算，total 是它们的和。
// by_agent 里没有某个 agent = 该窗口内它没有结算（跑了但没采集到），不是 0 用量。
export interface UsageWindow {
  by_agent: Record<string, UsageAgent>
  total: UsageAgent
}

// 一个 agent 在一个窗口内的用量：jobs 计它在窗口内跑过的每个 job，token/成本只累加
// 真报了数的那些。
export interface UsageAgent {
  jobs: number
  total_tokens: number
  input_tokens: number
  output_tokens: number
  cost_usd: number
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
  // 项目级交互 job 总开关（AGT-02 §2）：false = 交互 job 一律不放行，且这是项目侧**唯一**的交互
  // 闸（0.3 已彻底移除按 agent 收窄的历史名单：想只放行一部分 agent，就定义一个不带 interactive_args
  // 的 agent 变体）。后端始终下发；旧 yaml 里非空的历史列表在后端加载期被折算成 true（有效值），故这里
  // 读到的就是"能不能跑交互 job"的最终答案。⚠️ 控制台从磁盘热更（--web-dir）而二进制另走发布线，运行时
  // 仍可能是旧 server 不返回该字段 → undefined 视为"没有这个字段"（不是"闸为假"），消费方按 fail-safe
  // 放行、交给后端拒。
  allow_interactive: boolean
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
  // 以下字段全部可选 = **不发送即保持不变**（PUT 是合并语义，AGT-02 §2）：字段缺失不动，字段存在
  // （哪怕 false / [] / ""）即覆盖。故控制台保存时必须整体下发整份表单（含空数组）——省略空数组
  // 会让"取消最后一个勾选"发不出 []，旧值被静默保留（老 bug h-aii-3scy 的另一面）。
  container_path?: string
  default_agent?: string
  allowed_agents?: string[]
  allowed_runners?: string[]
  allow_exec: boolean
  allow_interactive?: boolean
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

// Agent 近期健康度（SUP-01 P3，/v1/agents 的 health）：状态 + 判定依据。
// unknown = 窗口内没有样本（不是"健康"）；degraded = 窗口内供应商错误已达
// 阈值且尚无足够成功恢复。
export interface AgentHealth {
  state: 'healthy' | 'degraded' | 'unknown' | string
  window_sec: number
  jobs: number
  ok: number
  transient_fail: number
  last_transient_at?: number
  last_ok_at?: number
}

export interface AgentInfo {
  key: string
  type: string
  available: boolean
  version?: string
  error?: string
  health?: AgentHealth
  // 运行时由内置模板注入（config.yaml 里没有写，但本机 PATH 上有它的 CLI）。
  // 展示用：让 Agents 页能说明"这个 agent 从哪来的"；不参与任何准入判断。
  injected?: boolean
}

// 探针结果（POST /v1/agents/{key}/probe）：承载它的普通 job 及其结果。
export interface AgentProbe {
  job_id: string
  status: string
  exit_code: number
  duration_ms: number
  first_line?: string
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

// 运行中交互：question 文本问答 / choice 选项 / confirmation 确认 /
// permission 审批门（GATE-01：acp-agent 的 session/request_permission 求批，
// 作答值取 options[].value = ACP optionId）。
// 'expired' 目前只由 decision 投影产生（EXPIRED 决策，只读），job interaction 不会返回。
export type InteractionType = 'question' | 'choice' | 'confirmation' | 'permission'
export type InteractionStatus = 'pending' | 'answered' | 'cancelled' | 'expired'

export interface InteractionOption {
  value: string
  label?: string
  // permission 专用：ACP optionId（= value）与该选项的 ACP kind
  // （allow_once|allow_always|reject_once|reject_always），用于分组/徽标。
  id?: string
  kind?: string
}

// permission 交互的工具调用详情（GATE-01 §1）
export interface InteractionToolCall {
  id: string
  title?: string
  kind?: string
  locations?: string[]
  raw_input_summary?: string
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
  // permission 交互：被求批的工具调用、策略提示（"ask: kind=edit"）与作答截止时间
  // （unix 秒，0 = 无倒计时）。
  tool_call?: InteractionToolCall
  policy_hint?: string
  expires_at?: number
}

// plan 决策通道（decision channel，T4）：agent 经 MCP gofer_ask_human 提问，
// 人在 web 作答。字段对齐 internal/httpapi/decision_handler.go 的 decisionView；
// 时间为 Unix 秒。options 为空 = 自由文本作答。
export type DecisionState = 'OPEN' | 'ANSWERED' | 'EXPIRED'

export interface Decision {
  id: string
  // 后端 omitempty（可空=全局提问）
  plan_id?: string
  title: string
  question: string
  // 后端 omitempty
  options?: string[]
  answer?: string
  state: DecisionState
  timeout_sec: number
  // Unix 秒
  asked_at: number
  answered_at?: number
  answered_by?: string
  // 会话中继（SESS-01）：kind=relay 的 decision 是某个 agent 会话的一个 turn，
  // session_id 指向该会话；无 options、自由文本作答。
  session_id?: string
  kind?: 'relay' | string
  // 未作答关闭的原因（SR-A5）：'user_returned' = 人回到键盘，自动布防的等待被放行
  released_by?: string
  // 非 turn 投递的审计 JSON（§9.1 A）：'{"path":"tmux","job_id":"…"}' —— 这条消息
  // 没有等待中的 turn，是被直接敲进终端的。真 turn 为空。
  detail?: string
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
  | 'job.tool_call'
  | 'interaction.created'
  | 'interaction.answered'
  // 审批门（GATE-01 S1）：acp-agent 的一次 session/request_permission 被求批 /
  // 被作答（含自动放行与超时兜底）/ 超时兜底
  | 'job.permission_requested'
  | 'job.permission_answered'
  | 'job.permission_timed_out'

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
  batch?: boolean
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
  // worker 注册时上报的协议版本；过旧(< reload/policy 最低要求)时 reload/policy 会 409
  protocol_version?: number
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
  // 交互 job 的放行只看项目级总开关，与 allowed_agents 相互独立（后端 job/config.go）：
  //  - allow_interactive（AGT-02 §2）：项目级总开关，false = 交互 job 一律不放行。0.3 起它是项目侧
  //    唯一的交互闸——按 agent 收窄的历史名单已彻底移除，也不再下发。
  //  - allow_exec：exec 型 agent 在 LOCAL runner 上还需它为真（worker/peer 由执行侧自己把关）。
  //
  // ⚠️ 两者都是 optional：控制台从磁盘热更（--web-dir），二进制另走一条发布线，故新前端
  // 可能跑在旧 server 上。**undefined = 该 server 没有这个字段**（不是"闸为假"）——此时必须
  // 按 fail-safe 放行（交给后端拒），否则会把 exec 全藏起来 / 交互下拉清空。后端对 allow_exec
  // 去掉了 omitempty，就是为了让 false 与 undefined 可区分。
  allow_interactive?: boolean
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
  batch?: boolean
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
  // 只读提交（bd h-aii-0ql3）：agent 不能写文件；需 agent 配了 read_only_args（cli-agent）
  // 或 acp.modes.read_only（acp-agent），否则后端 400。
  read_only?: boolean
  cols?: number
  rows?: number
  record_pty?: boolean
  // E5：自由标签（逗号分隔输入解析为数组），支持详情/list 的 ?tag= 检索。
  tags?: string[]
  // 提交渠道（provenance）：web 控制台提交固定 "web"；client(来源 IP)由 server 盖章。
  channel?: string
  // 任务书模板（SUP-01 P5）：服务端把 <name>.md 渲染成 prompt，vars 供 {{变量}}；
  // template 与 vars 随 request_json 存档（audit/rerun）。清单见 listTemplates()。
  template?: string
  vars?: Record<string, string>
}

// 任务书模板（SUP-01 P5，GET /v1/projects/{key}/templates）。
// vars 是模板声明的变量表；source 为 project|global（同名时项目副本遮蔽全局副本）；
// error 非空表示该文件存在但解析失败（列表如实报出，不隐藏）。
export interface TemplateVar {
  default?: string
  required?: boolean
  desc?: string
}

export interface TemplateInfo {
  name: string
  source: string
  path: string
  desc?: string
  vars?: Record<string, TemplateVar>
  error?: string
}

export interface TemplatesResp {
  templates: TemplateInfo[]
}

// 模板详情 + 服务端渲染预览（GET /v1/projects/{key}/templates/{name}?var=k=v）。
// 渲染在服务端做：只有它能展开 {{include: …}}、并按将要执行的那个项目解析 {{head}}，
// 所以这里的 prompt 就是提交时真正会发出去的正文。缺必填变量在 missing 里如实列出。
export interface TemplateRender {
  prompt: string
  missing?: string[]
  warnings?: string[]
}

export interface TemplatePreview extends TemplateInfo {
  body?: string
  render: TemplateRender
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
// A plan's todos by lifecycle status (server roll-up).
export interface PlanTodoCounts {
  total: number
  pending: number
  doing: number
  done: number
  skipped: number
}

// A plan's single progress figure: todos when it has any (done + skipped count as
// complete), else its jobs; basis 'none' + percent null = nothing to measure.
// Absent (undefined) on older servers — treat that as "not supported", not as 0.
export interface PlanCompletion {
  basis: 'todos' | 'jobs' | 'none'
  done: number
  total: number
  percent: number | null
}

// plan 待办项（P3）。job_id 为空=纯待办；非空=绑某次 job 执行（元数据，done 纯手动）。
// todo 生命周期状态（Part C §C2）：doing 自动记 started_at，done/skipped 记 done_at。
export type TodoStatus = 'pending' | 'doing' | 'done' | 'skipped'

export interface Todo {
  todo_id: string
  plan_id: string
  // 后端 omitempty
  job_id?: string
  title: string
  done: boolean
  status: TodoStatus
  // 挂接在该 todo 上的 job（SUP-01 C，服务端最多 10 条，新→旧）：plan 详情在待办行下
  // 显示数量与最近一次 job 的链接。老服务端不发时为 undefined。
  jobs?: TodoJob[]
  // Unix 秒；后端 omitempty（未开始/未完结为缺省）
  started_at?: number
  done_at?: number
  note?: string
  // 后端 omitempty
  sort?: number
  // Unix 秒
  created_at: number
  updated_at: number
}

// 挂在某个 todo 上的一次 job 执行（SUP-01 C）。
export interface TodoJob {
  id: string
  status: string
  // 后端 omitempty
  agent?: string
  started_at?: number
  ended_at?: number
  duration_sec?: number
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
  todo_counts?: PlanTodoCounts
  completion?: PlanCompletion
}

// plan 详情（GET /v1/plans/{id}）：头部 + counts + 其下 jobs + todos + decisions。
export interface PlanDetail extends Plan {
  counts: PlanCounts
  jobs: Job[]
  todos: Todo[]
  // 决策通道（T4）：该 plan 下的全部 decision（含 OPEN/ANSWERED/EXPIRED）
  decisions?: Decision[]
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
  read_only?: boolean
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
  read_only?: boolean
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
