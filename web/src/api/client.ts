// Typed API client。统一注入 Authorization；401 -> clearToken + 触发 unauthorized 回调并 throw。
// 非 2xx 解析 {error, detail} 抛出可读错误；text 端点返回字符串。

import { getToken, triggerUnauthorized } from '../store/auth'
import { streamJob } from './sse'
import type {
  AgentsResp,
  ArtifactsResp,
  ConfigView,
  CreateScheduleReq,
  DeliveriesResp,
  FileContent,
  GitStatus,
  InboxResp,
  Interaction,
  Job,
  JobEventsResp,
  JobsResp,
  JobStatus,
  ListJobsOpts,
  LogStream,
  MetaResp,
  PresenceResp,
  ProjectDetail,
  ProjectWriteReq,
  ProjectWriteResp,
  ProjectsResp,
  PtySessionsResp,
  ReposResp,
  RunnersResp,
  Schedule,
  SchedulesResp,
  Stats,
  SubmitJobReq,
  SubmitJobResult,
  Workflow,
  WorkflowEventsResp,
  WorkflowSpec,
  WorkflowsResp,
  WorkflowStatus,
} from './types'
import type { StreamJobOpts } from './sse'

interface ErrorBody {
  error?: string
  detail?: string
}

export class ApiError extends Error {
  status: number
  detail?: string
  code?: string

  constructor(status: number, message: string, detail?: string, code?: string) {
    super(message)
    this.name = 'ApiError'
    this.status = status
    this.detail = detail
    this.code = code
  }
}

function authHeaders(extra?: Record<string, string>): Record<string, string> {
  const headers: Record<string, string> = { ...extra }
  const token = getToken()
  if (token) {
    headers['Authorization'] = `Bearer ${token}`
  }
  return headers
}

async function raiseForStatus(res: Response): Promise<never> {
  let msg = `请求失败：HTTP ${res.status}`
  let detail = ''
  let code = ''
  try {
    const body = (await res.json()) as ErrorBody
    detail = body.detail ?? ''
    code = body.error ?? ''
    if (body.error || body.detail) {
      msg = [body.error, body.detail].filter(Boolean).join(' - ')
    }
  } catch {
    // 非 JSON 错误体，沿用默认
  }
  throw new ApiError(res.status, msg, detail || undefined, code || undefined)
}

async function request<T>(path: string, init?: RequestInit): Promise<T> {
  const res = await fetch(path, {
    ...init,
    headers: authHeaders(init?.headers as Record<string, string> | undefined),
  })
  if (res.status === 401) {
    triggerUnauthorized()
    throw new Error('未授权（401）：token 无效或已失效')
  }
  if (!res.ok) {
    return raiseForStatus(res)
  }
  return (await res.json()) as T
}

async function requestText(path: string, init?: RequestInit): Promise<string> {
  const res = await fetch(path, {
    ...init,
    headers: authHeaders(init?.headers as Record<string, string> | undefined),
  })
  if (res.status === 401) {
    triggerUnauthorized()
    throw new Error('未授权（401）：token 无效或已失效')
  }
  if (!res.ok) {
    return raiseForStatus(res)
  }
  return res.text()
}

export function listProjects(): Promise<ProjectsResp> {
  return request<ProjectsResp>('/v1/projects')
}

export function getProject(key: string): Promise<ProjectDetail> {
  return request<ProjectDetail>(`/v1/projects/${encodeURIComponent(key)}`)
}

export function getConfig(): Promise<ConfigView> {
  return request<ConfigView>('/v1/config')
}

export function createProject(req: ProjectWriteReq): Promise<ProjectWriteResp> {
  return request<ProjectWriteResp>('/v1/projects', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(req),
  })
}

export function updateProject(
  key: string,
  req: ProjectWriteReq,
): Promise<ProjectWriteResp> {
  return request<ProjectWriteResp>(`/v1/projects/${encodeURIComponent(key)}`, {
    method: 'PUT',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(req),
  })
}

export function deleteProject(key: string): Promise<{ status: string }> {
  return request<{ status: string }>(`/v1/projects/${encodeURIComponent(key)}`, {
    method: 'DELETE',
  })
}

// 项目当前 git 状态（E20，design §6.3）：GET /v1/projects/{key}/git。
// 非 git 仓 / 非本地可达 → is_git_repo:false（非错误）；详情页据此优雅降级。
export function getProjectGit(key: string): Promise<GitStatus> {
  return request<GitStatus>(`/v1/projects/${encodeURIComponent(key)}/git`)
}

