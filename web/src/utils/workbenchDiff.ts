// 会话改动的展示逻辑（gofer-yb5x）：工作台「改动」页签用。
//
// 没有 git 基线（首轮作业没有 base_sha / worktree_base_sha）或会话不可续接时，后端用
// 409 + detail 说明原因。那不是"加载失败"，页签里照着原因写一句说明就好；把它当红色
// 错误弹出来（还顺手打一条控制台错误）正是这个 issue 要修的东西。
import { ApiError } from '../api/client'

export interface DiffFailure {
  /** true = 页签内的说明（后端给了原因），false = 真正的加载失败 */
  unavailable: boolean
  /** 原因：服务端 detail 已是可读中文，优先原样显示 */
  reason: string
}

export function classifyDiffFailure(error: unknown): DiffFailure {
  const reason = error instanceof ApiError
    ? error.detail || error.message
    : error instanceof Error ? error.message : String(error)
  return { unavailable: error instanceof ApiError && error.status === 409, reason }
}
