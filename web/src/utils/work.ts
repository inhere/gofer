// 「工作」页的纯逻辑：状态元数据、分栏 / 按工作区分组、筛选、提醒时间预设、
// "请它汇报"的可用性。组件只负责渲染，规则都在这里（vitest 直接测）。
import { fmtAgo } from '../api/time'
import type { WorkItem, WorkRequest, WorkStatus, WorkSuggestion } from '../api/types'

export type WorkTone = 'live' | 'hot' | 'warn' | 'ok' | 'idle' | 'off'

export interface WorkStatusMeta {
  key: WorkStatus
  label: string
  tone: WorkTone
  hint: string
}

// 设计 §5 的 8 个状态；前 6 个是看板的列，done / dropped 只在「显示已完成」时出现。
export const WORK_STATUSES: WorkStatusMeta[] = [
  { key: 'active', label: '进行中', tone: 'live', hint: '会话正在干活' },
  { key: 'needs_me', label: '等我', tone: 'hot', hint: '需要你回复 / 决策' },
  { key: 'waiting_resource', label: '等资源', tone: 'warn', hint: '缺设备、账号、数据或别人的东西' },
  { key: 'needs_onsite', label: '需现场', tone: 'warn', hint: '要到现场操作' },
  { key: 'review', label: '待验收', tone: 'hot', hint: '做完了，等你检查' },
  { key: 'parked', label: '已搁置', tone: 'idle', hint: '暂时不做，到点或条件满足再说' },
  { key: 'done', label: '已完成', tone: 'ok', hint: '' },
  { key: 'dropped', label: '已放弃', tone: 'off', hint: '' },
]

export const BOARD_STATUSES: WorkStatus[] = ['active', 'needs_me', 'waiting_resource', 'needs_onsite', 'review', 'parked']
export const CLOSED_STATUSES: WorkStatus[] = ['done', 'dropped']

const META = new Map(WORK_STATUSES.map((m) => [m.key, m]))

export function statusMeta(s: WorkStatus): WorkStatusMeta {
  return META.get(s) ?? { key: s, label: s, tone: 'idle', hint: '' }
}
export function statusLabel(s: WorkStatus): string {
  return statusMeta(s).label
}
export function statusTone(s: WorkStatus): WorkTone {
  return statusMeta(s).tone
}

export function isClosed(s: WorkStatus): boolean {
  return s === 'done' || s === 'dropped'
}

// 顶部筛选：all 不筛；needs_me / due / unsorted 对应三个计数徽标。
export type WorkFilter = 'all' | 'needs_me' | 'due' | 'unsorted'

export function matchesFilter(it: WorkItem, f: WorkFilter): boolean {
  switch (f) {
    case 'needs_me':
      return it.status === 'needs_me'
    case 'due':
      return it.due
    case 'unsorted':
      return it.unsorted
    default:
      return true
  }
}

export function matchesQuery(it: WorkItem, q: string): boolean {
  const needle = q.trim().toLowerCase()
  if (!needle) return true
  return [it.title, it.goal, it.blocker_text, it.next_step, it.project_key, it.workspace, it.id]
    .some((v) => (v ?? '').toLowerCase().includes(needle))
}

export interface WorkView {
  filter: WorkFilter
  q: string
  showClosed: boolean
}

export function visibleItems(items: WorkItem[], v: WorkView): WorkItem[] {
  return items.filter((it) => (v.showClosed || !isClosed(it.status)) && matchesFilter(it, v.filter) && matchesQuery(it, v.q))
}

// 「未整理」区：自动生成、还没补目标的草稿（已结束的不算）。
export function unsortedItems(items: WorkItem[]): WorkItem[] {
  return items.filter((it) => it.unsorted && !isClosed(it.status))
}

// 排序：到期提醒的、再按最后活动倒序。
export function sortCards(items: WorkItem[]): WorkItem[] {
  return [...items].sort((a, b) => {
    if (a.due !== b.due) return a.due ? -1 : 1
    if (a.last_activity_at !== b.last_activity_at) return b.last_activity_at - a.last_activity_at
    return a.id.localeCompare(b.id)
  })
}

