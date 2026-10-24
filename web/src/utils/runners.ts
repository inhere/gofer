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

// runnerOnline：这个 runner 此刻是否在线。status 的取值按类型分开——local / peer-http 用
// up，worker 用心跳导出的 connected（见 api/types.ts RunnerStatus）；侧栏的「节点 N/M 在线」
// 与运行节点页必须同一口径。曾只判 up，于是 5 个节点里 4 个已连接的 worker 都被算成离线。
export function runnerOnline(status: Runner['status']): boolean {
  return status === 'up' || status === 'connected'
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
// 升级二进制按平台选：与 server 同 os/arch 用 server 自己的可执行文件，其它平台由 server
// 从下载源（server.update 的 mirror / GitHub release）取该平台的发布包（gofer-n1vj），所以
// 平台不同不再是不可升级的理由。下载源没配好时 server 拒绝并回原因，显示在升级提示里。
export function upgradeBlockReason(r: Runner, server?: RunnersServerInfo): string {
  const w = r.worker
  if (r.status !== 'connected' || !w) return t('util.upgrade.offline')
  if (r.upgrade?.state === 'pending') return t('util.upgrade.inProgress')
  if ((w.protocol_version ?? 0) < UPGRADE_MIN_PROTOCOL) return t('util.upgrade.tooOld')
  if (!server) return t('util.upgrade.noServer')
  return ''
}

// 升级二进制从哪来：同平台用 server 自己的可执行文件（server），其它平台由 server 从下载源
// 取该平台的发布包（mirror）。平台未知时按 server 处理，由 server 报原因。
export function upgradeSource(r: Runner, server?: RunnersServerInfo): 'server' | 'mirror' {
  const w = r.worker
  if (server && w?.os && w.arch && (w.os !== server.os || w.arch !== server.arch)) return 'mirror'
  return 'server'
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
