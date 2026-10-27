// 工作台顶部「运行节点提示」的纯逻辑（issue gofer-okz3 ③）：有 worker 掉线、或有 worker 的
// gofer 版本低于 server 时给一条可关闭的提示，平时（没问题 / 还没拉到数据）返回 null —— 提示条
// 不占位置。
//
// 数据源就是 /v1/runners（Runners 页同一份）：声明过但从没连上 / 已断开的 worker 是
// status=disconnected，连着的 worker 带 worker.gofer_version。
import type { Runner, RunnersServerInfo } from '../api/types'
import { runnerLabel } from './runnerDisplay'
import { runnerOnline } from './runners'

export interface FleetBehind {
  name: string
  // 该 worker 自报的版本（原样，含前缀 v）
  version: string
}

export interface FleetNotice {
  // 掉线的 worker 显示名
  offline: string[]
  // 版本低于 server 的 worker
  behind: FleetBehind[]
}

// parseVersion：`0.199.0` / `v1.2.3` → [0,199,0]。遇到非数字段（`0.199.0-rc1`、
// `1.2.3+abc`、`dev`）在那一截停下，前面解析出来的部分仍可用于比较；一段都解析不出 → null。
export function parseVersion(v: string | undefined | null): number[] | null {
  if (!v) return null
  const body = v.trim().replace(/^v/i, '')
  const out: number[] = []
  for (const part of body.split('.')) {
    const m = /^(\d+)/.exec(part)
    if (!m) break
    out.push(Number(m[1]))
    if (!/^\d+$/.test(part)) break
  }
  return out.length ? out : null
}

// compareVersions：a<b → 负数，相等 → 0，a>b → 正数；任一侧解析不了 → null（不比较，
// 也就不会误报「版本落后」）。
export function compareVersions(a: string | undefined | null, b: string | undefined | null): number | null {
  const pa = parseVersion(a)
  const pb = parseVersion(b)
  if (!pa || !pb) return null
  const len = Math.max(pa.length, pb.length)
  for (let i = 0; i < len; i++) {
    const d = (pa[i] ?? 0) - (pb[i] ?? 0)
    if (d !== 0) return d
  }
  return 0
}

// fleetNotice：只看 worker 行（local / peer-http 不是「掉线」的目标）；没有 worker 报告或
// 没有任何问题时返回 null。names 按字母序，同一台机器反复进出不影响提示内容稳定。
export function fleetNotice(runners: Runner[] | undefined, server?: RunnersServerInfo): FleetNotice | null {
  const workers = (runners ?? []).filter((r) => r.type === 'worker')
  if (workers.length === 0) return null
  const offline: string[] = []
  const behind: FleetBehind[] = []
  for (const w of workers) {
    const name = runnerLabel(w.worker_id || w.name)
    if (!runnerOnline(w.status)) {
      offline.push(name)
      continue
    }
    const version = w.worker?.gofer_version
    const cmp = compareVersions(version, server?.version)
    if (version && cmp != null && cmp < 0) behind.push({ name, version })
  }
  return offline.length || behind.length ? { offline: offline.sort(), behind } : null
}
