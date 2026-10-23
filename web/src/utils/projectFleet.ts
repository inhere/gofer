// 项目页「按执行机」视图的数据整形（UI 重设计 P5，issue gofer-d1kn）。
//
// 只用现有接口，全部是纯函数（测试见 utils/projectFleet.test.ts）：
//   GET /v1/projects                  server 声明的项目 key + 内置注入的 key
//   GET /v1/projects/{key}            allowed_runners（有配置读权限时来自 GET /v1/config 的 projects）
//   GET /v1/runners                   执行机清单与状态（local / worker 行）
//   GET /v1/workers/{id}              worker 心跳上报的目录（dirs.workspace = 它的默认工作空间）
//   GET /v1/workers/{id}/projects     worker 自己在 worker.yaml 里声明的项目（= worker 自有）
//
// 两处准入口径与后端一致，别在前端自创：
//   - 空 allowed_runners 只放行本机，绝不当通配（internal/project.AllowsLocalRunner、
//     internal/core/policy.go Q8「空 allowlist 不推给任何 worker」）；
//   - `server` 是 `local` 的对外拼写（G043），比较前统一成规范名。

import type { ProjectDetail, Runner, RunnerDirs, WorkerView } from '../api/types'
import { isLocalRunnerName, runnerLabel } from './runnerDisplay'

// FleetProject 是一组里「server 声明、且允许这台执行机运行」的项目。
export interface FleetProject {
  key: string
  // 内置注入（配置里没写）的 default 项目
  injected: boolean
}

// FleetOwnProject 是 worker 自己在 worker.yaml 里声明、server 配置里没有的项目。
export interface FleetOwnProject {
  key: string
  // 该 worker 上报的解析路径（心跳里没有这个项目时为空）
  path?: string
  exists?: boolean
}

// FleetNote 是「取不到」的原因码，文案由页面走 i18n：
//   ''           有数据（哪怕为空）
//   offline      worker 离线，心跳上报的数据取不到
//   notReported  在线但没有上报（旧版 worker，或还没有心跳）
export type FleetNote = '' | 'offline' | 'notReported'

export interface FleetGroup {
  // 'local' 或 worker id
  id: string
  kind: 'server' | 'worker'
  // 组标题：`server` / worker id
  label: string
  online: boolean
  // 该组的执行机名（`--runner` 取的值）：allowed_runners 按这些名字匹配
  runners: string[]
  workspace?: { path: string; exists?: boolean }
  workspaceNote: FleetNote
  projects: FleetProject[]
  own: FleetOwnProject[]
  ownNote: FleetNote
  // 取不到该 worker 数据时的原始错误（页面原样显示，不翻译）
  error?: string
}

export interface FleetInput {
  // server 声明的项目 key（GET /v1/projects 的 projects，含内置 default）
  projects: string[]
  // GET /v1/projects 的 injected
  injected: string[]
  // project key → allowed_runners（undefined = 取不到；按「只在本机可用」处理）
  allowed: Record<string, string[] | undefined>
  // GET /v1/runners
  runners: Runner[]
  // worker id → GET /v1/workers/{id}
  workerViews: Record<string, WorkerView | undefined>
  // worker id → GET /v1/workers/{id}/projects 的 projects（undefined = 取不到）
  workerProjects: Record<string, string[] | undefined>
  // worker id → 取数失败原因
  workerErrors: Record<string, string | undefined>
}

// workerOwnRow 是「按项目」视图里的 worker 自有项目一行（同一 key 可能被多台 worker 声明）。
export interface WorkerOwnRow {
  key: string
  workers: string[]
}

// normalizeRunnerName 与 config.NormalizeRunnerName 同口径：`server` → `local`，大小写不敏感。
export function normalizeRunnerName(name: string): string {
  const value = name.trim().toLowerCase()
  return value === 'server' ? 'local' : value
}

// reachesRunner：项目的 allowed_runners 是否覆盖这组执行机（见文件头的两处口径）。
export function reachesRunner(allowed: string[] | undefined, runners: string[]): boolean {
  const list = (allowed ?? []).map(normalizeRunnerName).filter((name) => name !== '')
  if (list.length === 0) return runners.some((name) => isLocalRunnerName(name))
  return runners.some((name) => list.includes(normalizeRunnerName(name)))
}

function workspaceOf(dirs: RunnerDirs | undefined): { path: string; exists?: boolean } | undefined {
  const ws = dirs?.workspace
  return ws?.path ? { path: ws.path, exists: ws.exists } : undefined
}

