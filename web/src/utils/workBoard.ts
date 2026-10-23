// 「工作项」页面的列表口径（纯函数，单测覆盖）：计数 chip、项目收窄、停滞标记、
// 以及由数据决定该用哪条文案（状态来源 / 到期种类）。
// 组件只负责渲染；文案 key 由这里给出，页面 / 卡片用 t() 取译文（i18n/pages/work.ts）。
import type { WorkItem, WorkRequest, WorkStatus } from '../api/types'
import { t } from '../i18n'
import { statusFamily, type StatusFamily } from './statusFamily'
import { isClosed, requestInFlight } from './work'

// ---------------- 平铺列表（P6 工作项页收尾） ----------------
// 页面不再分栏 / 分段，而是一条平铺列表：泳道顺序在这里一处定义，页面与测试都读它。
// 顺序按「先要人、再在跑、再等资源、再待整理、最后收尾」；收集箱排在草稿之后。

/** 收集箱条目的伪状态（服务端 status=captured，不在 WorkStatus 里）。 */
export const CAPTURE_LANE_STATUS = 'captured'

/** 泳道：工作项状态 + 未整理草稿 / 收集箱两个伪状态。顺序即列表顺序。 */
export const WORK_LANES = [
  'needs_me',
  'review',
  'active',
  'needs_onsite',
  'waiting_resource',
  'parked',
  'draft',
  CAPTURE_LANE_STATUS,
  'done',
  'dropped',
] as const

export type WorkLane = (typeof WORK_LANES)[number]

const LANE_SET = new Set<string>(WORK_LANES)

export function isWorkLane(value: unknown): value is WorkLane {
  return typeof value === 'string' && LANE_SET.has(value)
}

/** 一条工作项属于哪条泳道：收集箱 → captured；未结束的未整理草稿 → draft；其余就是状态本身。 */
export function workLane(it: Pick<WorkItem, 'status' | 'unsorted'>): WorkLane {
  const status = it.status as string
  if (status === CAPTURE_LANE_STATUS) return CAPTURE_LANE_STATUS
  if (it.unsorted && !isClosed(it.status)) return 'draft'
  return isWorkLane(status) ? status : 'active'
}

/** 泳道 → 状态族（颜色取 styles/status.css 的 --c；唯一映射表是 utils/statusFamily.ts）。 */
export function laneFamily(lane: WorkLane): StatusFamily {
  // 两个伪状态都是「还没开始」：用中性色（设计稿 §5：queue 用 --ink-3）。
  if (lane === 'draft' || lane === CAPTURE_LANE_STATUS) return 'queue'
  return statusFamily(lane)
}

/** 泳道的 CSS 类（.k-run …）：卡片左边框颜色由它给出的 --c 决定。 */
export function laneClass(lane: WorkLane): string {
  return `k-${laneFamily(lane)}`
}

/** 泳道的显示名：真实状态的译文走 status.<值>，两个伪状态走本页字典。 */
export function laneLabelKey(lane: WorkLane): string {
  if (lane === 'draft') return 'work.lane.draft'
  if (lane === CAPTURE_LANE_STATUS) return 'work.lane.captured'
  return `status.${lane}`
}

/** 泳道顺序（0 起），不在表里的排最后。 */
export function laneOrder(lane: WorkLane): number {
  const i = WORK_LANES.indexOf(lane)
  return i < 0 ? WORK_LANES.length : i
}

/** 平铺排序：先按泳道顺序，泳道内按最近活动倒序（收集箱条目没有活动时间，用创建时间）。 */
export function sortByLane<T extends Pick<WorkItem, 'status' | 'unsorted' | 'last_activity_at' | 'created_at' | 'id'>>(items: T[]): T[] {
  const activity = (it: T): number => it.last_activity_at || it.created_at || 0
  return [...items].sort((a, b) => {
    const la = laneOrder(workLane(a))
    const lb = laneOrder(workLane(b))
    if (la !== lb) return la - lb
    const da = activity(a)
    const db = activity(b)
    if (da !== db) return db - da
    return a.id.localeCompare(b.id)
  })
}

export type LaneCounts = Record<WorkLane, number>

/** 每条泳道有多少条（不受泳道筛选影响，chip 上的数字要稳定）。 */
export function laneCounts(items: WorkItem[]): LaneCounts {
  const out = {} as LaneCounts
  for (const lane of WORK_LANES) out[lane] = 0
  for (const it of items) out[workLane(it)]++
  return out
}

/** ?status= 的值：认不出来的（旧链接、手改成已删状态）当「全部」。 */
export function laneFromQuery(query: Record<string, unknown> | undefined): WorkLane | '' {
  const raw = query?.status
  const v = Array.isArray(raw) ? raw[0] : raw
  if (isWorkLane(v)) return v
  // 迁移兼容：收集箱原来用 ?tab=capture 深链（顶栏「随手记」、Today 的入口），
  // 现在收集箱是一条泳道，旧链接落到同一条筛选上，不另设兼容分支。
  const tab = query?.tab
  if (tab === 'capture') return CAPTURE_LANE_STATUS
  return ''
}

/** 切换泳道后的 query：默认「全部」不写进 URL，其余参数保留。 */
export function laneQuery(query: Record<string, unknown> | undefined, lane: WorkLane | ''): Record<string, string> {
  const next: Record<string, string> = {}
  for (const [k, v] of Object.entries(query ?? {})) {
    if (typeof v === 'string' && v && k !== 'status' && k !== 'tab') next[k] = v
  }
  if (lane) next.status = lane
  return next
}

/** 泳道的状态过滤（全 = 不筛）。 */
export function inLane(it: WorkItem, lane: WorkLane | '') {
  return !lane || workLane(it) === lane
}

/** 状态 chip 的顺序：未结束的六条先来，已结束（done / dropped）只在「显示已完成」时出现。 */
export function laneChipKeys(showClosed: boolean): WorkLane[] {
  return WORK_LANES.filter((lane) => showClosed || !isClosed(lane as WorkStatus))
}

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
