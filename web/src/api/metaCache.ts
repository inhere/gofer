// /v1/agents 与 /v1/meta 的模块级缓存（Q1）：NewJob / Workbench / Agents / JobDetail 共用一份，
// 不再每个页面各拉一次。推送的 `meta` 失效通知（配置热重载、agent 降级/恢复）到来时失效重拉。
import { listAgents, getMeta } from './client'
import type { AgentsResp, MetaResp } from './types'

let agentsP: Promise<AgentsResp> | null = null
let metaP: Promise<MetaResp> | null = null
const listeners = new Set<() => void>()

export function getAgentsCached(): Promise<AgentsResp> {
  if (!agentsP) {
    agentsP = listAgents().catch((e) => {
      agentsP = null // 失败不缓存
      throw e
    })
  }
  return agentsP
}

export function getMetaCached(): Promise<MetaResp> {
  if (!metaP) {
    metaP = getMeta().catch((e) => {
      metaP = null
      throw e
    })
  }
  return metaP
}

// 失效缓存并通知订阅者重拉（meta 主题的 inval / 兜底轮询调用）。
export function invalidateMetaCache(): void {
  agentsP = null
  metaP = null
  for (const fn of [...listeners]) fn()
}

export function onMetaInvalidated(fn: () => void): () => void {
  listeners.add(fn)
  return () => listeners.delete(fn)
}
