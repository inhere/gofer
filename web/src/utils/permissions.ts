// 设置 → 权限 的纯逻辑：展开预览的结果整理、风险确认、只读判定、「有新权限待确认」、403 文案。
// 模式怎么展开【不在这里】：规则只在服务端 internal/authz 一份，界面调 GET /v1/auth/expand 预览
// （api/permissions.ts expandGrantPattern），这里只把服务端的回答整理成界面要用的形状。

import { callerLabel } from './callerLabel'
import type { CallerNames } from './callerLabel'

export type PermRisk = 'low' | 'medium' | 'high' | 'dangerous' | 'root'
export type SubjectKind = 'users' | 'caller' | 'session'

export interface CatalogPermission {
  name: string
  description: string
  risk: PermRisk | string
  risk_label: string
  session_grantable: boolean
  scope: string
  scope_label: string
  source: string
  source_label: string
  seed_version?: number
}

export interface PermissionPreset {
  key: string
  label: string
  patterns: string[]
  permissions: string[]
}

export interface PermissionCatalog {
  permissions: CatalogPermission[]
  presets: PermissionPreset[]
}

export interface AuthGrant {
  id: string
  subject: string
  source: string
  pattern?: string
  scope?: string
  state: string
  active: boolean
  expires_at?: number
  granted_by?: string
  granted_at?: number
  revoked_at?: number
  revoked_by?: string
  orphaned_at?: number
  revocable: boolean
  read_only?: boolean
  session_owner?: string
  session_incarnation?: string
  permissions: string[]
  snapshot?: string[]
  new_permissions?: string[]
  note?: string
  last_used_at?: number
  use_count: number
}

export interface AuthDerived {
  id: string
  subject: string
  source: string
  read_only: boolean
  permissions: string[]
}

export interface AuthGrantsResp {
  version: number
  // 调用方显示名（id → 名称，仅含配了 name 的调用方）。
  caller_names?: Record<string, string>
  roots?: string[]
  grants: AuthGrant[]
  derived: AuthDerived[]
}

export interface AuthWhoami {
  caller_id: string
  // 调用方显示名（callers[].name）；没配时缺省，显示 id。
  caller_name?: string
  // 全部已命名调用方的 id → 名称，用来给界面里出现的 caller id 配名称。
  caller_names?: Record<string, string>
  kind: string
  root: boolean
  permissions: { name: string; source: string; grant_id?: string }[]
  session_id?: string
  grants_version: number
  roots?: string[]
  hint?: string
}

export interface AuthAuditEntry {
  at: number
  actor?: string
  action: string
  subject?: string
  permission?: string
  session_id?: string
  grant_id?: string
  method?: string
  path?: string
  status?: number
  reason?: string
}

export const AUTH_MANAGE = 'auth.manage'

export function parseSubject(subject: string): { kind: SubjectKind; id: string } | null {
  const s = subject.trim()
  if (s === 'users') return { kind: 'users', id: '' }
  if (s.startsWith('caller:') && s.slice(7).trim()) return { kind: 'caller', id: s.slice(7).trim() }
  if (s.startsWith('session:') && s.slice(8).trim()) return { kind: 'session', id: s.slice(8).trim() }
  return null
}

export function subjectLabel(subject: string, names?: CallerNames): string {
  const p = parseSubject(subject)
  if (!p) return subject
  if (p.kind === 'users') return '全体用户'
  if (p.kind === 'caller') return `调用方 ${callerLabel(p.id, names)}`
  return `会话 ${p.id}`
}

// GET /v1/auth/expand 的回答（服务端 internal/authz.Explain 的视图）。
export interface ExpandPerm {
  name: string
  description: string
  risk: PermRisk | string
  risk_label: string
  session_grantable: boolean
}

export type ExpandReason = 'root_only' | 'dangerous_wildcard' | 'not_session_grantable'

export interface ExpandExcluded {
  name: string
  risk: string
  reason: ExpandReason | string
}

export interface ExpandPreview {
  pattern: string
  subject: string
  kind: string
  permissions: ExpandPerm[]
  excluded: ExpandExcluded[]
  // 只在展开后什么都不剩（HTTP 400）时有。
  error?: string
  detail?: string
  problem?: string
}

