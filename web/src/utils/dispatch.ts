// 收集箱分派（design 2026-10-16 §3）的纯逻辑：推荐候选的展示数据、各分支请求体、409 提示、
// 按权限隐藏动作、新建 job 的预填与「提交后关联」流程、转 issue 的仓库过滤、「待同步」展示规则。
// 组件只负责渲染；服务端仍是唯一权威，这里只提前给出可读的错误与不必要的按钮隐藏。
import type { AgentSession, TrackerRepo, WorkItem, WorkLink } from '../api/types'
import type { AuthWhoami } from './permissions'
import { holdsPermission } from './permissionGate'
import { hitLabel, MAYBE_OFFLINE_LABEL } from './sessionTopic'

export type DispatchTo = 'session' | 'job' | 'todo' | 'issue' | 'archive' | 'drop'

export interface DispatchRequest {
  to: DispatchTo
  session_id?: string
  job_id?: string
  plan_id?: string
  new_plan_title?: string
  tracker_id?: string
  title?: string
}

export interface DispatchForm {
  sessionId: string
  jobId: string
  planId: string // '' = 新建 plan
  newPlanTitle: string
  trackerId: string
  issueTitle: string
}

export const EMPTY_DISPATCH_FORM: DispatchForm = { sessionId: '', jobId: '', planId: '', newPlanTitle: '', trackerId: '', issueTitle: '' }

export interface DispatchBuild {
  req: DispatchRequest | null
  error: string
}

// buildDispatchRequest：每个分支只带自己需要的字段，缺必填项给出可读提示（不发请求）。
export function buildDispatchRequest(to: DispatchTo, f: DispatchForm): DispatchBuild {
  switch (to) {
    case 'session':
      if (!f.sessionId.trim()) return { req: null, error: '先选一个会话' }
      return { req: { to, session_id: f.sessionId.trim() }, error: '' }
    case 'job':
      if (!f.jobId.trim()) return { req: null, error: '先提交 job，再关联到条目' }
      return { req: { to, job_id: f.jobId.trim() }, error: '' }
    case 'todo': {
      const req: DispatchRequest = { to }
      if (f.planId.trim()) req.plan_id = f.planId.trim()
      else if (f.newPlanTitle.trim()) req.new_plan_title = f.newPlanTitle.trim()
      return { req, error: '' }
    }
    case 'issue': {
      if (!f.trackerId.trim()) return { req: null, error: '先选一个仓库' }
      const req: DispatchRequest = { to, tracker_id: f.trackerId.trim() }
      if (f.issueTitle.trim()) req.title = f.issueTitle.trim()
      return { req, error: '' }
    }
    case 'archive':
    case 'drop':
      return { req: { to }, error: '' }
    default:
      return { req: null, error: '未知的分派方式' }
  }
}

export const DISPATCH_ACTIONS: Array<{ to: DispatchTo; label: string; hint: string }> = [
  { to: 'session', label: '交给会话', hint: '把条目内容作为信件发给一个会话（对方下一轮开始时收到），条目转为进行中' },
  { to: 'job', label: '新建 job', hint: '带着条目内容去新建 job，提交后自动关联' },
  { to: 'todo', label: '转 todo', hint: '加到一个 plan 里作为 todo' },
  { to: 'issue', label: '转 issue', hint: '在仓库里新建 issue，同步前带「待同步」标记' },
  { to: 'archive', label: '归档', hint: '不处理了，标为已完成' },
  { to: 'drop', label: '丢弃', hint: '放弃这条' },
]

// ---- 推荐候选 ----

export interface TargetHit {
  label: string
  text: string
  inferred: boolean
}

export interface TargetRow {
  sessionId: string
  title: string
  agent: string
  project: string
  topic: string
  topicInferred: boolean
  hits: TargetHit[]
  maybeOffline: boolean
  offlineLabel: string
  callerId: string
}

// targetRows：dispatch-targets 的展示数据；推断出来的命中带 inferred，界面要标出来。
export function targetRows(targets: AgentSession[] | undefined): TargetRow[] {
  return (targets ?? []).map((s) => ({
    sessionId: s.session_id,
    title: s.title || s.peer_name || s.session_id.slice(0, 8),
    agent: s.agent,
    project: s.project_key ?? '',
    topic: s.topic ?? '',
    topicInferred: s.topic_source === 'inferred',
    hits: (s.hits ?? []).map((h) => ({
      label: hitLabel(h),
      text: h.match ? `${hitLabel(h)}：${h.match}` : hitLabel(h),
      inferred: h.source === 'inferred',
    })),
    maybeOffline: s.maybe_offline === true,
    offlineLabel: s.maybe_offline ? MAYBE_OFFLINE_LABEL : '',
    callerId: s.caller_id ?? '',
  }))
}

