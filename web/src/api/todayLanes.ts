// 「今天」首页「并行中」泳道（N3 T2）：GET /v1/today/lanes 的类型与请求。
// 与 client.ts 共用 request（同一份鉴权与错误解析）。
import { request } from './client'
import type { WorkHealth } from './types'

export type LaneKind = 'work' | 'plan'
export type LaneAgentState = 'running' | 'awaiting_input' | 'idle'
export type LanePipStatus = 'done' | 'running' | 'needs_review' | 'failed' | 'pending'

export interface LaneAgent {
  agent: string
  state: LaneAgentState
  // session = 终端会话；job = job（含 ACP / pty 会话 job）
  kind: 'session' | 'job'
  ref: string
}

export interface LanePip {
  todo_id: string
  title: string
  status: LanePipStatus
}

export interface LaneProgress {
  // 有 plan：todo 节点（按依赖顺序）；无 plan 时缺省
  pips?: LanePip[]
  // 有 plan：当前步骤名；无 plan：最近一条里程碑
  current?: string
  current_at?: number
}

export interface LaneUsage {
  total_tokens: number
  cost_usd?: number
}

export interface LaneLinks {
  work_item_id?: string
  plan_id?: string
  job_ids?: string[]
  session_ids?: string[]
}

export interface TodayLane {
  id: string
  kind: LaneKind
  title: string
  project_key?: string
  // 工作项状态，或 plan 状态（open / blocked）
  status: string
  agents: LaneAgent[]
  progress: LaneProgress
  health: WorkHealth
  health_reason?: string
  started_at: number
  elapsed_sec: number
  activity_at: number
  usage?: LaneUsage
  links: LaneLinks
}

export interface TodayLanesSummary {
  total: number
  agents_running: number
  attention: number
}

export interface TodayLanesResp {
  lanes: TodayLane[]
  summary: TodayLanesSummary
  generated_at: number
}

export function getTodayLanes(): Promise<TodayLanesResp> {
  return request<TodayLanesResp>('/v1/today/lanes')
}