export interface WorkColumn {
  status: WorkStatus
  label: string
  tone: WorkTone
  hint: string
  items: WorkItem[]
}

// 按状态分栏。未整理的草稿只在「未整理」区出现，不进列（避免一张卡出现两次）；
// 过滤模式（等我 / 到期提醒）下它们照常参与，所以 includeUnsorted 由调用方决定。
export function groupByStatus(items: WorkItem[], opts: { showClosed: boolean; includeUnsorted?: boolean }): WorkColumn[] {
  const statuses = opts.showClosed ? [...BOARD_STATUSES, ...CLOSED_STATUSES] : BOARD_STATUSES
  return statuses.map((st) => {
    const m = statusMeta(st)
    const col = items.filter((it) => it.status === st && (opts.includeUnsorted || !it.unsorted))
    return { status: st, label: m.label, tone: m.tone, hint: m.hint, items: sortCards(col) }
  })
}

export interface WorkGroup {
  key: string
  label: string
  items: WorkItem[]
}

// 工作区的显示名：目录最后两段；没有工作区的归到「（未指定工作区）」。
export function workspaceLabel(ws: string | undefined, project?: string): string {
  const w = (ws ?? '').trim()
  if (!w) return project ? `（${project}）` : '（未指定工作区）'
  const parts = w.replace(/\\/g, '/').split('/').filter(Boolean)
  return parts.slice(-2).join('/') || w
}

// 按工作区分组（解决「一个工作区多个会话 / 多件事」）：组内按状态优先级再按活动时间。
const STATUS_ORDER = new Map<WorkStatus, number>(
  (['needs_me', 'review', 'active', 'needs_onsite', 'waiting_resource', 'parked', 'done', 'dropped'] as WorkStatus[]).map((s, i) => [s, i]),
)

export function groupByWorkspace(items: WorkItem[]): WorkGroup[] {
  const groups = new Map<string, WorkGroup>()
  for (const it of items) {
    const key = (it.workspace ?? '').trim() || `project:${it.project_key ?? ''}`
    let g = groups.get(key)
    if (!g) {
      g = { key, label: workspaceLabel(it.workspace, it.project_key), items: [] }
      groups.set(key, g)
    }
    g.items.push(it)
  }
  const out = [...groups.values()]
  for (const g of out) {
    g.items.sort((a, b) => {
      const sa = STATUS_ORDER.get(a.status) ?? 99
      const sb = STATUS_ORDER.get(b.status) ?? 99
      if (sa !== sb) return sa - sb
      return b.last_activity_at - a.last_activity_at
    })
  }
  // 有「等我」的工作区靠前，其次按最近活动
  out.sort((a, b) => {
    const ha = a.items.some((i) => i.status === 'needs_me') ? 0 : 1
    const hb = b.items.some((i) => i.status === 'needs_me') ? 0 : 1
    if (ha !== hb) return ha - hb
    const la = Math.max(...a.items.map((i) => i.last_activity_at))
    const lb = Math.max(...b.items.map((i) => i.last_activity_at))
    return lb - la || a.label.localeCompare(b.label)
  })
  return out
}

// 当前会话里「在运行」的：能收到传话 / 汇报请求的。
const RUNNING_STATES = new Set(['running', 'idle', 'waiting_reply', 'needs_attention'])

export function runningSessionIds(it: WorkItem): string[] {
  return it.sessions.filter((s) => s.role === 'current' && !s.missing && !!s.state && RUNNING_STATES.has(s.state)).map((s) => s.session_id)
}

// 「请它汇报」：在运行的会话经账本收到请求；不在运行的会话（或送达失败）自动改为整理——
// 所以只要有当前会话就能点；没有会话时灰显并说明。
export function reportRequestBlock(it: WorkItem): string {
  if (!it.sessions.some((s) => s.role === 'current')) return '没有关联会话，无法请它汇报'
  return ''
}

// 「整理」按钮：读会话的 transcript 尾部提炼；同样需要一个当前会话。
export function summarizeBlock(it: WorkItem): string {
  if (!it.sessions.some((s) => s.role === 'current' && !s.missing)) return '没有可读取的会话，无法整理'
  return ''
}