// 子 git 仓发现（E32，design §6.4）：GET /v1/projects/{key}/repos。
// 含根仓（rel_path "."）；按深度/排除/上限扫描，返回 {repos:[...]}。
export function listRepos(key: string): Promise<ReposResp> {
  return request<ReposResp>(`/v1/projects/${encodeURIComponent(key)}/repos`)
}

// 关键文件读取（E32，design §6.4）：GET /v1/projects/{key}/file?path=<rel>。
// 仅白名单 basename 可读；非白名单→403、穿越/缺失→404、二进制→415、缺 path→400
// （均经 request 转可读错误抛出，调用方按需区分）。
export function getProjectFile(key: string, path: string): Promise<FileContent> {
  const qs = `?path=${encodeURIComponent(path)}`
  return request<FileContent>(
    `/v1/projects/${encodeURIComponent(key)}/file${qs}`,
  )
}

export function listAgents(): Promise<AgentsResp> {
  return request<AgentsResp>('/v1/agents')
}

export function getStats(): Promise<Stats> {
  return request<Stats>('/v1/stats')
}

export function listPresence(
  role?: string,
  project?: string,
): Promise<PresenceResp> {
  const params = new URLSearchParams()
  if (role) {
    params.set('role', role)
  }
  if (project) {
    params.set('project', project)
  }
  const qs = params.toString()
  return request<PresenceResp>(`/v1/agents/presence${qs ? `?${qs}` : ''}`)
}

export function listInbox(
  id: string,
  includeRead = true,
): Promise<InboxResp> {
  const qs = includeRead ? '?include_read=1' : ''
  return request<InboxResp>(`/v1/agents/${encodeURIComponent(id)}/inbox${qs}`)
}

// 运行器舰队状态（worker / peer-http / local）。Runners 视图轮询读取。
export function listRunners(): Promise<RunnersResp> {
  return request<RunnersResp>('/v1/runners')
}

// 提交表单选项聚合（G4，design §6.4）：projects/agents/runners/workers 一次取齐。
export function getMeta(): Promise<MetaResp> {
  return request<MetaResp>('/v1/meta')
}

// 提交 job（POST /v1/jobs）。复用 request 的 token/401/错误体处理，但因需读
// 响应状态码判定 202（X-Gofer-Async：同步等待超服务端上限退回异步），这里独立
// 走 fetch 以拿到 Response.status。200=终态/queued，202=仍在后台。
export async function submitJob(req: SubmitJobReq): Promise<SubmitJobResult> {
  const res = await fetch('/v1/jobs', {
    method: 'POST',
    headers: authHeaders({ 'Content-Type': 'application/json' }),
    body: JSON.stringify(req),
  })
  if (res.status === 401) {
    triggerUnauthorized()
    throw new Error('未授权（401）：token 无效或已失效')
  }
  if (!res.ok) {
    return raiseForStatus(res)
  }
  const job = (await res.json()) as Job
  return { job, async: res.status === 202 }
}

export function listJobs(opts?: ListJobsOpts): Promise<JobsResp> {
  const params = new URLSearchParams()
  if (opts?.status) {
    params.set('status', opts.status)
  }
  if (opts?.project) {
    params.set('project', opts.project)
  }
  if (opts?.tag) {
    params.set('tag', opts.tag)
  }
  if (opts?.agent) {
    params.set('agent', opts.agent)
  }
  if (opts?.runner) {
    params.set('runner', opts.runner)
  }
  if (opts?.since != null) {
    params.set('since', String(opts.since))
  }
  if (opts?.caller) {
    params.set('caller', opts.caller)
  }
  if (opts?.limit != null) {
    params.set('limit', String(opts.limit))
  }
  if (opts?.offset != null) {
    params.set('offset', String(opts.offset))
  }
  const qs = params.toString()
  return request<JobsResp>(`/v1/jobs${qs ? `?${qs}` : ''}`)
}

export function getJob(id: string): Promise<Job> {
  return request<Job>(`/v1/jobs/${encodeURIComponent(id)}`)
}

export function requestAttachTicket(
  id: string,
  mode: 'write' | 'read',
): Promise<{ ticket: string; expires_in: number }> {
  const qs = `?mode=${encodeURIComponent(mode)}`
  return request<{ ticket: string; expires_in: number }>(
    `/v1/jobs/${encodeURIComponent(id)}/attach-ticket${qs}`,
    { method: 'POST' },
  )
}