// ---- 权限 ----

export interface DispatchVisibility {
  session: boolean
  job: boolean
  todo: boolean
  issue: boolean
  archive: boolean
  drop: boolean
}

// 是别人的资源且没有 *.others 权限就别摆出来；who 未知时一律显示，由服务端 403 说话。
function ownsOrHolds(who: AuthWhoami | null | undefined, ownerId: string | undefined, perm: string): boolean {
  if (who && ownerId && ownerId === who.caller_id) return true
  return holdsPermission(who, perm)
}

// dispatchVisibility：分派动作按权限显示。交给会话的细粒度（他人的会话需 session.reply.others）
// 在候选 / 选择器那一行用 canReplySession 判断；动作本身只要有任一可用会话就留着。
export function dispatchVisibility(who: AuthWhoami | null | undefined): DispatchVisibility {
  return {
    session: true,
    job: holdsPermission(who, 'job.submit'),
    todo: true,
    issue: true,
    archive: true,
    drop: true,
  }
}

export function canReplySession(who: AuthWhoami | null | undefined, s: Pick<AgentSession, 'caller_id'>): boolean {
  return ownsOrHolds(who, s.caller_id, 'session.reply.others')
}

// 转 todo 选了已有 plan：他人的 plan 需 plan.manage.others；plan 无 owner 信息时不拦。
export function canManagePlan(who: AuthWhoami | null | undefined, ownerId?: string): boolean {
  return ownsOrHolds(who, ownerId, 'plan.manage.others')
}

// ---- 错误 ----

export interface DispatchErrorLike {
  status?: number
  message?: string
  detail?: string
  body?: unknown
}

export interface DispatchErrorView {
  text: string
  // 条目已不在收集箱 / todo 已转过：刷新条目比重试更有用
  refresh: boolean
}

// dispatchErrorText：409 用服务端的 detail（会话送不出去的原因、仓库缺 id 前缀等）；
// todo 已转过带 todo_id / plan_id；其余回落到 message。
export function dispatchErrorText(to: DispatchTo, e: DispatchErrorLike): DispatchErrorView {
  const b = (e.body ?? {}) as { todo_id?: string; plan_id?: string }
  if (e.status === 409) {
    if (to === 'todo' && (b.todo_id || b.plan_id)) {
      return { text: `已经转过了：todo ${b.todo_id ?? ''}（plan ${b.plan_id ?? ''}）`, refresh: true }
    }
    if (e.detail) return { text: e.detail, refresh: to !== 'session' && to !== 'issue' }
    if (to === 'session') return { text: '会话没能收下这条，条目仍在收集箱', refresh: false }
    return { text: e.message || '这条已经不在收集箱了', refresh: true }
  }
  const base = e.message || '分派失败'
  return { text: e.detail && !base.includes(e.detail) ? `${base}：${e.detail}` : base, refresh: false }
}

// ---- 新建 job 的预填与关联 ----

export const FROM_ITEM_QUERY = 'from_item'

export function readFromItemQuery(v: unknown): string {
  const x = Array.isArray(v) ? v[0] : v
  return typeof x === 'string' ? x.trim() : ''
}

// newJobLink：收集箱条目 → 新建 job 页。
export function newJobLink(itemId: string): string {
  return `/new?${FROM_ITEM_QUERY}=${encodeURIComponent(itemId)}`
}

export function workItemLink(itemId: string): string {
  return `/work?id=${encodeURIComponent(itemId)}`
}

export interface NewJobPrefill {
  prompt: string
  project: string
  cwd: string
}

function relativeCwd(c: string | undefined): string {
  const v = (c ?? '').trim()
  if (v === '' || v.startsWith('/') || v.startsWith('\\') || /^[A-Za-z]:/.test(v) || v.startsWith('~')) return ''
  return v
}