// 400 的 body 是不是预览形状（而不是普通 {error, detail}）。
export function isExpandPreview(body: unknown): body is ExpandPreview {
  if (typeof body !== 'object' || body === null) return false
  const b = body as Record<string, unknown>
  return typeof b.problem === 'string' && Array.isArray(b.permissions) && Array.isArray(b.excluded)
}

// empty / loading / unavailable 是界面自己的状态；其余来自服务端的 problem。
export type ExpandProblem =
  | 'empty'
  | 'loading'
  | 'unavailable'
  | 'no_match'
  | 'only_root'
  | 'only_dangerous_wildcard'
  | 'only_not_session_grantable'

export interface ExpandResult {
  pattern: string
  permissions: ExpandPerm[]
  // 通配符够得着但被服务端规则挡住的：危险级（须字面写出）、不可授予会话。
  skippedDangerous: string[]
  skippedSession: string[]
  // 匹配到 auth.manage（永远不授予）。
  skippedRoot: boolean
  problem?: ExpandProblem
  // problem=unavailable 时的原因（请求失败的文案）。
  message?: string
}

export function emptyExpandResult(problem?: ExpandProblem, pattern = '', message?: string): ExpandResult {
  return { pattern, permissions: [], skippedDangerous: [], skippedSession: [], skippedRoot: false, problem, ...(message ? { message } : {}) }
}

// 预设可能带多个模式（用逗号 / 空白分隔写在同一输入框）；一个模式一次预览，
// 提交时一个模式一条授权（存的仍是模式）。
export function splitPatterns(text: string): string[] {
  return text.split(/[\s,]+/).filter(Boolean)
}

// 把每个模式的预览合并成界面要用的一份：权限去重保序，被挡下的按原因归类。
// 任何一个模式什么都没剩，保存时服务端会 400；这里把它的 problem 带出来提前拦住。
export function mergeExpandPreviews(previews: ExpandPreview[]): ExpandResult {
  if (previews.length === 0) return emptyExpandResult('empty')
  const out = emptyExpandResult(undefined, previews.map((p) => p.pattern).join(','))
  const seen = new Set<string>()
  for (const pv of previews) {
    for (const p of pv.permissions) {
      if (seen.has(p.name)) continue
      seen.add(p.name)
      out.permissions.push(p)
    }
    for (const e of pv.excluded) {
      if (e.reason === 'dangerous_wildcard') out.skippedDangerous.push(e.name)
      else if (e.reason === 'not_session_grantable') out.skippedSession.push(e.name)
      else if (e.reason === 'root_only') out.skippedRoot = true
    }
  }
  const bad = previews.find((p) => p.problem)
  if (bad) out.problem = bad.problem as ExpandProblem
  return out
}

export function expandProblemText(p: ExpandProblem | string | undefined, message?: string): string {
  switch (p) {
    case 'empty':
      return '填写授权模式，例如 tunnel.*'
    case 'loading':
      return '正在预览…'
    case 'unavailable':
      return message ? `预览失败：${message}` : '预览失败，稍后再试'
    case 'no_match':
      return '这个模式没有匹配任何权限'
    case 'only_root':
      return 'auth.manage 只属于根管理员，任何模式都不会授予'
    case 'only_dangerous_wildcard':
      return '通配符不会授予危险级权限，请写出完整权限名'
    case 'only_not_session_grantable':
      return '会话授权只能包含可授予会话的权限'
    default:
      return ''
  }
}

export interface GrantConfirm {
  high: string[]
  dangerous: string[]
  // 高级别：提交前要多一步确认。
  needsConfirm: boolean
  // 危险级（必然是字面写出的）：还要填原因。
  needsReason: boolean
}

export function grantConfirmRules(exp: ExpandResult): GrantConfirm {
  const high = exp.permissions.filter((p) => p.risk === 'high').map((p) => p.name)
  const dangerous = exp.permissions.filter((p) => p.risk === 'dangerous').map((p) => p.name)
  return { high, dangerous, needsConfirm: high.length > 0 || dangerous.length > 0, needsReason: dangerous.length > 0 }
}

export interface GrantFormState {
  subject: string
  pattern: string
  note: string
  confirmed: boolean
}