export function listPtySessions(id: string): Promise<PtySessionsResp> {
  return request<PtySessionsResp>(
    `/v1/jobs/${encodeURIComponent(id)}/pty/sessions`,
  )
}

export function logsTail(id: string, stream: LogStream): Promise<string> {
  return requestText(
    `/v1/jobs/${encodeURIComponent(id)}/logs/${stream}?bytes=262144`,
  )
}

export interface JobLogPage {
  text: string
  total: number
  offset: number
  lines: number
}

export async function fetchJobLog(
  id: string,
  stream: LogStream,
  opts?: { lines?: number; offset?: number; full?: boolean },
): Promise<JobLogPage> {
  const params = new URLSearchParams()
  if (opts?.full) {
    params.set('full', '1')
  } else {
    if (opts?.lines != null) {
      params.set('lines', String(opts.lines))
    }
    if (opts?.offset != null) {
      params.set('offset', String(opts.offset))
    }
  }
  const qs = params.toString()
  const res = await fetch(
    `/v1/jobs/${encodeURIComponent(id)}/logs/${stream}${qs ? `?${qs}` : ''}`,
    { headers: authHeaders() },
  )
  if (res.status === 401) {
    triggerUnauthorized()
    throw new Error('未授权（401）：token 无效或已失效')
  }
  if (!res.ok) {
    return raiseForStatus(res)
  }
  const text = await res.text()
  return {
    text,
    total: Number(res.headers.get('X-Log-Total-Lines') ?? '0') || 0,
    offset: Number(res.headers.get('X-Log-Offset') ?? '0') || 0,
    lines: Number(res.headers.get('X-Log-Lines') ?? '0') || logicalLineCount(text),
  }
}

function logicalLineCount(text: string): number {
  if (!text) {
    return 0
  }
  const t = text.endsWith('\n') ? text.slice(0, -1) : text
  return t.length === 0 ? 0 : t.split('\n').length
}

// 产物清单（E1，P2）：GET /v1/jobs/{id}/artifacts。
export function listArtifacts(id: string): Promise<ArtifactsResp> {
  return request<ArtifactsResp>(
    `/v1/jobs/${encodeURIComponent(id)}/artifacts`,
  )
}

// 编码产物相对路径用于 URL：逐段 encodeURIComponent 后用 '/' 连回，保留子目录结构。
function encodeArtifactPath(name: string): string {
  return name.split('/').map(encodeURIComponent).join('/')
}

// 下载单个产物（E1，P2）。下载需带鉴权头，故走 fetch+blob，触发浏览器另存。
// 文件名优先用后端 Content-Disposition；缺失时回退 name 的 basename。
export async function downloadArtifact(id: string, name: string): Promise<void> {
  const url = `/v1/jobs/${encodeURIComponent(id)}/artifacts/${encodeArtifactPath(name)}`
  const res = await fetch(url, { headers: authHeaders() })
  if (res.status === 401) {
    triggerUnauthorized()
    throw new Error('未授权（401）：token 无效或已失效')
  }
  if (!res.ok) {
    return raiseForStatus(res)
  }
  const blob = await res.blob()
  const objURL = URL.createObjectURL(blob)
  const a = document.createElement('a')
  a.href = objURL
  a.download = name.split('/').pop() ?? name
  document.body.appendChild(a)
  a.click()
  a.remove()
  URL.revokeObjectURL(objURL)
}

export async function downloadPtyRecording(id: string): Promise<void> {
  const url = `/v1/jobs/${encodeURIComponent(id)}/pty/recording`
  const res = await fetch(url, { headers: authHeaders() })
  if (res.status === 401) {
    triggerUnauthorized()
    throw new Error('未授权（401）：token 无效或已失效')
  }
  if (!res.ok) {
    return raiseForStatus(res)
  }
  const blob = await res.blob()
  const objURL = URL.createObjectURL(blob)
  const a = document.createElement('a')
  a.href = objURL
  a.download = contentDispositionFilename(res) ?? `${id}.cast`
  document.body.appendChild(a)
  a.click()
  a.remove()
  URL.revokeObjectURL(objURL)
}