// newJobPrefill：prompt = 标题 + 目标（相同则只写一份）；项目取条目的 project_key；
// 目录只在是项目相对路径时带上（绝对路径提交会被拒）。
export function newJobPrefill(it: Pick<WorkItem, 'title' | 'goal' | 'project_key' | 'workspace'>): NewJobPrefill {
  const title = (it.title ?? '').trim()
  const goal = (it.goal ?? '').trim()
  const prompt = goal && goal !== title ? (title ? `${title}\n\n${goal}` : goal) : title || goal
  return { prompt, project: it.project_key ?? '', cwd: relativeCwd(it.workspace) }
}

export interface LinkOutcome {
  linked: boolean
  error: string
}

// linkJobAfterSubmit：job 已提交后把它关联到条目。失败不丢 job——调用方保留 jobId，
// 显示 error 与「重试关联」。dispatch 由调用方注入（纯函数便于测试）。
export async function linkJobAfterSubmit(
  dispatch: (req: DispatchRequest) => Promise<unknown>,
  jobId: string,
): Promise<LinkOutcome> {
  const built = buildDispatchRequest('job', { ...EMPTY_DISPATCH_FORM, jobId })
  if (!built.req) return { linked: false, error: built.error }
  try {
    await dispatch(built.req)
    return { linked: true, error: '' }
  } catch (e) {
    return { linked: false, error: dispatchErrorText('job', e as DispatchErrorLike).text }
  }
}

// ---- 转 issue / 新建 issue ----

// 只有记录了 id 前缀的仓库能新建 issue（没同步过的仓库 server 会 409）。
export function issueRepos(repos: TrackerRepo[] | undefined): TrackerRepo[] {
  return (repos ?? []).filter((r) => (r.prefix ?? '').trim() !== '')
}

export function issueRepoLabel(r: Pick<TrackerRepo, 'tracker_id' | 'project_key' | 'prefix'>): string {
  return `${r.project_key || r.tracker_id}（${r.prefix}-）`
}

export const ISSUE_TYPES = ['task', 'bug', 'feature', 'chore', 'epic'] as const
export const ISSUE_PRIORITIES: Array<{ value: number; label: string }> = [
  { value: 0, label: 'P0 紧急' },
  { value: 1, label: 'P1 高' },
  { value: 2, label: 'P2 普通' },
  { value: 3, label: 'P3 低' },
  { value: 4, label: 'P4 待定' },
]
export const ISSUE_TITLE_MAX = 300

export interface NewIssueForm {
  trackerId: string
  title: string
  description: string
  priority: string
  type: string
}

export interface NewIssueRequest {
  title: string
  description?: string
  priority: number
  type: string
}

export interface NewIssueBuild {
  req: NewIssueRequest | null
  error: string
}

// buildNewIssueRequest：Issues 页「新建 issue」表单校验与请求体（POST /v1/tracker/repos/{id}/issues）。
export function buildNewIssueRequest(f: NewIssueForm, repos: TrackerRepo[] | undefined): NewIssueBuild {
  const repo = (repos ?? []).find((r) => r.tracker_id === f.trackerId)
  if (!repo) return { req: null, error: '先选一个仓库' }
  if (!(repo.prefix ?? '').trim()) return { req: null, error: '这个仓库还没有 id 前缀，先在仓库里执行 gofer repo sync' }
  const title = f.title.trim()
  if (!title) return { req: null, error: '标题不能为空' }
  if ([...title].length > ISSUE_TITLE_MAX) return { req: null, error: `标题最多 ${ISSUE_TITLE_MAX} 个字符` }
  const priority = Number.parseInt(f.priority, 10)
  if (!Number.isFinite(priority) || priority < 0 || priority > 4) return { req: null, error: '优先级取 0–4' }
  const type = (ISSUE_TYPES as readonly string[]).includes(f.type) ? f.type : 'task'
  const req: NewIssueRequest = { title, priority, type }
  const description = f.description.trim()
  if (description) req.description = description
  return { req, error: '' }
}

// ---- 待同步 ----

export const PENDING_SYNC_LABEL = '待同步'
export const PENDING_SYNC_HINT = 'server 已新建这个 issue，仓库下次 gofer repo sync 后才会拉到本地'

// 只有 issue 关联且 pending_sync 为真才显示；同步后 server 清除标记，标记随之消失。
export function isPendingSync(l: Pick<WorkLink, 'kind'> & { pending_sync?: boolean }): boolean {
  return l.kind === 'issue' && l.pending_sync === true
}

export function pendingSyncCount(links: Array<Pick<WorkLink, 'kind'> & { pending_sync?: boolean }> | undefined): number {
  return (links ?? []).filter(isPendingSync).length
}
