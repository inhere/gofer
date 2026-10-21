// 审阅提示（gofer-hjj1）的显示纯函数：与 internal/job/reviewhints.go 的口径一致，
// 只负责把三节渲染成一行行文本，判定留在服务端。
import type { DeletionHotspot, RemovedComment, ReviewHints } from '../api/reviewHints'

// hasReviewHints：三节都不为空才渲染卡片（干净的 diff 不是「没有审阅」）。
export function hasReviewHints(h: ReviewHints | null | undefined): boolean {
  if (!h) return false
  return reviewHintCount(h) > 0
}

// reviewHintCount：卡片标题里的条数（三节之和）。
export function reviewHintCount(h: ReviewHints | null | undefined): number {
  if (!h) return 0
  return (h.deletion_hotspots?.length ?? 0) + (h.removed_comments?.length ?? 0) + (h.verify_lines?.length ?? 0)
}

// hotspotLine："+12/-30 path"，added 在前、deleted 在后（排序依据是 deleted）。
export function hotspotLine(d: DeletionHotspot): string {
  return `+${d.added}/-${d.deleted}  ${d.path}`
}

// removedCommentLine：旧文件行号 + 被删的注释原文。
export function removedCommentLine(rc: RemovedComment): string {
  return `${rc.path}:${rc.line}  ${rc.text}`
}
