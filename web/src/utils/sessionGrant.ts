// Sessions 页「授权」：给一个终端会话临时授权的纯逻辑——可选权限、有效期、请求体、
// 「已授权」徽标的判定与威胁说明。展开规则不在这里（预览调 GET /v1/auth/expand）。
import { t } from '../i18n'
import { parseSubject } from './permissions'
import type { AuthGrant, CatalogPermission } from './permissions'
import type { CreateGrantReq } from '../api/permissions'

// 会话授权的有效期选项。服务端要求会话授权必带 expires_sec，上限 86400（24 小时）；
// 会话结束时授权本来就失效，所以没有单独的「会话结束」选项，写在说明里。
// label 用 getter：数组本身是模块级常量（多处按下标取 sec、续期选项直接复用这一组），
// 但文案要跟当前语言，所以渲染时才取。
export const SESSION_GRANT_EXPIRY_OPTIONS: { label: string; sec: number }[] = [
  { get label() { return t('util.sessionGrant.expiry.hour1') }, sec: 3600 },
  { get label() { return t('util.sessionGrant.expiry.hour4') }, sec: 14400 },
  { get label() { return t('util.sessionGrant.expiry.hour24') }, sec: 86400 },
]
export const SESSION_GRANT_DEFAULT_EXPIRY_SEC = 3600
export const SESSION_GRANT_MAX_SEC = 86400

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

// 这条授权现在对这个会话算生效吗：以服务端给的 effective 为准（服务端按设计第四节全部条件判定，
// 含 web 看不到的会话化身）；只有字段缺失（旧服务端）时才退回客户端估计：
// 属于该会话、授权仍 active、未过期、会话没结束、owner 仍是授权时的 owner。
export function isEffectiveSessionGrant(g: AuthGrant, s: SessionGrantTarget, nowSec: number): boolean {
  const p = parseSubject(g.subject)
  if (p?.kind !== 'session' || p.id !== s.session_id) return false
  if (typeof g.effective === 'boolean') return g.effective
  if (!g.active || g.state !== 'active') return false
  if (g.expires_at && g.expires_at <= nowSec) return false
  if (s.state === 'ended') return false
  if (g.session_owner && s.caller_id && g.session_owner !== s.caller_id) return false
  return true
}

// 设置页「会话授权」列表里一条授权的生效标签：服务端 effective 为准，缺失时按 state / 有效期估计。
export function grantEffectiveLabel(g: AuthGrant, nowSec: number): { text: string; effective: boolean } {
  const eff =
    typeof g.effective === 'boolean'
      ? g.effective
      : g.active && g.state === 'active' && !(g.expires_at && g.expires_at <= nowSec)
  return { text: eff ? t('util.sessionGrant.effective.on') : t('util.sessionGrant.effective.off'), effective: eff }
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
  const tail = b.expiresAt ? t('util.sessionGrant.badge.earliest', { at: fmt(b.expiresAt) }) : ''
  return t('util.sessionGrant.badge.title', {
    perms: b.permissions.join(t('util.sep.list')) || '—',
    tail,
  })
}
