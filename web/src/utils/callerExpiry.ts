// 调用方 token 的到期 / 最近使用展示（设计 2026-11-14 §4，plan R9）：列表要能一眼看出
// 「这个 token 什么时候不能用」「这台机器还在用吗」。两个判定都是纯函数，单测直接覆盖。
//
// expires_at / last_used_at 都是 unix 秒；0 或缺省分别表示「永不过期」「从未使用」。
// 配置文件里声明的调用方（source=file）没有到期（hash 要等 P3 才入库）。
import { fmtDateTime } from '../api/time'
import type { CallerView } from '../api/types'
import { t } from '../i18n'

// STALE_DAYS 是「标灰」的阈值：超过 30 天没用的 token 看起来像废票，方便淘汰。
export const STALE_DAYS = 30
const DAY_SEC = 86400

// expiresState：never（永不过期）/ expired（已过期）/ soon（7 天内到期）/ ok。
// 7 天与后端发卡窗口（internal/inbox/caller_token.go 的 CallerTokenWarnWindow）一致。
export type ExpiresState = 'never' | 'expired' | 'soon' | 'ok'

export function expiresState(expiresAt: number | undefined, nowSec: number): ExpiresState {
  if (!expiresAt) return 'never'
  if (expiresAt <= nowSec) return 'expired'
  if (expiresAt - nowSec <= 7 * DAY_SEC) return 'soon'
  return 'ok'
}

// expiresText：表格里的一行文字（到期还要看具体时间，所以永远带上时刻）。
export function expiresText(expiresAt: number | undefined, nowSec: number): string {
  switch (expiresState(expiresAt, nowSec)) {
    case 'never':
      return t('settings.callers.expires.never')
    case 'expired':
      return t('settings.callers.expires.expired', { at: fmtDateTime(expiresAt ?? 0) })
    case 'soon':
      return t('settings.callers.expires.soon', { at: fmtDateTime(expiresAt ?? 0) })
    default:
      return fmtDateTime(expiresAt ?? 0)
  }
}

// expiresTone：tag 的样式族——过期是 fail，快到期是 warn，其余（含永不过期）中性。
export function expiresTone(expiresAt: number | undefined, nowSec: number): 'off' | 'warn' | 'ok' {
  const st = expiresState(expiresAt, nowSec)
  if (st === 'expired') return 'off'
  if (st === 'soon') return 'warn'
  return 'ok'
}

// lastUsedText：最近使用；超过 STALE_DAYS 天的按日期显示并标灰。
export function lastUsedText(lastUsedAt: number | undefined): string {
  if (!lastUsedAt) return t('settings.callers.lastUsed.never')
  return fmtDateTime(lastUsedAt)
}

// isStale：超过 STALE_DAYS 天没用过的行标灰。从未使用（last_used_at=0）也算陈旧——它从建好就
// 没人用过，正是该淘汰的那种；但如果 token 刚建（created_at 也新），30 天后自然变灰。
export function isStale(v: Pick<CallerView, 'last_used_at' | 'created_at'>, nowSec: number): boolean {
  const last = v.last_used_at || 0
  if (last > 0) return nowSec - last > STALE_DAYS * DAY_SEC
  const created = v.created_at || 0
  return created > 0 && nowSec - created > STALE_DAYS * DAY_SEC
}

// expiryFormFrom：轮换表单的初值——保留原到期（服务端不填就不改），所以默认空串。
export function expiryFormFrom(): string {
  return ''
}
