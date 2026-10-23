// 会话催办（N2 §E，SESS-12）的展示与输入口径：纯函数，便于单测。
import type { SessionNudge, SessionNudgeKind } from '../api/types'
import { t, te } from '../i18n'

export const NUDGE_MIN_MINUTES = 1

// 间隔秒数 → "30 分钟" / "2 小时" / "1 小时 30 分钟"。
export function nudgeIntervalLabel(sec: number): string {
  const m = Math.round(sec / 60)
  if (m < 60) return t('util.nudge.minutes', { n: m })
  const h = Math.floor(m / 60)
  const rest = m % 60
  return rest === 0 ? t('util.nudge.hours', { n: h }) : t('util.nudge.hoursMinutes', { h, m: rest })
}

export function nudgeRuleLabel(kind: SessionNudgeKind, sec: number): string {
  return kind === 'stalled'
    ? t('util.nudge.ruleStalled', { d: nudgeIntervalLabel(sec) })
    : t('util.nudge.ruleEvery', { d: nudgeIntervalLabel(sec) })
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

// 结束原因：识别得出的给一句人话（字典键），认不得的原样返回。
function endedReason(reason: string | undefined): string {
  if (!reason) return ''
  const key = `util.nudge.ended.${reason}`
  return te(key) ? t(key) : reason
}

// 状态一行：停用 / 结束原因、连续失败。
export function nudgeStatusText(n: SessionNudge): string {
  if (n.state === 'ended') {
    const r = endedReason(n.ended_reason)
    return r ? t('util.nudge.ended', { reason: r }) : t('util.nudge.endedNoReason')
  }
  if (n.state === 'paused') {
    return n.pause_reason ? t('util.nudge.paused', { reason: n.pause_reason }) : t('util.nudge.pausedNoReason')
  }
  return n.fail_count > 0 ? t('util.nudge.runningFailing', { n: n.fail_count }) : t('util.nudge.running')
}
