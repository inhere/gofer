// 「工作项」页面的列表口径（纯函数，单测覆盖）：计数 chip、项目收窄、停滞标记、
// 以及由数据决定该用哪条文案（状态来源 / 到期种类）。
// 组件只负责渲染；文案 key 由这里给出，页面 / 卡片用 t() 取译文（i18n/pages/work.ts）。
import type { WorkItem, WorkRequest } from '../api/types'
import { t } from '../i18n'
import { isClosed, requestInFlight } from './work'

/** 列表头三个计数 chip 的键（值 = utils/work.ts 的 WorkFilter 值）。 */
export const WORK_CHIP_KEYS = ['needs_me', 'due', 'unsorted'] as const
export type WorkChipKey = (typeof WORK_CHIP_KEYS)[number]

export interface WorkCounts {
  /** 未结束的工作项（不含收集箱条目） */
  open: number
  needs_me: number
  due: number
  unsorted: number
}

// 计数口径与迁移前一致：只算未结束的（done / dropped 不算），未整理取草稿区那一份。
export function workCounts(items: WorkItem[], unsorted: WorkItem[] = []): WorkCounts {
  const open = items.filter((it) => !isClosed(it.status))
  return {
    open: open.length,
    needs_me: open.filter((it) => it.status === 'needs_me').length,
    due: open.filter((it) => it.due).length,
    unsorted: unsorted.length,
  }
}

/** 项目收窄（空串 = 全部项目）。收集箱条目一起按项目收窄。 */
export function itemInProject(it: WorkItem, project: string): boolean {
  const want = project.trim()
  if (!want) return true
  return (it.project_key ?? '').trim() === want
}

export function narrowByProject(items: WorkItem[], project: string): WorkItem[] {
  const want = project.trim()
  if (!want) return items
  return items.filter((it) => itemInProject(it, want))
}

/** 停滞阈值：未结束、非搁置、非草稿，且这么多天没有一点活动，就在卡片上标停滞。 */
export const STALL_DAYS = 2

export function stalledDays(it: WorkItem, nowSec: number): number {
  if (isClosed(it.status) || it.status === 'parked' || it.unsorted) return 0
  // 有在途请求说明正在等会话 / 整理器的结果，不算停滞
  if ((it.requests ?? []).some((r) => r.state === 'pending' || r.state === 'sent')) return 0
  const idle = nowSec - (it.last_activity_at || 0)
  if (idle <= 0) return 0
  const days = Math.floor(idle / 86400)
  return days >= STALL_DAYS ? days : 0
}

/** 卡片上的到期徽标种类：搁置到期与提醒到期用不同文案。 */
export function dueLabelKey(it: WorkItem): string {
  if (!it.due) return ''
  if (it.status === 'parked' && it.park_until && it.park_until * 1000 <= Date.now()) return 'work.card.dueParked'
  return 'work.card.dueRemind'
}

/** 「来源」一行：工作项是谁建的。 */
export function sourceLabelKey(source: WorkItem['source']): string {
  if (source === 'auto') return 'work.d.sourceAuto'
  if (source === 'steward') return 'work.d.sourceSteward'
  return 'work.d.sourceHuman'
}

/** 「状态」一行：状态由谁决定。 */
export function statusSourceLabelKey(source: string | undefined): string {
  if (source === 'auto') return 'work.d.statusSrcAuto'
  if (source === 'human') return 'work.d.statusSrcHuman'
  if (source === 'report') return 'work.d.statusSrcReport'
  return ''
}

/** 整理建议的字段名。 */
export function suggestionLabelKey(field: string): string {
  const known: Record<string, string> = {
    goal: 'goal',
    blocker: 'blocker',
    blocker_kind: 'blockerKind',
    next: 'next',
    summary: 'summary',
    status_hint: 'status',
  }
  return `work.o.suggest.${known[field] ?? 'other'}`
}

/** 请求账本的种类（汇报 / 交接 / 整理）。 */
export function requestKindLabelKey(kind: string): string {
  if (kind === 'handoff') return 'work.o.request.handoff'
  if (kind === 'summarize') return 'work.o.request.summarize'
  return 'work.o.request.report'
}

/** 请求账本的状态。 */
export function requestStateLabelKey(state: string): string {
  const known: Record<string, string> = {
    pending: 'pending',
    sent: 'sent',
    answered: 'answered',
    failed: 'failed',
    expired: 'expired',
  }
  return `work.o.request.state.${known[state] ?? 'other'}`
}

/**
 * 请求账本的一行说明（卡片与详情共用，走本页字典）：
 * 「汇报请求 · 已送达，等会话回复（12 分钟后超时）」/「整理 · 失败：整理器不可用…」。
 * utils/work.ts 的 requestLine 保留给旧调用方与既有单测，页面文案以这条为准。
 */
export function workRequestLine(r: WorkRequest, nowSec: number = Math.floor(Date.now() / 1000)): string {
  const kind = t(requestKindLabelKey(r.kind))
  switch (r.state) {
    case 'pending':
      return `${kind} · ${t(r.kind === 'summarize' ? 'work.o.request.working' : 'work.o.request.sending')}`
    case 'sent': {
      const left = r.deadline ? Math.max(0, Math.ceil((r.deadline - nowSec) / 60)) : 0
      const timeout = r.deadline ? t('work.o.request.sentTimeout', { n: left }) : ''
      return `${kind} · ${t('work.o.request.sentWaiting')}${timeout}`
    }
    case 'answered':
      return `${kind} · ${t(r.kind === 'summarize' ? 'work.o.request.answeredDone' : 'work.o.request.answeredSession')}`
    case 'expired':
      return `${kind} · ${t('work.o.request.expiredToSummary')}`
    case 'failed':
      return `${kind} · ${t('work.o.request.failed')}${r.error ? '：' + r.error : ''}`
    default:
      return `${kind} · ${t(requestStateLabelKey(r.state))}`
  }
}

export { requestInFlight }
