import type { Runner, RunnersServerInfo, RunnerTool, WorkerUpgradeRecord } from '../api/types'
import { t } from '../i18n'

// server pings each worker every 15s (wshub.DefaultPingInterval) and the worker's reply refreshes
// last_heartbeat; two missed beats (2x) mean "no heartbeat". The age is recomputed from the local
// clock (Runners.vue ticks nowMs every second), the data itself is refreshed by the `runners` push.
export const SERVER_PING_MS = 15_000
export const STALE_MS = 2 * SERVER_PING_MS

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

// 远程升级的协议门槛（wsproto.UpgradeMinProtocolVersion）。
export const UPGRADE_MIN_PROTOCOL = 15

// 空串 = 可以升级；否则是按钮不可点的原因（直接显示给用户）。
// 网页只提供「用 server 自身二进制升级」，所以要求 worker 与 server 同 os/arch；
// 不同平台的 worker 用 CLI 传二进制：gofer worker upgrade <id> --file <二进制>。
export function upgradeBlockReason(r: Runner, server?: RunnersServerInfo): string {
  const w = r.worker
  if (r.status !== 'connected' || !w) return t('util.upgrade.offline')
  if (r.upgrade?.state === 'pending') return t('util.upgrade.inProgress')
  if ((w.protocol_version ?? 0) < UPGRADE_MIN_PROTOCOL) return t('util.upgrade.tooOld')
  if (!server) return t('util.upgrade.noServer')
  if (w.os !== server.os || w.arch !== server.arch) {
    return t('util.upgrade.platform', {
      os: w.os || '?',
      arch: w.arch || '?',
      serverOs: server.os,
      serverArch: server.arch,
      id: r.worker_id || r.name,
    })
  }
  return ''
}

function fmtDuration(ms?: number): string {
  if (!ms || ms < 0) return ''
  if (ms < 1000) return `${ms}ms`
  const s = ms / 1000
  return s < 60 ? `${s.toFixed(1)}s` : `${Math.floor(s / 60)}m${String(Math.round(s % 60)).padStart(2, '0')}s`
}

// 升级记录的一行中文摘要（与 CLI `gofer worker show` 同口径）。
export function upgradeSummary(u: WorkerUpgradeRecord | undefined): string {
  if (!u) return ''
  const dur = fmtDuration(u.duration_ms)
  const took = dur ? t('util.upgrade.took', { took: dur }) : ''
  switch (u.state) {
    case 'pending':
      return t('util.upgrade.summary.pending', { from: u.from_version || '?', to: u.target_version || '?' })
    case 'succeeded':
      return t('util.upgrade.summary.ok', { to: u.target_version || '?', took })
    case 'rolled_back':
      return t('util.upgrade.summary.rolledBack', { error: u.error || t('util.upgrade.notRegistered'), took })
    default:
      return t('util.upgrade.summary.failed', { error: u.error || t('util.upgrade.unknownReason'), took })
  }
}

export function upgradeStateClass(u: WorkerUpgradeRecord | undefined): string {
  switch (u?.state) {
    case 'pending': return 'st--warn'
    case 'succeeded': return 'st--ok'
    case 'rolled_back':
    case 'failed': return 'st--down'
    default: return ''
  }
}

// runnerTools 返回 runner 所在机器声明的外部工具：优先能力摘要（local / worker 同源），
// 旧响应只有 worker 明细时退回 worker.tools。
export function runnerTools(r: Runner): RunnerTool[] {
  return r.capabilities?.tools ?? r.worker?.tools ?? []
}

// toolTitle 是工具 chip 的悬停说明：desc 与（不同于 name 时的）可执行文件。
export function toolTitle(t: RunnerTool): string {
  const parts: string[] = []
  if (t.desc) parts.push(t.desc)
  if (t.bin && t.bin !== t.name) parts.push(`bin: ${t.bin}`)
  return parts.join('\n') || t.name
}
