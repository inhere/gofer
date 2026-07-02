// 时间工具：started_at/ended_at 是 Unix 秒（types 里声明为 string，运行期为秒数字符串/数字）。

import type { Job } from './types'

// 安全转 Unix 秒数字；空/非法返回 null。
export function toUnixSec(v: string | number | undefined | null): number | null {
  if (v == null || v === '') {
    return null
  }
  const n = typeof v === 'number' ? v : Number(v)
  return Number.isFinite(n) && n > 0 ? n : null
}

// 任务耗时（秒）：已结束用 ended_at-started_at；运行中用 now-started_at；无 started 返回 null。
export function jobDurationSec(job: Pick<Job, 'started_at' | 'ended_at'>, nowSec?: number): number | null {
  const start = toUnixSec(job.started_at)
  if (start == null) {
    return null
  }
  const end = toUnixSec(job.ended_at) ?? nowSec ?? Math.floor(Date.now() / 1000)
  const d = end - start
  return d >= 0 ? d : 0
}

// 绝对时间：Unix 秒 -> 本地 "MM-DD HH:mm:ss"（0/空/非法返回 —）。
export function fmtDateTime(sec: number | null | undefined): string {
  const n = toUnixSec(sec ?? null)
  if (n == null) {
    return '—'
  }
  const d = new Date(n * 1000)
  const p = (x: number): string => String(x).padStart(2, '0')
  return `${p(d.getMonth() + 1)}-${p(d.getDate())} ${p(d.getHours())}:${p(d.getMinutes())}:${p(d.getSeconds())}`
}

// 距未来时间点的人类可读间隔（cron next_run 用）：已到点/过期 -> "待触发"，否则 "≈3m20s"。
export function fmtUntil(sec: number | null | undefined, nowSec?: number): string {
  const n = toUnixSec(sec ?? null)
  if (n == null) {
    return '—'
  }
  const d = n - (nowSec ?? Math.floor(Date.now() / 1000))
  return d <= 0 ? '待触发' : `≈${fmtDuration(d)}`
}

// 人类可读耗时：12s / 3m20s / 1h05m。
export function fmtDuration(sec: number | null): string {
  if (sec == null) {
    return '—'
  }
  const s = Math.floor(sec)
  if (s < 60) {
    return `${s}s`
  }
  if (s < 3600) {
    const m = Math.floor(s / 60)
    const r = s % 60
    return r ? `${m}m${String(r).padStart(2, '0')}s` : `${m}m`
  }
  const h = Math.floor(s / 3600)
  const m = Math.floor((s % 3600) / 60)
  return `${h}h${String(m).padStart(2, '0')}m`
}
