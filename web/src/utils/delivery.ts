// GIT-02：worktree job 的交付（合并 / 推送 / PR）展示文字。
import type { JobDelivery } from '../api/types'

const STATE: Record<string, string> = {
  pending: '交付中',
  merged: '已合并',
  pushed: '已推送分支',
  pr_opened: '已开 PR',
  failed: '交付失败',
  skipped: '未交付',
}

const MODE: Record<string, string> = { merge: '合并到基线', push: '推送分支', pr: '推送并开 PR', none: '不交付' }

/** 一行状态文字（不含链接）。 */
export function deliveryText(d: JobDelivery | undefined | null): string {
  if (!d) return ''
  const parts = [STATE[d.state] ?? d.state]
  if (d.branch) parts.push(d.branch)
  if (d.base) parts.push(`→ ${d.base}`)
  if (d.note) parts.push(`（${d.note}）`)
  if (d.error) parts.push(`：${d.error}`)
  return parts.join(' ')
}

/** 交付状态的中文名（plan 待办 / 看板卡片上的 PR 链接悬浮提示用）；未知状态原样返回。 */
export function deliveryStateLabel(state: string | undefined): string {
  if (!state) return ''
  return STATE[state] ?? state
}

/** 交付方式的中文名（验收按钮与下拉框共用）。 */
export function deliveryModeLabel(mode: string | undefined): string {
  return MODE[mode ?? ''] ?? ''
}

/** 交付后能点开的链接：PR，或推送后手动开 PR 的页面。 */
export function deliveryLink(d: JobDelivery | undefined | null): { href: string; label: string } | null {
  if (!d) return null
  if (d.pr_url) return { href: d.pr_url, label: d.pr_number ? `PR #${d.pr_number}` : '查看 PR' }
  if (d.state === 'pushed' && d.compare_url) return { href: d.compare_url, label: '去开 PR' }
  return null
}
