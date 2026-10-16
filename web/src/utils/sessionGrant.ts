// Sessions 页「授权」：给一个终端会话临时授权的纯逻辑——可选权限、有效期、请求体、
// 「已授权」徽标的判定与威胁说明。展开规则不在这里（预览调 GET /v1/auth/expand）。
import { parseSubject } from './permissions'
import type { AuthGrant, CatalogPermission } from './permissions'
import type { CreateGrantReq } from '../api/permissions'

// 会话授权的有效期选项。服务端要求会话授权必带 expires_sec，上限 86400（24 小时）；
// 会话结束时授权本来就失效，所以没有单独的「会话结束」选项，写在说明里。
export const SESSION_GRANT_EXPIRY_OPTIONS: { label: string; sec: number }[] = [
  { label: '1 小时', sec: 3600 },
  { label: '4 小时', sec: 14400 },
  { label: '24 小时', sec: 86400 },
]
export const SESSION_GRANT_DEFAULT_EXPIRY_SEC = 3600
export const SESSION_GRANT_MAX_SEC = 86400

// 威胁模型（设计「四、会话授权」）：授权跟着终端会话，而不是跟着人。
export const SESSION_GRANT_THREAT_NOTE =
  '这条授权属于「终端会话」，不属于某个人：只要能在这个终端会话里运行命令——你自己、会话里的 agent、' +
  '它启动的任何进程——在授权有效期内都拥有下面这些权限。只勾这次确实要用的。'

export const SESSION_GRANT_ENDS_NOTE =
  '会话结束、被唤醒复活、被接管或转给别人后，授权立即失效，不会带到新的会话；最长 24 小时，到期自动失效；随时可以在会话抽屉里撤销。'

export function sessionSubject(sid: string): string {
  return `session:${sid}`
}

// 弹窗里只列可授予会话的权限：目录里 session_grantable 为真，且不是根级（保险，服务端本来就拒）。
export function sessionGrantable(catalog: CatalogPermission[]): CatalogPermission[] {
  return catalog.filter((p) => p.session_grantable && p.risk !== 'root' && p.name !== 'auth.manage')
}

// 选中的权限名，按目录顺序去重，且只保留可授予会话的（目录里没有的名字丢弃）。
export function orderedSelection(selected: Iterable<string>, grantable: CatalogPermission[]): string[] {
  const want = new Set(selected)
  return grantable.filter((p) => want.has(p.name)).map((p) => p.name)
}

// 选中的权限拼成预览输入框的模式文本（每个权限名就是一个字面模式）。
export function selectionPatternText(names: string[]): string {
  return names.join(', ')
}

// 预设里整套都可授予会话的才给快捷填入（否则点了一半被丢，行为让人意外）。
export function sessionPresets<T extends { permissions: string[] }>(presets: T[], grantable: CatalogPermission[]): T[] {
  const ok = new Set(grantable.map((p) => p.name))
  return presets.filter((p) => p.permissions.length > 0 && p.permissions.every((n) => ok.has(n)))
}

// 一次提交的请求体：一个权限一条授权（存的是模式），同一有效期、同一原因。
export function buildSessionGrantRequests(sid: string, names: string[], expirySec: number, note: string): CreateGrantReq[] {
  const sec = Math.min(Math.max(Math.trunc(expirySec), 1), SESSION_GRANT_MAX_SEC)
  const n = note.trim()
  return names.map((pattern) => ({
    subject: sessionSubject(sid),
    pattern,
    expires_sec: sec,
    ...(n ? { note: n } : {}),
  }))
}

export interface SessionGrantBadge {
  granted: boolean
  grants: AuthGrant[]
  // 生效中的权限名（去重，保序）。
  permissions: string[]
  // 最早到期的那条（unix 秒），用于「至 …」；0 = 无。
  expiresAt: number
}

export interface SessionGrantTarget {
  session_id: string
  state?: string
  // 会话当前 owner：与授权记录的 owner 不一致就不是同一份托付。
  caller_id?: string
}

// 这条授权现在对这个会话算生效吗（客户端能看到的部分）：
// 属于该会话、授权仍 active、未过期、会话没结束、owner 仍是授权时的 owner。
// 化身（incarnation）接口没给 web，会话结束后复活的那一类由服务端判定，列表里它仍显示 active——
// 所以徽标写「已授权」而不是「正在生效」，撤销入口始终可用。
export function isEffectiveSessionGrant(g: AuthGrant, s: SessionGrantTarget, nowSec: number): boolean {
  const p = parseSubject(g.subject)
  if (p?.kind !== 'session' || p.id !== s.session_id) return false
  if (!g.active || g.state !== 'active') return false
  if (g.expires_at && g.expires_at <= nowSec) return false
  if (s.state === 'ended') return false
  if (g.session_owner && s.caller_id && g.session_owner !== s.caller_id) return false
  return true
}

export function sessionGrantBadge(grants: AuthGrant[], s: SessionGrantTarget, nowSec: number): SessionGrantBadge {
  const mine = grants.filter((g) => isEffectiveSessionGrant(g, s, nowSec))
  const names: string[] = []
  for (const g of mine) for (const n of g.permissions) if (!names.includes(n)) names.push(n)
  const exps = mine.map((g) => g.expires_at ?? 0).filter((x) => x > 0)
  return { granted: mine.length > 0, grants: mine, permissions: names, expiresAt: exps.length ? Math.min(...exps) : 0 }
}

// 抽屉里能点「授权」吗：根管理员，且会话没结束（服务端对已结束会话回 409）。
export function canGrantSession(isManager: boolean, s: Pick<SessionGrantTarget, 'state'> | null | undefined): boolean {
  return isManager && !!s && s.state !== 'ended'
}

export function badgeTitle(b: SessionGrantBadge, fmt: (sec: number) => string): string {
  if (!b.granted) return ''
  const tail = b.expiresAt ? `；最早 ${fmt(b.expiresAt)} 到期` : ''
  return `这个会话持有授权：${b.permissions.join('、') || '—'}${tail}。会话结束即失效。`
}
