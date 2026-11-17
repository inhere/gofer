// 新建会话的默认值（R13）：默认 ACP + `claude-acp`（项目准入里有时）、runner 默认
// 在线且带 `container` 标签、支持该 agent 的 worker，没有则本机；记住上次选的项目。
import type { MetaAgent, MetaProject, MetaRunner, MetaWorker } from '../api/types'

// 新建会话首选的 agent：项目放行时用它，否则回落到准入名单里的第一个 ACP agent。
export const PREFERRED_SESSION_AGENT = 'claude-acp'
// 只有这些标签的 worker 适合跑容器里的会话；没有就退回本机。
export const SESSION_WORKER_LABEL = 'container'
// 上次选的项目的本地记忆（localStorage）。
export const LAST_PROJECT_KEY = 'gofer.sessions.lastProject'

// sessionAgents 是「类型 = acp」的可选 agent，顺序与准入名单一致（准入为空的 = 不限制）。
export function sessionAgents(meta: Pick<{ agents: MetaAgent[] }, 'agents'>, project?: Pick<MetaProject, 'allowed_agents'>): MetaAgent[] {
  const allowed = new Set(project?.allowed_agents ?? [])
  return (meta.agents ?? [])
    .filter((agent) => agent.type === 'acp-agent')
    .filter((agent) => allowed.size === 0 || allowed.has(agent.key))
}

// defaultSessionAgent 选新建会话的默认 agent：`claude-acp` 在准入里就用它，否则第一个。
export function defaultSessionAgent(agents: MetaAgent[]): string {
  if (agents.some((agent) => agent.key === PREFERRED_SESSION_AGENT)) return PREFERRED_SESSION_AGENT
  return agents[0]?.key ?? ''
}

function workerOf(runner: MetaRunner, workers: MetaWorker[]): MetaWorker | undefined {
  const id = runner.worker_id || runner.name
  return workers.find((worker) => worker.id === id)
}

// workerSupportsAgent：worker 上报的 agent 名单里有它才算支持（老 worker 没上报 = 不知道，
// 不据此排除，交给后端校验）。
function workerSupportsAgent(worker: MetaWorker | undefined, agent: string): boolean {
  if (!worker) return false
  const known = [...(worker.agents ?? []), ...(worker.agent_caps ?? []).map((cap) => cap.key)]
  if (known.length === 0) return true
  return known.includes(agent)
}

// defaultSessionRunner 选默认 runner：在线的 worker、带 container 标签、支持该 agent；
// 都不满足时退回本机 local（没有 local 就退回第一个 runner，再没有就是 'local'）。
export function defaultSessionRunner(
  runners: MetaRunner[],
  workers: MetaWorker[],
  agent: string,
): string {
  const usable = runners.filter((runner) => runner.type === 'local' || runner.type === 'worker')
  const containerWorker = usable.find((runner) => {
    if (runner.type !== 'worker') return false
    const worker = workerOf(runner, workers)
    if (!worker?.connected) return false
    if (!(worker.labels ?? []).includes(SESSION_WORKER_LABEL)) return false
    return workerSupportsAgent(worker, agent)
  })
  if (containerWorker) return containerWorker.name
  const local = usable.find((runner) => runner.type === 'local')
  return local?.name ?? usable[0]?.name ?? 'local'
}

// rememberedProject 读上次选的项目：仍在上限内（出现在候选里）时用它。
export function rememberedProject(candidates: string[], stored: string | null | undefined): string {
  return stored && candidates.includes(stored) ? stored : ''
}

// rememberProject 记住这次选的项目（localStorage 不可用时就只在本次生效）。
export function rememberProject(project: string): void {
  if (!project) return
  try {
    localStorage.setItem(LAST_PROJECT_KEY, project)
  } catch {
    // 无本地存储：只在本次页面生效。
  }
}

// readRememberedProject 读 localStorage 里的上次项目（异常 = 没有）。
export function readRememberedProject(): string {
  try {
    return localStorage.getItem(LAST_PROJECT_KEY) ?? ''
  } catch {
    return ''
  }
}

// 权限档位（R7）：自动 / 问我 / 严格。
export type PermissionTier = 'auto' | 'ask' | 'strict'

// permissionTierValue 把表单选择转成提交字段：auto 是后端默认，干脆不发。
export function permissionTierValue(tier: PermissionTier): PermissionTier | undefined {
  return tier === 'auto' ? undefined : tier
}

// sessionSubmitOptions 把新建会话的两个新选项转成提交字段（R7 / R8）：只给 ACP 会话，
// 自动档不发 permission_tier，保持目录锁默认关。
export function sessionSubmitOptions(opts: {
  type: 'pty' | 'acp'
  tier: PermissionTier
  keepDirLock: boolean
}): { permission_tier?: PermissionTier; keep_dir_lock?: boolean } {
  if (opts.type !== 'acp') return {}
  const out: { permission_tier?: PermissionTier; keep_dir_lock?: boolean } = {}
  const tier = permissionTierValue(opts.tier)
  if (tier) out.permission_tier = tier
  if (opts.keepDirLock) out.keep_dir_lock = true
  return out
}