// 卡片上的「主会话」：优先在运行的，其次最近见到的当前会话。
export function primarySession(it: WorkItem): WorkItem['sessions'][number] | undefined {
  const cur = it.sessions.filter((s) => s.role === 'current')
  const running = cur.filter((s) => !!s.state && RUNNING_STATES.has(s.state))
  const pool = running.length ? running : cur
  return [...pool].sort((a, b) => (b.last_seen_at ?? 0) - (a.last_seen_at ?? 0))[0]
}

// ---------------- 搁置 / 提醒时间 ----------------

export interface WhenPreset {
  key: string
  label: string
  at: number // unix 秒
}

function atHour(base: Date, dayOffset: number, hour: number): number {
  const d = new Date(base.getFullYear(), base.getMonth(), base.getDate() + dayOffset, hour, 0, 0, 0)
  return Math.floor(d.getTime() / 1000)
}

export function whenPresets(now: Date): WhenPreset[] {
  const sec = Math.floor(now.getTime() / 1000)
  const out: WhenPreset[] = [
    { key: '1h', label: '1 小时后', at: sec + 3600 },
    { key: '3h', label: '3 小时后', at: sec + 3 * 3600 },
  ]
  if (now.getHours() < 18) out.push({ key: 'tonight', label: '今晚 18:00', at: atHour(now, 0, 18) })
  out.push({ key: 'tomorrow', label: '明天 09:00', at: atHour(now, 1, 9) })
  const toMonday = ((8 - now.getDay()) % 7) || 7
  out.push({ key: 'monday', label: '下周一 09:00', at: atHour(now, toMonday, 9) })
  return out
}

// <input type="datetime-local"> 的值 <-> unix 秒（本地时区）。空 / 非法返回 0。
export function localInputToUnix(v: string): number {
  if (!v) return 0
  const t = new Date(v).getTime()
  return Number.isFinite(t) ? Math.floor(t / 1000) : 0
}

export function unixToLocalInput(sec: number): string {
  if (!sec) return ''
  const d = new Date(sec * 1000)
  const p = (n: number) => String(n).padStart(2, '0')
  return `${d.getFullYear()}-${p(d.getMonth() + 1)}-${p(d.getDate())}T${p(d.getHours())}:${p(d.getMinutes())}`
}

export function dueText(it: WorkItem): string {
  if (!it.due) return ''
  if (it.status === 'parked' && it.park_until && it.park_until * 1000 <= Date.now()) return '搁置到期'
  return '提醒到期'
}

// 关联项小标签：issue / todo / job / plan 计数。
export function linkTags(it: WorkItem): string[] {
  const n: Record<string, number> = {}
  for (const l of it.links) n[l.kind] = (n[l.kind] ?? 0) + 1
  return (['issue', 'plan', 'todo', 'job'] as const).filter((k) => n[k]).map((k) => `${k}${n[k] > 1 ? ' ×' + n[k] : ''}`)
}

// ---------------- 发言者标注（W2a） ----------------
// by 的规范拼写：human:<caller> / session:<sid>(<agent>) / steward(<agent>) / summarizer(<agent>) / job:<id> / system

export type ActorKind = 'human' | 'session' | 'summarizer' | 'steward' | 'job' | 'system' | 'other'

export function actorKind(by: string | undefined): ActorKind {
  const b = (by ?? '').trim()
  if (b === '' || b === 'system') return 'system'
  if (b === 'human' || b.startsWith('human:')) return 'human'
  if (b === 'session' || b.startsWith('session:')) return 'session'
  if (b === 'steward' || b.startsWith('steward(')) return 'steward'
  if (b === 'summarizer' || b.startsWith('summarizer(')) return 'summarizer'
  if (b.startsWith('job:')) return 'job'
  return 'other'
}

function agentOf(by: string): string {
  const m = /\(([^)]*)\)\s*$/.exec(by)
  return m ? m[1] : ''
}

