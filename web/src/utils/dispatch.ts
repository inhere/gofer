// 收集箱分派（design 2026-10-16 §3）的纯逻辑：推荐候选的展示数据、各分支请求体、409 提示、
// 按权限隐藏动作、新建 job 的预填与「提交后关联」流程、转 issue 的仓库过滤、「待同步」展示规则。
// 组件只负责渲染；服务端仍是唯一权威，这里只提前给出可读的错误与不必要的按钮隐藏。
import type { AgentSession, TrackerRepo, WorkItem, WorkLink } from '../api/types'
import { t } from '../i18n'
import type { AuthWhoami } from './permissions'
import { holdsPermission } from './permissionGate'
import { hitLabel, maybeOfflineLabel } from './sessionTopic'

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
      if (!f.sessionId.trim()) return { req: null, error: t('util.dispatch.err.pickSession') }
      return { req: { to, session_id: f.sessionId.trim() }, error: '' }
    case 'job':
      if (!f.jobId.trim()) return { req: null, error: t('util.dispatch.err.pickJob') }
      return { req: { to, job_id: f.jobId.trim() }, error: '' }
    case 'todo': {
      const req: DispatchRequest = { to }
      if (f.planId.trim()) req.plan_id = f.planId.trim()
      else if (f.newPlanTitle.trim()) req.new_plan_title = f.newPlanTitle.trim()
      return { req, error: '' }
    }
    case 'issue': {
      if (!f.trackerId.trim()) return { req: null, error: t('util.dispatch.err.pickRepo') }
      const req: DispatchRequest = { to, tracker_id: f.trackerId.trim() }
      if (f.issueTitle.trim()) req.title = f.issueTitle.trim()
      return { req, error: '' }
    }
    case 'archive':
    case 'drop':
      return { req: { to }, error: '' }
    default:
      return { req: null, error: t('util.dispatch.err.unknown') }
  }
}

// 动作的名字与说明（getter：渲染时取当前语言）。
export const DISPATCH_ACTIONS: Array<{ to: DispatchTo; label: string; hint: string }> = [
  { to: 'session', get label() { return t('util.dispatch.act.session') }, get hint() { return t('util.dispatch.act.sessionHint') } },
  { to: 'job', get label() { return t('util.dispatch.act.job') }, get hint() { return t('util.dispatch.act.jobHint') } },
  { to: 'todo', get label() { return t('util.dispatch.act.todo') }, get hint() { return t('util.dispatch.act.todoHint') } },
  { to: 'issue', get label() { return t('util.dispatch.act.issue') }, get hint() { return t('util.dispatch.act.issueHint') } },
  { to: 'archive', get label() { return t('util.dispatch.act.archive') }, get hint() { return t('util.dispatch.act.archiveHint') } },
  { to: 'drop', get label() { return t('util.dispatch.act.drop') }, get hint() { return t('util.dispatch.act.dropHint') } },
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
      text: h.match ? t('util.dispatch.hit', { label: hitLabel(h), match: h.match }) : hitLabel(h),
      inferred: h.source === 'inferred',
    })),
    maybeOffline: s.maybe_offline === true,
    offlineLabel: s.maybe_offline ? maybeOfflineLabel() : '',
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
      return { text: t('util.dispatch.err.converted', { todo: b.todo_id ?? '', plan: b.plan_id ?? '' }), refresh: true }
    }
    if (e.detail) return { text: e.detail, refresh: to !== 'session' && to !== 'issue' }
    if (to === 'session') return { text: t('util.dispatch.err.sessionRefused'), refresh: false }
    return { text: e.message || t('util.dispatch.err.notInInbox'), refresh: true }
  }
  const base = e.message || t('util.dispatch.err.failed')
  return { text: e.detail && !base.includes(e.detail) ? t('util.dispatch.err.withDetail', { base, detail: e.detail }) : base, refresh: false }
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

// newJobPrefill：prompt 优先带完整收集原文（旧条目仍用标题）和目标；项目取条目的 project_key；
// 目录只在是项目相对路径时带上（绝对路径提交会被拒）。
export function newJobPrefill(it: Pick<WorkItem, 'title' | 'goal' | 'capture_text' | 'project_key' | 'workspace'>): NewJobPrefill {
  const title = (it.capture_text || it.title || '').trim()
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
  return t('util.dispatch.repoLabel', { name: r.project_key || r.tracker_id, prefix: r.prefix })
}

export const ISSUE_TYPES = ['task', 'bug', 'feature', 'chore', 'epic'] as const
// 优先级的名字（getter：渲染时取当前语言）。
export const ISSUE_PRIORITIES: Array<{ value: number; label: string }> = [
  { value: 0, get label() { return t('util.dispatch.prio.0') } },
  { value: 1, get label() { return t('util.dispatch.prio.1') } },
  { value: 2, get label() { return t('util.dispatch.prio.2') } },
  { value: 3, get label() { return t('util.dispatch.prio.3') } },
  { value: 4, get label() { return t('util.dispatch.prio.4') } },
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
  if (!repo) return { req: null, error: t('util.dispatch.err.pickRepo') }
  if (!(repo.prefix ?? '').trim()) return { req: null, error: t('util.dispatch.err.noRepoPrefix') }
  const title = f.title.trim()
  if (!title) return { req: null, error: t('util.dispatch.err.emptyTitle') }
  if ([...title].length > ISSUE_TITLE_MAX) return { req: null, error: t('util.dispatch.err.titleTooLong', { n: ISSUE_TITLE_MAX }) }
  const priority = Number.parseInt(f.priority, 10)
  if (!Number.isFinite(priority) || priority < 0 || priority > 4) return { req: null, error: t('util.dispatch.err.priority') }
  const type = (ISSUE_TYPES as readonly string[]).includes(f.type) ? f.type : 'task'
  const req: NewIssueRequest = { title, priority, type }
  const description = f.description.trim()
  if (description) req.description = description
  return { req, error: '' }
}

// ---- 待同步 ----

// 「待同步」标记文案（页面上的 chip 与悬停说明）。
export function pendingSyncLabel(): string {
  return t('util.dispatch.pendingSync')
}

export function pendingSyncHint(): string {
  return t('util.dispatch.pendingSyncHint')
}

// 只有 issue 关联且 pending_sync 为真才显示；同步后 server 清除标记，标记随之消失。
export function isPendingSync(l: Pick<WorkLink, 'kind'> & { pending_sync?: boolean }): boolean {
  return l.kind === 'issue' && l.pending_sync === true
}

export function pendingSyncCount(links: Array<Pick<WorkLink, 'kind'> & { pending_sync?: boolean }> | undefined): number {
  return (links ?? []).filter(isPendingSync).length
}
