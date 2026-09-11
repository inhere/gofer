import type { Runner } from '../api/types'

const STALE_MS = 30_000

export function workerAgeMs(r: Runner, nowMs: number): number | null {
  if (!r.worker) return null
  if (r.worker.last_heartbeat > 0) return Math.max(0, nowMs - r.worker.last_heartbeat)
  return Math.max(0, r.worker.heartbeat_age_ms)
}

export function beatOf(r: Runner, nowMs: number): 'connected' | 'stale' | 'flatline' {
  if (r.status !== 'connected') return 'flatline'
  const age = workerAgeMs(r, nowMs)
  return age != null && age > STALE_MS ? 'stale' : 'connected'
}

export function workerStatusText(r: Runner, nowMs: number): string {
  if (r.status !== 'connected') return 'offline'
  const age = workerAgeMs(r, nowMs)
  return age != null && age > STALE_MS ? `no heartbeat ${Math.floor(age / 1000)}s` : 'connected'
}

export function fmtAge(ms: number | null): string {
  if (ms == null) return '—'
  const s = Math.floor(ms / 1000)
  if (s < 1) return 'just now'
  if (s < 60) return `${s}s ago`
  if (s < 3600) { const m = Math.floor(s / 60); const r = s % 60; return r ? `${m}m${String(r).padStart(2, '0')}s ago` : `${m}m ago` }
  const h = Math.floor(s / 3600); const m = Math.floor((s % 3600) / 60)
  return `${h}h${String(m).padStart(2, '0')}m ago`
}

export function fmtUptime(startedAtSec: number | undefined, nowMs: number): string {
  if (!startedAtSec || startedAtSec <= 0) return ''
  const s = Math.max(0, Math.floor(nowMs / 1000) - startedAtSec)
  if (s < 60) return `up ${s}s`
  if (s < 3600) return `up ${Math.floor(s / 60)}m`
  if (s < 86400) { const h = Math.floor(s / 3600); const m = Math.floor((s % 3600) / 60); return `up ${h}h${String(m).padStart(2, '0')}m` }
  return `up ${Math.floor(s / 86400)}d${Math.floor((s % 86400) / 3600)}h`
}
