// GIT-02：worktree job 的交付（合并 / 推送 / PR）展示文字。文案走 i18n（util.delivery.*）。
import type { JobDelivery } from '../api/types'
import { t, te } from '../i18n'

/** 一行状态文字（不含链接）。 */
export function deliveryText(d: JobDelivery | undefined | null): string {
  if (!d) return ''
  const parts = [deliveryStateLabel(d.state) || d.state]
  if (d.branch) parts.push(d.branch)
  if (d.base) parts.push(`→ ${d.base}`)
  if (d.note) parts.push(t('util.delivery.note', { note: d.note }))
  if (d.error) parts.push(t('util.delivery.error', { error: d.error }))
  return parts.join(' ')
}

/** 交付状态的显示名（plan 待办 / 看板卡片上的 PR 链接悬浮提示用）；未知状态原样返回。 */
export function deliveryStateLabel(state: string | undefined): string {
  if (!state) return ''
  const key = `util.delivery.state.${state}`
  return te(key) ? t(key) : state
}

/** 交付方式的显示名（验收按钮与下拉框共用）。 */
export function deliveryModeLabel(mode: string | undefined): string {
  const key = `util.delivery.mode.${mode ?? ''}`
  return te(key) ? t(key) : ''
}

/** 交付后能点开的链接：PR，或推送后手动开 PR 的页面。 */
export function deliveryLink(d: JobDelivery | undefined | null): { href: string; label: string } | null {
  if (!d) return null
  if (d.pr_url) return { href: d.pr_url, label: d.pr_number ? `PR #${d.pr_number}` : t('util.delivery.link.pr') }
  if (d.state === 'pushed' && d.compare_url) return { href: d.compare_url, label: t('util.delivery.link.openPr') }
  return null
}
