// 流程推进（stage）的接口（docs/reference/http-api.md「流程推进（stage）」）：
// GET 一个工作项的流程（当前步的门禁实时求值），以及 done / back / approve / level 四个写接口。
// 开始 / 放弃流程由命令行做（`gofer stage start` / `drop`），控制台只显示与推进已有的流程。
import { request } from './client'
import type { StageActionResult, StageLevel, WorkStageView } from './types'

const base = (id: string): string => `/v1/work-items/${encodeURIComponent(id)}/stage`

// getWorkStage：工作项没有进行中的流程（或不存在）时 404。
export function getWorkStage(id: string): Promise<WorkStageView> {
  return request<WorkStageView>(base(id))
}

// stageDone 求值当前步的门禁：advanced=false 不是错误，gate.missing 说还缺什么，流程停在原步。
export function stageDone(id: string): Promise<StageActionResult> {
  return request<StageActionResult>(`${base(id)}/done`, { method: 'POST' })
}

// stageBack 退回上一步，必须带理由（服务端空理由 400、已在第一步 400）。
export function stageBack(id: string, reason: string): Promise<WorkStageView> {
  return request<WorkStageView>(`${base(id)}/back`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ reason }),
  })
}

// stageApprove 是人对当前步的批准：review 的门禁就是这次批准，判不了的门禁（needs_human）被放行。
// 服务端只认人的凭据，其他情况 403。
export function stageApprove(id: string): Promise<StageActionResult> {
  return request<StageActionResult>(`${base(id)}/approve`, { method: 'POST' })
}

// stageSetLevel 改放权等级（record / remind），返回流程视图；只有人的凭据能改。
export function stageSetLevel(id: string, level: StageLevel): Promise<WorkStageView> {
  return request<WorkStageView>(`${base(id)}/level`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ level }),
  })
}