// 日志作者 / 字段来源的显示：我 / 会话 abcdef01 (claude) / 整理器 (claude) / 管家 (…) / job … / 系统
export function actorLabel(by: string | undefined): string {
  const b = (by ?? '').trim()
  const agent = agentOf(b)
  const suffix = agent ? ` (${agent})` : ''
  switch (actorKind(b)) {
    case 'human':
      return '我'
    case 'session': {
      const rest = b.slice('session:'.length)
      const sid = rest.replace(/\(.*$/, '')
      return `会话 ${sid.slice(0, 8)}${suffix}`.trim()
    }
    case 'summarizer':
      return `整理器${suffix}`
    case 'steward':
      return `管家${suffix}`
    case 'job':
      return `job ${b.slice(4, 12)}`
    case 'system':
      return '系统'
    default:
      return b
  }
}

export const journalByLabel = actorLabel

// 字段来源行：「整理器 (claude) · 5分前」；没有来源记录返回空串。
export function fieldSourceText(it: WorkItem, field: string, nowSec?: number): string {
  const fs = it.field_sources?.[field]
  if (!fs || !fs.by) return ''
  return `${actorLabel(fs.by)} · ${fmtAgo(fs.at, nowSec)}`
}

export function fieldSourceKind(it: WorkItem, field: string): ActorKind | '' {
  const fs = it.field_sources?.[field]
  return fs && fs.by ? actorKind(fs.by) : ''
}

// ---------------- 整理建议 ----------------

const SUGGESTION_LABELS: Record<string, string> = {
  goal: '目标',
  blocker: '阻塞',
  blocker_kind: '阻塞类型',
  next: '下一步',
  summary: '摘要',
  status_hint: '状态',
}

export function suggestionLabel(field: string): string {
  return SUGGESTION_LABELS[field] ?? field
}

// status_hint 的值是状态 key，显示成中文标签。
export function suggestionValueText(sg: WorkSuggestion): string {
  if (sg.field === 'status_hint') return statusLabel(sg.value as WorkStatus)
  return sg.value
}

export function pendingSuggestions(it: WorkItem): WorkSuggestion[] {
  return (it.suggestions ?? []).filter((s) => s.state === 'pending' || !s.state)
}

// ---------------- 请求账本 ----------------

const REQUEST_KIND: Record<string, string> = { report: '汇报请求', handoff: '交接请求', summarize: '整理' }
const REQUEST_STATE: Record<string, string> = { pending: '进行中', sent: '已送达', answered: '已完成', failed: '失败', expired: '超时' }

export function requestInFlight(r: WorkRequest): boolean {
  return r.state === 'pending' || r.state === 'sent'
}

export function inflightRequests(it: WorkItem): WorkRequest[] {
  return (it.requests ?? []).filter(requestInFlight)
}

// 最近一条已结束的请求（卡片上提示结果：会话已回复 / 未回应已改为整理 / 整理失败）。
export function lastFinishedRequest(it: WorkItem): WorkRequest | undefined {
  return (it.requests ?? []).filter((r) => !requestInFlight(r)).sort((a, b) => b.created_at - a.created_at)[0]
}

// 一行说明：「汇报请求 · 已送达，等回复（12 分后超时）」/「整理 · 失败：整理器不可用…」
export function requestLine(r: WorkRequest, nowSec: number = Math.floor(Date.now() / 1000)): string {
  const kind = REQUEST_KIND[r.kind] ?? r.kind
  switch (r.state) {
    case 'pending':
      return r.kind === 'summarize' ? `${kind} · 进行中…` : `${kind} · 发送中…`
    case 'sent': {
      const left = r.deadline ? Math.max(0, Math.ceil((r.deadline - nowSec) / 60)) : 0
      return `${kind} · 已送达，等会话回复${r.deadline ? `（${left} 分钟后超时）` : ''}`
    }
    case 'answered':
      return r.kind === 'summarize' ? `${kind} · 已完成` : `${kind} · 会话已回复`
    case 'expired':
      return `${kind} · 会话未回应，已改为整理`
    case 'failed':
      return `${kind} · 失败${r.error ? '：' + r.error : ''}`
    default:
      return `${kind} · ${REQUEST_STATE[r.state] ?? r.state}`
  }
}
