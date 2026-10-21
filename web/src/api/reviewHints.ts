// 审阅提示（gofer-hjj1）：服务端按 job 的 changes.diff 与 stdout 尾部现算的三块提示——
// 删除行最多的文件、被删掉且没在原处重新出现的注释行、汇报里的验证结果行。
// 验收面板「汇报」页签顶部折叠展示，只读；CLI `job review` 用同一个接口。
import { request } from './client'

export interface DeletionHotspot {
  path: string
  added: number
  deleted: number
}

export interface RemovedComment {
  path: string
  line: number
  text: string
}

export interface ReviewHints {
  deletion_hotspots?: DeletionHotspot[]
  removed_comments?: RemovedComment[]
  verify_lines?: string[]
}

interface ReviewHintsResponse {
  id: string
  review_hints?: ReviewHints
}

// 空 job（没采集到 diff / 没有汇报）返回空三节，不是错误。
export async function fetchReviewHints(id: string): Promise<ReviewHints> {
  const res = await request<ReviewHintsResponse>(`/v1/jobs/${encodeURIComponent(id)}/review-hints`)
  return res.review_hints ?? {}
}