// ownProjectsOf：worker 自己声明的项目。server 也声明了的 key 不算「自有」（那时它是 server
// 项目的准入问题，不是 worker 私有项目），路径取该 worker 上报的解析路径。
function ownProjectsOf(
  declared: Set<string>,
  projects: string[] | undefined,
  dirs: RunnerDirs | undefined,
): FleetOwnProject[] {
  const paths = new Map((dirs?.projects ?? []).map((p) => [p.key, p]))
  return (projects ?? [])
    .filter((key) => key !== '' && !declared.has(key))
    .map((key) => {
      const hit = paths.get(key)
      return { key, path: hit?.path, exists: hit?.exists }
    })
    .sort((a, b) => a.key.localeCompare(b.key))
}

// buildFleet：server 一组 + 每个 worker 一组。worker 行来自 /v1/runners（离线 worker 也在里面），
// 每台的数据另取 /v1/workers/{id} 与 /v1/workers/{id}/projects。
export function buildFleet(input: FleetInput): FleetGroup[] {
  const injected = new Set(input.injected)
  const declared = new Set(input.projects)
  const keys = [...input.projects].sort()

  const allowedProjects = (runners: string[]): FleetProject[] =>
    keys
      .filter((key) => reachesRunner(input.allowed[key], runners))
      .map((key) => ({ key, injected: injected.has(key) }))

  const groups: FleetGroup[] = []

  // server 本机：/v1/runners 的 local 行（服务就在跑，没有这一行也照样列一组）。
  const locals = input.runners.filter((r) => r.type === 'local')
  const localNames = locals.length ? locals.map((r) => r.name) : ['local']
  const localWs = workspaceOf(locals[0]?.dirs)
  groups.push({
    id: 'local',
    kind: 'server',
    label: runnerLabel(locals[0]?.name || 'local'),
    online: true,
    runners: localNames,
    workspace: localWs,
    workspaceNote: localWs ? '' : 'notReported',
    projects: allowedProjects(localNames),
    own: [],
    ownNote: '',
  })

  // 每个 worker 一组：同一 worker 上有多个 runner 名时合成一组（allowed_runners 按名字匹配）。
  const byWorker = new Map<string, Runner[]>()
  for (const r of input.runners) {
    if (r.type !== 'worker') continue
    const id = r.worker_id || r.name
    const rows = byWorker.get(id)
    if (rows) rows.push(r)
    else byWorker.set(id, [r])
  }
  for (const id of [...byWorker.keys()].sort()) {
    const rows = byWorker.get(id) ?? []
    const names = rows.map((r) => r.name)
    const view = input.workerViews[id]
    const online = view ? view.connected : rows.some((r) => r.status === 'connected')
    const dirs = view?.worker?.dirs ?? rows[0]?.dirs
    const ws = workspaceOf(dirs)
    const own = ownProjectsOf(declared, input.workerProjects[id], dirs)
    // GET /v1/workers/{id}/projects 对「已登记但离线」的 worker 也回 200 + 空列表（注册表里没有
    // 它的快照），那不代表它没有项目 —— 必须再看 connected，否则会把「取不到」说成「没有」。
    const haveProjects = input.workerProjects[id] !== undefined && online
    groups.push({
      id,
      kind: 'worker',
      label: id,
      online,
      runners: names,
      workspace: ws,
      workspaceNote: ws ? '' : online ? 'notReported' : 'offline',
      projects: allowedProjects(names),
      own,
      ownNote: haveProjects ? '' : online ? 'notReported' : 'offline',
      error: input.workerErrors[id] || undefined,
    })
  }

  return groups
}

// workerOwnProjects：「按项目」视图里的 worker 自有项目（server 配置里没有的那些），
// 同一 key 被多台 worker 声明时合成一行，带上声明它的 worker。
export function workerOwnProjects(input: Pick<FleetInput, 'projects' | 'workerProjects'>): WorkerOwnRow[] {
  const declared = new Set(input.projects)
  const rows = new Map<string, string[]>()
  for (const [workerID, projects] of Object.entries(input.workerProjects)) {
    for (const key of projects ?? []) {
      if (key === '' || declared.has(key)) continue
      const at = rows.get(key)
      if (!at) rows.set(key, [workerID])
      else if (!at.includes(workerID)) at.push(workerID)
    }
  }
  return [...rows.entries()]
    .sort(([a], [b]) => a.localeCompare(b))
    .map(([key, workers]) => ({ key, workers: [...workers].sort() }))
}

// projectAllowedFromConfig：seed allowed_runners（有 config 读权限时一次拿全，省掉逐项目请求）。
export function projectAllowedFromConfig(projects: ProjectDetail[] | undefined): Record<string, string[]> {
  const out: Record<string, string[]> = {}
  for (const p of projects ?? []) {
    if (p?.key) out[p.key] = p.allowed_runners ?? []
  }
  return out
}
