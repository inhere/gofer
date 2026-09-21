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

// 服务端本地时区偏移（秒，bd h-aii-tnua）：由 /v1/stats 的 server_tz_offset_sec 填一次。
// 时间戳仍是 Unix 秒；这里只是用服务端那一侧的时钟渲染——schedule/wakeup 何时触发，
// 服务端说了算，浏览器在别的时区时不该看到另一个时刻。拿不到（老 server / 未拉到）就
// 退回浏览器本地时区，行为与本改动前一致。
let serverTZOffsetSec: number | null = null

export function setServerTZOffset(sec: number | null | undefined): void {
  serverTZOffsetSec = typeof sec === 'number' && Number.isFinite(sec) ? sec : null
}

// 偏移后缀：+08:00 / -05:30（带符号，HH:MM）。
function tzSuffix(off: number): string {
  const sign = off < 0 ? '-' : '+'
  const abs = Math.abs(off)
  const p = (x: number): string => String(x).padStart(2, '0')
  return `${sign}${p(Math.floor(abs / 3600))}:${p(Math.floor((abs % 3600) / 60))}`
}

// 绝对时间：Unix 秒 -> "MM-DD HH:mm:ss"（服务端时区，带 +08:00 后缀；拿不到时区退回
// 浏览器本地且不加后缀）。0/空/非法返回 —。
export function fmtDateTime(sec: number | null | undefined): string {
  const n = toUnixSec(sec ?? null)
  if (n == null) {
    return '—'
  }
  const p = (x: number): string => String(x).padStart(2, '0')
  if (serverTZOffsetSec == null) {
    const d = new Date(n * 1000)
    return `${p(d.getMonth() + 1)}-${p(d.getDate())} ${p(d.getHours())}:${p(d.getMinutes())}:${p(d.getSeconds())}`
  }
  // 先平移到服务端时区，再按 UTC 读字段：结果与浏览器的本地时区无关。
  const d = new Date((n + serverTZOffsetSec) * 1000)
  return `${p(d.getUTCMonth() + 1)}-${p(d.getUTCDate())} ${p(d.getUTCHours())}:${p(d.getUTCMinutes())}:${p(d.getUTCSeconds())} ${tzSuffix(serverTZOffsetSec)}`
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

// 相对时间（过去）：Unix 秒 -> "刚刚 / 12s前 / 3m前 / 2h前 / 5d前"（0/空/非法返回 —）。
export function fmtAgo(sec: number | null | undefined, nowSec?: number): string {
  const n = toUnixSec(sec ?? null)
  if (n == null) {
    return '—'
  }
  const d = (nowSec ?? Math.floor(Date.now() / 1000)) - n
  if (d < 5) {
    return '刚刚'
  }
  if (d < 60) {
    return `${d}s前`
  }
  if (d < 3600) {
    return `${Math.floor(d / 60)}m前`
  }
  if (d < 86400) {
    return `${Math.floor(d / 3600)}h前`
  }
  return `${Math.floor(d / 86400)}d前`
}
