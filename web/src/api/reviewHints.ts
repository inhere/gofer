// 审阅提示（gofer-hjj1）：服务端按 job 的 changes.diff 与 stdout 尾部现算的三块提示——
// 删除行最多的文件、被删掉且没在原处重新出现的注释行、汇报里的验证结果行。
// 验收面板「汇报」页签顶部折叠展示，只读；CLI `job review` 用同一个接口。
import { request } from './client'

export interface DeletionHotspot {
  path: string
  added: number
  deleted: number
  // net = added - deleted（负数 = 文件变短）。
  net?: number
  // deletion_heavy = 删除行数显著多于新增（服务端按删除 ≥ 2×新增 判定）。
  deletion_heavy?: boolean
}

export interface RemovedComment {
  path: string
  line: number
  text: string
}

// 汇报里「验证」标题下的代码块：agent 自述的输出，job 没有 verify 步骤时仅作参考。
export interface ReportVerify {
  heading: string
  text: string
  // source 恒为 'agent'（agent 自述，不是 gofer 跑的 verify 步骤）。
  source: string
}

export interface ReviewHints {
  deletion_hotspots?: DeletionHotspot[]
  removed_comments?: RemovedComment[]
  verify_lines?: string[]
  report_verify?: ReportVerify[]
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