function contentDispositionFilename(res: Response): string | null {
  const header = res.headers.get('Content-Disposition')
  if (!header) {
    return null
  }
  const utf8 = header.match(/filename\*=UTF-8''([^;]+)/i)
  if (utf8?.[1]) {
    return decodeURIComponent(utf8[1].trim().replace(/^"|"$/g, ''))
  }
  const plain = header.match(/filename="?([^";]+)"?/i)
  return plain?.[1]?.trim() || null
}

// 取单个产物 blob 供 inline 预览（E19a，P2）。与下载同 endpoint，但只取 blob
// 交给 FilePreview 渲染（不触发浏览器另存）；需带鉴权头故走 fetch + res.blob()。
export async function fetchArtifactBlob(id: string, name: string): Promise<Blob> {
  const url = `/v1/jobs/${encodeURIComponent(id)}/artifacts/${encodeArtifactPath(name)}`
  const res = await fetch(url, { headers: authHeaders() })
  if (res.status === 401) {
    triggerUnauthorized()
    throw new Error('未授权（401）：token 无效或已失效')
  }
  if (!res.ok) {
    return raiseForStatus(res)
  }
  return res.blob()
}

// 拉取完整 diff（E12，P3）。changes.diff 是「未提交改动」(tracked vs HEAD/index)
// 的全量 patch；需带鉴权头，返回 UTF-8 文本交给页面内 <pre> 渲染。
export async function fetchDiffText(id: string): Promise<string> {
  const url = `/v1/jobs/${encodeURIComponent(id)}/diff?full=1`
  const res = await fetch(url, { headers: authHeaders() })
  if (res.status === 401) {
    triggerUnauthorized()
    throw new Error('未授权（401）：token 无效或已失效')
  }
  if (!res.ok) {
    return raiseForStatus(res)
  }
  return res.text()
}

export function cancelJob(id: string): Promise<Job> {
  return request<Job>(`/v1/jobs/${encodeURIComponent(id)}/cancel`, {
    method: 'POST',
  })
}

// 工作流列表（GET /v1/workflows）：可选 status 过滤；返回头部数组（无 steps）。
export function listWorkflows(status?: WorkflowStatus): Promise<WorkflowsResp> {
  const qs = status ? `?status=${encodeURIComponent(status)}` : ''
  return request<WorkflowsResp>(`/v1/workflows${qs}`)
}

// 工作流详情（GET /v1/workflows/{id}）：头部 + 内联 steps 步骤链。
export function getWorkflow(id: string): Promise<Workflow> {
  return request<Workflow>(`/v1/workflows/${encodeURIComponent(id)}`)
}

// 提交 workflow（POST /v1/workflows）：body 是 workflow.Spec JSON。
// Web 侧可从 YAML 解析成对象后复用同一契约，后端继续 BindJSON。
export function submitWorkflow(spec: WorkflowSpec): Promise<Workflow> {
  return request<Workflow>('/v1/workflows', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(spec),
  })
}

// 取消运行中的工作流（POST /v1/workflows/{id}/cancel）：返回更新后的头部快照。
// 取消终态工作流为稳定 no-op（服务端）。
export function cancelWorkflow(id: string): Promise<Workflow> {
  return request<Workflow>(`/v1/workflows/${encodeURIComponent(id)}/cancel`, {
    method: 'POST',
  })
}

// 工作流事件时间线（GET /v1/workflows/{id}/events，P1）：可选 since 增量游标。
// 返回 {events:[...]}，按 seq 升序。详情页据此渲染 fan-out/retry/子 wf 里程碑。
export function getWorkflowEvents(
  id: string,
  since?: number,
): Promise<WorkflowEventsResp> {
  const qs = since && since > 0 ? `?since=${encodeURIComponent(String(since))}` : ''
  return request<WorkflowEventsResp>(
    `/v1/workflows/${encodeURIComponent(id)}/events${qs}`,
  )
}

// 工作流导出（GET /v1/workflows/{id}/export，T4.1）：返回剥离 secret 后的 spec JSON
// 文本（可再导入复现）。响应头 X-Gofer-Redacted=1 表示有占位需替换。requestText 不读
// 头，故这里独立 fetch 以拿到 redacted 标记。
export async function exportWorkflow(
  id: string,
): Promise<{ spec: string; redacted: boolean }> {
  const res = await fetch(`/v1/workflows/${encodeURIComponent(id)}/export`, {
    headers: authHeaders(),
  })
  if (res.status === 401) {
    triggerUnauthorized()
    throw new Error('未授权（401）：token 无效或已失效')
  }
  if (!res.ok) {
    return raiseForStatus(res)
  }
  const spec = await res.text()
  return { spec, redacted: res.headers.get('X-Gofer-Redacted') === '1' }
}

