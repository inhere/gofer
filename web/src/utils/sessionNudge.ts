// 会话催办（N2 §E，SESS-12）的展示与输入口径：纯函数，便于单测。
import type { SessionNudge, SessionNudgeKind } from '../api/types'

export const NUDGE_MIN_MINUTES = 1

// 间隔秒数 → "30 分钟" / "2 小时" / "1 小时 30 分钟"。
export function nudgeIntervalLabel(sec: number): string {
  const m = Math.round(sec / 60)
  if (m < 60) return `${m} 分钟`
  const h = Math.floor(m / 60)
  const rest = m % 60
  return rest === 0 ? `${h} 小时` : `${h} 小时 ${rest} 分钟`
}

export function nudgeRuleLabel(kind: SessionNudgeKind, sec: number): string {
  return kind === 'stalled' ? `停滞超过 ${nudgeIntervalLabel(sec)}` : `每 ${nudgeIntervalLabel(sec)}`
}

// 表单分钟数 → 秒；非法（空 / 小于 1 分钟 / NaN）返回 null。
export function nudgeMinutesToSec(minutes: number | string): number | null {
  const n = typeof minutes === 'string' ? Number(minutes.trim()) : minutes
  if (!Number.isFinite(n) || n < NUDGE_MIN_MINUTES) return null
  return Math.round(n * 60)
}

// <input type="datetime-local"> 的值（本地时间）→ unix 秒；空串 = 不设截止（0）；无法解析 = null。
export function nudgeUntilToUnix(local: string): number | null {
  const v = local.trim()
  if (!v) return 0
  const t = new Date(v).getTime()
  return Number.isNaN(t) ? null : Math.floor(t / 1000)
}

// 只有活着的会话能新建催办（已结束 / 已接管的会话没有可送达的对象）。
export function nudgeCanCreate(state: string | undefined): boolean {
  return !!state && state !== 'ended' && state !== 'handed_off'
}

const ENDED_REASON: Record<string, string> = {
  until: '已到截止时间',
  session_ended: '会话已结束',
  session_handed_off: '会话已被接管',
  removed: '已删除',
}

// 状态一行：停用 / 结束原因、连续失败。
export function nudgeStatusText(n: SessionNudge): string {
  if (n.state === 'ended') return `已结束 · ${ENDED_REASON[n.ended_reason ?? ''] ?? n.ended_reason ?? ''}`.replace(/ · $/, '')
  if (n.state === 'paused') return `已暂停${n.pause_reason ? ' · ' + n.pause_reason : ''}`
  return n.fail_count > 0 ? `运行中 · 连续失败 ${n.fail_count}/3` : '运行中'
}
