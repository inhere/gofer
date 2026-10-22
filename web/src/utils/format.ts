// 统一的格式化（设计稿 §9.1）：时间、相对时间、时长、数字都走 Intl，替换页面里散落的
// toLocaleString 和几套手写时长换算。locale 默认取当前界面语言（store/locale.ts），
// 测试里可以显式传 'en-US' / 'zh-CN'。
import { locale, type Locale } from '../store/locale'

// 服务端时间多是 ISO 字符串；也接受毫秒时间戳与 Date。
export function toDate(input: Date | string | number | null | undefined): Date | null {
  if (input === null || input === undefined || input === '') return null
  if (input instanceof Date) return Number.isNaN(input.getTime()) ? null : input
  if (typeof input === 'number') {
    // 10 位的是秒级时间戳，统一换算成毫秒
    const ms = input < 1e12 ? input * 1000 : input
    const d = new Date(ms)
    return Number.isNaN(d.getTime()) ? null : d
  }
  const d = new Date(/^\d+$/.test(input) ? Number(input) : input)
  return Number.isNaN(d.getTime()) ? null : d
}

export function formatDateTime(
  input: Date | string | number | null | undefined,
  loc: Locale = locale.value,
): string {
  const d = toDate(input)
  if (!d) return ''
  return new Intl.DateTimeFormat(loc, { dateStyle: 'medium', timeStyle: 'short' }).format(d)
}

export function formatDate(
  input: Date | string | number | null | undefined,
  loc: Locale = locale.value,
): string {
  const d = toDate(input)
  if (!d) return ''
  return new Intl.DateTimeFormat(loc, { dateStyle: 'medium' }).format(d)
}

export function formatTime(
  input: Date | string | number | null | undefined,
  loc: Locale = locale.value,
): string {
  const d = toDate(input)
  if (!d) return ''
  return new Intl.DateTimeFormat(loc, { timeStyle: 'short' }).format(d)
}

// 相对时间（「3 分钟前」/「3 minutes ago」）：按秒/分/时/天/月/年选最合适的一档，
// numeric: 'auto' 让 Intl 自己给「昨天」「yesterday」这类词。
export function formatRelative(
  input: Date | string | number | null | undefined,
  now: Date | number = Date.now(),
  loc: Locale = locale.value,
): string {
  const d = toDate(input)
  const ref = toDate(now)
  if (!d || !ref) return ''
  const diffSec = Math.round((d.getTime() - ref.getTime()) / 1000)
  const abs = Math.abs(diffSec)
  const rtf = new Intl.RelativeTimeFormat(loc, { numeric: 'auto' })
  if (abs < 45) return rtf.format(diffSec, 'second')
  const diffMin = Math.round(diffSec / 60)
  if (Math.abs(diffMin) < 45) return rtf.format(diffMin, 'minute')
  const diffHour = Math.round(diffMin / 60)
  if (Math.abs(diffHour) < 22) return rtf.format(diffHour, 'hour')
  const diffDay = Math.round(diffHour / 24)
  if (Math.abs(diffDay) < 26) return rtf.format(diffDay, 'day')
  const diffMonth = Math.round(diffDay / 30)
  if (Math.abs(diffMonth) < 11) return rtf.format(diffMonth, 'month')
  return rtf.format(Math.round(diffMonth / 12), 'year')
}

export function formatNumber(
  n: number | null | undefined,
  loc: Locale = locale.value,
  opts: Intl.NumberFormatOptions = {},
): string {
  if (n === null || n === undefined || !Number.isFinite(n)) return ''
  return new Intl.NumberFormat(loc, opts).format(n)
}

export function formatPercent(
  ratio: number | null | undefined,
  loc: Locale = locale.value,
  fractionDigits = 0,
): string {
  if (ratio === null || ratio === undefined || !Number.isFinite(ratio)) return ''
  return new Intl.NumberFormat(loc, {
    style: 'percent',
    minimumFractionDigits: fractionDigits,
    maximumFractionDigits: fractionDigits,
  }).format(ratio)
}

// 时长：给 job 耗时、会话时长、指标用。单位字母与 locale 无关（h/m/s/ms），
// 在等宽字体里不会跳字；超过 1 天按 h 继续累计。
export function formatDuration(ms: number | null | undefined): string {
  if (ms === null || ms === undefined || !Number.isFinite(ms) || ms < 0) return ''
  if (ms < 1000) return `${Math.round(ms)}ms`
  const totalSec = ms / 1000
  if (totalSec < 60) {
    return `${totalSec < 10 ? totalSec.toFixed(1).replace(/\.0$/, '') : Math.round(totalSec)}s`
  }
  const totalMin = Math.floor(totalSec / 60)
  const sec = Math.round(totalSec - totalMin * 60)
  if (totalMin < 60) {
    return sec ? `${totalMin}m ${String(sec).padStart(2, '0')}s` : `${totalMin}m`
  }
  const totalHour = Math.floor(totalMin / 60)
  const min = totalMin - totalHour * 60
  return min ? `${totalHour}h ${String(min).padStart(2, '0')}m` : `${totalHour}h`
}