// 表单能否提交：模式有效；高级别已确认；危险级有原因。
export function grantFormBlocker(form: GrantFormState, exp: ExpandResult): string {
  if (!parseSubject(form.subject)) return '选择授权对象'
  if (exp.problem) return expandProblemText(exp.problem, exp.message)
  const rules = grantConfirmRules(exp)
  if (rules.needsReason && !form.note.trim()) return '危险级权限需要填写原因'
  if (rules.needsConfirm && !form.confirmed) return '确认这些高风险权限'
  return ''
}

export interface ManageMode {
  // 能写授权（是根管理员）。
  canManage: boolean
  readOnly: boolean
  // 该页签调 /v1/auth/grants|audit 之前要不要先跳过（没权限调只会 403）。
  canQueryGrants: boolean
  reason: string
}

// 只读判定：没有根管理员的部署谁都不能授权；有根管理员但自己不是，也只能看目录。
export function manageMode(who: AuthWhoami | null | undefined): ManageMode {
  if (!who) return { canManage: false, readOnly: true, canQueryGrants: false, reason: '' }
  if (who.kind !== 'user') {
    return { canManage: false, readOnly: true, canQueryGrants: false, reason: '当前凭据不是个人账号，权限由它自己的允许范围决定。' }
  }
  const holdsManage = who.root || who.permissions.some((p) => p.name === AUTH_MANAGE)
  if (holdsManage) return { canManage: true, readOnly: false, canQueryGrants: true, reason: '' }
  const roots = who.roots ?? []
  if (roots.length === 0) {
    return {
      canManage: false,
      readOnly: true,
      canQueryGrants: false,
      reason: '这个部署没有根管理员，没人能授权。在配置里给一个调用方写 can_admin: true 后重载。',
    }
  }
  return {
    canManage: false,
    readOnly: true,
    canQueryGrants: false,
    reason: `只有根管理员能管理授权：${roots.join('、')}。你可以查看权限目录和自己的权限。`,
  }
}

// 「有新权限待确认」：授权仍生效，且模式如今比保存时多展开了权限。
export function pendingNewPermissions(g: Pick<AuthGrant, 'active' | 'new_permissions' | 'state'>): string[] {
  if (!g.active && g.state !== 'active') return []
  return g.new_permissions ?? []
}

export interface SubjectGroup {
  subject: string
  label: string
  grants: AuthGrant[]
  derived: AuthDerived[]
  pending: number
  orphaned: number
}

function subjectRank(subject: string): number {
  return subject === 'users' ? 0 : 1
}

// 授权页签：按 users / caller:<id> 分组；session:* 另在会话授权页签。
export function groupGrantsBySubject(grants: AuthGrant[], derived: AuthDerived[], names?: CallerNames): SubjectGroup[] {
  const map = new Map<string, SubjectGroup>()
  const at = (subject: string): SubjectGroup => {
    let g = map.get(subject)
    if (!g) {
      g = { subject, label: subjectLabel(subject, names), grants: [], derived: [], pending: 0, orphaned: 0 }
      map.set(subject, g)
    }
    return g
  }
  for (const d of derived) {
    if (parseSubject(d.subject)?.kind === 'session') continue
    at(d.subject).derived.push(d)
  }
  for (const gr of grants) {
    if (parseSubject(gr.subject)?.kind === 'session') continue
    const g = at(gr.subject)
    g.grants.push(gr)
    if (pendingNewPermissions(gr).length > 0) g.pending++
    if (gr.state === 'orphaned') g.orphaned++
  }
  return [...map.values()].sort((a, b) => subjectRank(a.subject) - subjectRank(b.subject) || a.subject.localeCompare(b.subject))
}

export function sessionGrants(grants: AuthGrant[]): AuthGrant[] {
  return grants
    .filter((g) => parseSubject(g.subject)?.kind === 'session')
    .sort((a, b) => Number(b.active) - Number(a.active) || (b.granted_at ?? 0) - (a.granted_at ?? 0))
}

const STATE_LABELS: Record<string, string> = {
  active: '生效中',
  orphaned: '孤儿',
  revoked: '已撤销',
  expired: '已过期',
  unmatched: '模式失效',
}

export function grantStateLabel(state: string): string {
  return STATE_LABELS[state] ?? state
}