// 运行中交互：作答某个 interaction，返回更新后的 Interaction（status=answered）
export function answerInteraction(
  id: string,
  iid: string,
  answer: string,
): Promise<Interaction> {
  return request<Interaction>(
    `/v1/jobs/${encodeURIComponent(id)}/interactions/${encodeURIComponent(iid)}/answer`,
    {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ answer }),
    },
  )
}

export function puntInteraction(
  id: string,
  iid: string,
): Promise<{ status: string }> {
  return request<{ status: string }>(
    `/v1/jobs/${encodeURIComponent(id)}/interactions/${encodeURIComponent(iid)}/punt`,
    { method: 'POST' },
  )
}

// 断线兜底：拉取某 job 的全部交互（SSE 重连前对齐状态）
export function getInteractions(
  id: string,
): Promise<{ interactions: Interaction[] }> {
  return request<{ interactions: Interaction[] }>(
    `/v1/jobs/${encodeURIComponent(id)}/interactions`,
  )
}

export function listPendingInteractions(): Promise<{
  interactions: Interaction[]
}> {
  return request<{ interactions: Interaction[] }>(
    '/v1/interactions?status=pending',
  )
}

// job 生命周期事件时间线（E13）：初始拉取全量；SSE event 帧增量 append。
// since>0 时仅取该 seq 之后的事件（断线对齐用，可选）。
export function listEvents(id: string, since?: number): Promise<JobEventsResp> {
  const qs = since != null && since > 0 ? `?since=${since}` : ''
  return request<JobEventsResp>(
    `/v1/jobs/${encodeURIComponent(id)}/events${qs}`,
  )
}

// webhook 投递状态（E14）：只读查询，详情页展示每条事件外发的 delivered/重试/失败。
export function listDeliveries(id: string): Promise<DeliveriesResp> {
  return request<DeliveriesResp>(
    `/v1/jobs/${encodeURIComponent(id)}/deliveries`,
  )
}

// cron 定时调度（AUTO-02）CRUD + 操作。均走 authed 组（Bearer token），与 /jobs
// 写操作同级。列表可按 project 过滤；enable/disable/run-now 返回更新后的快照。

export function listSchedules(project?: string): Promise<SchedulesResp> {
  const qs = project ? `?project=${encodeURIComponent(project)}` : ''
  return request<SchedulesResp>(`/v1/schedules${qs}`)
}

export function getSchedule(id: string): Promise<Schedule> {
  return request<Schedule>(`/v1/schedules/${encodeURIComponent(id)}`)
}

export function createSchedule(req: CreateScheduleReq): Promise<Schedule> {
  return request<Schedule>('/v1/schedules', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(req),
  })
}

export function deleteSchedule(id: string): Promise<{ status: string }> {
  return request<{ status: string }>(`/v1/schedules/${encodeURIComponent(id)}`, {
    method: 'DELETE',
  })
}

// 启用/停用：走 /enable 或 /disable 子路径（后端 setScheduleEnabled）。
export function setScheduleEnabled(id: string, enabled: boolean): Promise<Schedule> {
  const verb = enabled ? 'enable' : 'disable'
  return request<Schedule>(`/v1/schedules/${encodeURIComponent(id)}/${verb}`, {
    method: 'POST',
  })
}

// 立即触发一次（不改 next_run_at）：返回提交的 job（channel=cron）。
export function runSchedule(id: string): Promise<Job> {
  return request<Job>(`/v1/schedules/${encodeURIComponent(id)}/run-now`, {
    method: 'POST',
  })
}

// SSE 流（转发到 api/sse.ts）
export function streamJobLogs(id: string, opts: StreamJobOpts): Promise<void> {
  return streamJob(id, opts)
}

// 状态 -> 视觉 token 颜色（供 Board/JobDetail 等复用）
const STATUS_COLOR: Record<JobStatus, string> = {
  running: 'var(--run)',
  pending_interaction: 'var(--phosphor)',
  done: 'var(--done)',
  failed: 'var(--fail)',
  timeout: 'var(--fail)',
  queued: 'var(--queue)',
  cancelled: 'var(--queue)',
}

export function statusColor(status: JobStatus): string {
  return STATUS_COLOR[status] ?? 'var(--queue)'
}
