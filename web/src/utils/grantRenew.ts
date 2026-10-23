// 会话授权「续期」的纯逻辑：剩余时间文案、续期入口的可见性、请求体、失败文案。
// 接口 POST /v1/auth/grants/{id}/renew（body {expires_sec}，1–86400，需 auth.manage）只把
// 同一条授权的到期时间从「现在」往后挪：模式、展开结果、授权人、绑定的会话都不变，
// 所以续期既不会放宽范围也不会搬走授权。已撤销 / 已过期 / 会话已结束 / 归属或化身变了的
// 不能续期（服务端 409），要重新授权。界面接线见 views/settings/Permissions.vue 的
// 会话授权行与会话抽屉的「已授权」条，两处共用 components/GrantRenewDialog.vue。

import { t } from '../i18n'
import { parseSubject } from './permissions'
import type { AuthGrant } from './permissions'
import { SESSION_GRANT_EXPIRY_OPTIONS, grantEffectiveLabel } from './sessionGrant'
import type { SessionGrantBadge } from './sessionGrant'

// 续期的有效期选项：与「授权」完全同一组（1 / 4 / 24 小时），上限同样是 24 小时。
export const RENEW_EXPIRY_OPTIONS = SESSION_GRANT_EXPIRY_OPTIONS
export const RENEW_DEFAULT_EXPIRY_SEC = RENEW_EXPIRY_OPTIONS[0].sec
export const RENEW_MIN_SEC = 1
export const RENEW_MAX_SEC = 86400
// 会话抽屉的「已授权」条：剩余不足这个数才摆出剩余时间与续期入口，不然按钮天天挂着。
export const RENEW_SOON_SEC = 3600

// 剩余秒数（向下取整，已到期为 0）；没有到期时间返回 0。
export function grantRemainingSec(expiresAt: number | undefined, nowSec: number): number {
  if (!expiresAt || expiresAt <= 0) return 0
  return Math.max(0, Math.floor(expiresAt) - Math.floor(nowSec))
}

// 剩余时间文案；没有到期时间（会话授权一定有）返回空串。
export function grantRemainingText(expiresAt: number | undefined, nowSec: number): string {
  if (!expiresAt || expiresAt <= 0) return ''
  const s = grantRemainingSec(expiresAt, nowSec)
  if (s <= 0) return t('util.renew.expired')
  if (s < 60) return t('util.renew.remainingSec', { n: s })
  if (s < 3600) return t('util.renew.remainingMin', { n: Math.floor(s / 60) })
  const h = Math.floor(s / 3600)
  const m = Math.floor((s % 3600) / 60)
  return m > 0 ? t('util.renew.remainingHM', { h, m }) : t('util.renew.remainingH', { h })
}

// 未生效的原因：与服务端 409 的理由对齐（会话那半边 web 看不全，归成一句）。
export function renewBlockedReason(g: Pick<AuthGrant, 'state'>): string {
  switch (g.state) {
    case 'revoked':
      return t('util.perm.state.revoked')
    case 'expired':
      return t('util.perm.state.expired')
    case 'unmatched':
      return t('util.perm.hint.unmatched')
    case 'orphaned':
      return t('util.renew.blockedOrphaned')
    default:
      return t('util.renew.blockedGone')
  }
}

export interface GrantRenewState {
  canRenew: boolean
  remainingSec: number
  // 剩余不足 RENEW_SOON_SEC（抽屉按它决定要不要提醒）。
  soon: boolean
  // 不能续期时给人看的原因；能续期时为空串。
  reason: string
}

// 这条授权现在能续期吗：只有仍生效的会话授权才行。生效与否以服务端给的 effective 为准
// （缺失时退回 state / 有效期估计，口径与「生效中」标签同一个），与服务端续期前的判定一致。
export function grantRenewState(g: AuthGrant, nowSec: number): GrantRenewState {
  const remainingSec = grantRemainingSec(g.expires_at, nowSec)
  const soon = remainingSec > 0 && remainingSec < RENEW_SOON_SEC
  if (parseSubject(g.subject)?.kind !== 'session') {
    return { canRenew: false, remainingSec, soon: false, reason: t('util.renew.onlySession') }
  }
  if (!grantEffectiveLabel(g, nowSec).effective) {
    return { canRenew: false, remainingSec, soon, reason: t('util.renew.cannot', { reason: renewBlockedReason(g) }) }
  }
  return { canRenew: true, remainingSec, soon, reason: '' }
}

// 续期入口要摆出来吗：只有持有 auth.manage 的人（whoami 的 canManage）看得见，且这条确实能续期。
export function canRenewGrant(isManager: boolean, g: AuthGrant, nowSec: number): boolean {
  return isManager && grantRenewState(g, nowSec).canRenew
}

// 会话抽屉徽标上的提醒与续期入口：有生效授权，且最早到期的一条剩余不足 1 小时。
export function canRenewBadge(isManager: boolean, b: SessionGrantBadge, nowSec: number): boolean {
  return isManager && b.granted && b.expiresAt > 0 && grantRemainingSec(b.expiresAt, nowSec) < RENEW_SOON_SEC
}

// 徽标上的续期对象：这个会话生效中的每一条各自延（各自用自己的到期时间，范围都不变）。
export function badgeRenewIds(b: SessionGrantBadge): string[] {
  return b.grants.map((g) => g.id)
}

// 续期请求体：只带有效期，1–86400 在客户端先夹一次，别把 0 发出去白挨一个 400。
export function buildGrantRenewRequest(id: string, expiresSec: number): { id: string; expires_sec: number } {
  const sec = Math.min(Math.max(Math.trunc(expiresSec) || 0, RENEW_MIN_SEC), RENEW_MAX_SEC)
  return { id, expires_sec: sec }
}

// 4xx 的文案。message 来自 api/client.ts 的「error - detail」拼接，detail 里就写着是哪个条件
// 不满足（如 session s1 has ended: grant it again instead），原样带出来给操作员看。
export function renewFailureText(status: number, message: string): string {
  const m = message.trim()
  if (status === 403) return t('util.renew.errNoPerm', { msg: m })
  if (status === 409) return t('util.renew.errConflict', { msg: m })
  if (status === 400 || status === 404) return t('util.renew.errRejected', { msg: m })
  return m
}