export function grantStateHint(g: Pick<AuthGrant, 'state' | 'subject'>): string {
  switch (g.state) {
    case 'orphaned':
      return '这个调用方已不在配置里，授权暂停。调用方回来后需确认才恢复。'
    case 'unmatched':
      return '模式在当前目录里已没有可授予的权限，不再生效。'
    default:
      return ''
  }
}

// 孤儿授权可确认恢复；已撤销的不行。
export function canActivate(g: Pick<AuthGrant, 'state' | 'source'>): boolean {
  return g.source === 'db' && g.state === 'orphaned'
}

export function canRevoke(g: Pick<AuthGrant, 'revocable' | 'read_only' | 'source'>): boolean {
  return g.revocable && !g.read_only && g.source === 'db'
}

export const AUDIT_ACTION_LABELS: Record<string, string> = {
  'grant.create': '新增授权',
  'grant.revoke': '撤销授权',
  'grant.activate': '确认恢复',
  'request.session_grant': '会话授权被使用',
  'request.denied': '请求被拒',
}

export function auditActionLabel(a: string): string {
  return AUDIT_ACTION_LABELS[a] ?? a
}

export interface AuditFilter {
  subject: string
  permission: string
  grant_id: string
  limit?: number
}

export function auditQuery(f: AuditFilter): string {
  const q = new URLSearchParams()
  if (f.subject.trim()) q.set('subject', f.subject.trim())
  if (f.permission.trim()) q.set('permission', f.permission.trim())
  if (f.grant_id.trim()) q.set('grant_id', f.grant_id.trim())
  q.set('limit', String(f.limit ?? 100))
  return q.toString()
}

// 服务端按最新在前返回；保险起见客户端再排一次（同秒保持原序）。
export function auditNewestFirst(entries: AuthAuditEntry[]): AuthAuditEntry[] {
  return entries
    .map((e, i) => ({ e, i }))
    .sort((a, b) => b.e.at - a.e.at || a.i - b.i)
    .map((x) => x.e)
}

export const RISK_ORDER: Record<string, number> = { low: 0, medium: 1, high: 2, dangerous: 3, root: 4 }

// 风险档在界面上的色调，对应 tokens：低=queue、中=phosphor、高=run、危险/根=fail。
export function riskTone(risk: string): 'low' | 'medium' | 'high' | 'danger' {
  if (risk === 'dangerous' || risk === 'root') return 'danger'
  if (risk === 'high' || risk === 'medium' || risk === 'low') return risk
  return 'low'
}

export interface CatalogFilter {
  text: string
  risk: string
  sessionOnly: boolean
}

export function filterCatalog(list: CatalogPermission[], f: CatalogFilter): CatalogPermission[] {
  const t = f.text.trim().toLowerCase()
  return list.filter((p) => {
    if (f.risk && p.risk !== f.risk) return false
    if (f.sessionOnly && !p.session_grantable) return false
    if (!t) return true
    return p.name.toLowerCase().includes(t) || p.description.toLowerCase().includes(t)
  })
}

// 服务端 403 的 error 形如 "permission required: tunnel.open, job.submit"。
export function missingPermissions(error: string | undefined): string[] {
  const prefix = 'permission required:'
  const e = (error ?? '').trim()
  if (!e.toLowerCase().startsWith(prefix)) return []
  return e
    .slice(prefix.length)
    .split(',')
    .map((s) => s.trim())
    .filter(Boolean)
}

// 403 的展示文案：缺的权限名 + 去哪里处理。非权限类 403 返回 null（沿用原文案）。
export function permissionDeniedMessage(status: number, error: string | undefined): string | null {
  if (status !== 403) return null
  const miss = missingPermissions(error)
  if (miss.length === 0) return null
  return `缺少权限：${miss.join('、')}。请根管理员在「设置 → 权限」授予；在那里也能查看你现有的权限。`
}

// 授权有效期（新增授权表单）。
export const EXPIRY_OPTIONS: { label: string; sec: number }[] = [
  { label: '长期', sec: 0 },
  { label: '1 天', sec: 86400 },
  { label: '7 天', sec: 7 * 86400 },
  { label: '30 天', sec: 30 * 86400 },
]
