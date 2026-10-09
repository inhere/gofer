// N3「今天」决策队列的纯逻辑：分组、短标记、时间文案、卡片操作到现有写接口的映射、跳转目标。
// 组件只管渲染与交互，便于在 node 环境下单测。
import {
  ackSessionTurn,
  acceptJob,
  acceptWorkSuggestion,
  addWorkNote,
  answerDecision,
  answerInteraction,
  dismissWorkSuggestion,
  patchWorkItem,
  planResume,
  rejectJob,
  requestWorkReport,
  saySession,
  sendSessionMessage,
} from '../api/client'
import { acceptMergeSuggestion, dismissMergeSuggestion } from '../api/steward'
import type { TodayAction, TodayCard } from '../api/today'

export const TOP_N = 5
// 撤销窗口（design §2.3）：5 秒后才真正调用写接口；会在 30 秒内超时的卡立即发送。
export const UNDO_MS = 5000
export const SEND_NOW_WITHIN_SEC = 30

export type Tier = 'now' | 'block' | 'normal'

export const TIER_LABEL: Record<Tier, string> = { now: '会超时', block: '卡住别人', normal: '可稍后' }

export function tierOf(card: TodayCard): Tier {
  if (card.urgency === 'now') return 'now'
  return card.blocks.score > 0 ? 'block' : 'normal'
}

// 「卡住 N」短标记：阻塞项数；没有阻塞时不显示。
export function blockShort(card: TodayCard): string {
  return card.blocks.items > 0 ? `卡住 ${card.blocks.items}` : ''
}

export function minutesText(sec: number): string {
  const s = Math.max(0, Math.floor(sec))
  if (s < 60) return '不到 1 分钟'
  if (s < 3600) return `${Math.floor(s / 60)} 分钟`
  if (s < 86400) return `${Math.floor(s / 3600)} 小时`
  return `${Math.floor(s / 86400)} 天`
}

export function waitText(card: TodayCard, nowSec: number): string {
  return card.waiting_since > 0 ? `等了 ${minutesText(nowSec - card.waiting_since)}` : ''
}

export function expiresText(card: TodayCard, nowSec: number): string {
  if (!card.expires_at) return ''
  const left = card.expires_at - nowSec
  return left <= 0 ? '已到超时' : `${minutesText(left)}后超时`
}

export interface TierGroup {
  tier: Tier
  label: string
  cards: TodayCard[]
}

// 按出现顺序把（已排好序的）卡切成分组；后端排序已保证同组相邻。
export function groupByTier(cards: TodayCard[]): TierGroup[] {
  const out: TierGroup[] = []
  for (const c of cards) {
    const t = tierOf(c)
    const last = out[out.length - 1]
    if (last && last.tier === t) last.cards.push(c)
    else out.push({ tier: t, label: TIER_LABEL[t], cards: [c] })
  }
  return out
}

// 管家建议对应的操作（T4 才会有）；有就做成最前面的「按建议：X」主按钮。
export function adviceAction(card: TodayCard): TodayAction | null {
  const id = card.advice?.action_id
  if (!id) return null
  return card.actions.find((a) => actionKey(a) === id || a.id === id) ?? null
}

// 操作的稳定标识：answer 类按 value 区分。
export function actionKey(a: TodayAction): string {
  return a.id === 'answer' ? `answer:${a.value ?? ''}` : a.id
}

// 纯跳转 / 展开类操作：不进撤销窗口、不记审计。
export function isLocalAction(a: TodayAction): boolean {
  return a.id === 'diff' || a.id === 'open' || a.id === 'reply' || a.id === 'rerun'
}

// 卡片标题的跳转目标（design §6）：会话类 → 工作台；工作项 → Works 抽屉；plan → 详情；job → 详情。
export function cardLink(card: TodayCard): string {
  const r = card.refs
  switch (card.kind) {
    case 'relay':
    case 'interaction':
      if (r.thread_id) return `/workbench?thread=${encodeURIComponent(r.thread_id)}`
      if (r.job_id) return `/jobs/${encodeURIComponent(r.job_id)}`
      break
    case 'review':
      if (r.job_id) return `/jobs/${encodeURIComponent(r.job_id)}`
      break
    case 'work':
    case 'suggestion':
    case 'merge':
      if (r.work_item_id) return `/work?id=${encodeURIComponent(r.work_item_id)}`
      break
    case 'decision':
    case 'plan_blocked':
      if (r.plan_id) return `/plans/${encodeURIComponent(r.plan_id)}`
      break
  }
  if (r.plan_id) return `/plans/${encodeURIComponent(r.plan_id)}`
  return '/today'
}

// toast 文案：「已批准」「已回复」……
export function doneLabel(a: TodayAction, viaAdvice = false): string {
  switch (a.id) {
    case 'reply':
      return '已回复'
    case 'rerun':
      return '已退回并重跑'
    case 'ack':
      return '已标已读'
  }
  return `${viaAdvice ? '已按建议' : '已'}${a.label}`
}

// 明早 9:00（本地时区）——「搁置」不另弹输入，默认搁置到明早。
export function tomorrowNine(now = new Date()): number {
  const d = new Date(now.getFullYear(), now.getMonth(), now.getDate() + 1, 9, 0, 0, 0)
  return Math.floor(d.getTime() / 1000)
}

// 把一次卡片操作映射为现有写接口调用。返回的函数同步发起第一个请求（keepalive 包装依赖这一点）。
export function runCardAction(card: TodayCard, a: TodayAction, text = ''): () => Promise<unknown> {
  const r = card.refs
  switch (card.kind) {
    case 'interaction':
      return () => answerInteraction(r.job_id ?? '', r.interaction_id ?? '', a.id === 'answer' ? a.value ?? '' : text)
    case 'decision':
      return () => answerDecision(r.decision_id ?? '', a.id === 'answer' ? a.value ?? '' : text)
    case 'relay':
      // 「已读」确认这张卡代表的所有未读 turn（同会话只出一张卡）；请求同步发起。
      if (a.id === 'ack') {
        const ids = r.decision_ids?.length ? r.decision_ids : [r.decision_id ?? '']
        return () => Promise.all(ids.map((id) => ackSessionTurn(r.session_id ?? '', id)))
      }
      return () => saySession(r.session_id ?? '', text)
    case 'review':
      if (a.id === 'rerun') return () => rejectJob(r.job_id ?? '', text, a.value !== '0')
      return () => acceptJob(r.job_id ?? '')
    case 'work': {
      const id = r.work_item_id ?? ''
      if (a.id === 'report') return () => requestWorkReport(id)
      if (a.id === 'park') return () => patchWorkItem(id, { status: 'parked', park_until: tomorrowNine() })
      if (a.id.startsWith('adopt:')) return () => acceptWorkSuggestion(id, a.id.slice(6))
      if (a.id.startsWith('dismiss:')) return () => dismissWorkSuggestion(id, a.id.slice(8))
      // 回复 = 写一条日志；有在线会话时同时经会话送达（web 传话），两个请求同步发起。
      return () => {
        const note = addWorkNote(id, text)
        const msg = r.session_id ? sendSessionMessage(r.session_id, text) : Promise.resolve(null)
        return Promise.all([note, msg])
      }
    }
    case 'suggestion':
      if (a.id === 'dismiss') return () => dismissWorkSuggestion(r.work_item_id ?? '', r.field ?? '')
      return () => acceptWorkSuggestion(r.work_item_id ?? '', r.field ?? '')
    case 'merge':
      if (a.id === 'dismiss') return () => dismissMergeSuggestion(r.merge_id ?? 0)
      return () => acceptMergeSuggestion(r.merge_id ?? 0)
    case 'plan_blocked':
      return () => planResume(r.plan_id ?? '')
  }
  return () => Promise.resolve(null)
}

// settleHidden：重拉 /v1/today 后决定哪些卡继续藏着。
// - 还在撤销窗口或写请求在途（未进 committed）的卡：服务端还返回就继续藏；
// - 写操作已成功（committed 记下了成功时的刷新序号 doneSeq）且这次刷新是在那之后才发起的
//   （doneSeq < seq）：不再藏——服务端没返回就自然消失；还返回（如「已读」后又来新 turn、
//   工作项「回复」/「请求汇报」后仍是等我）就重新显示，不会在这个标签页里永远消失。
// committed 会被就地清理。
export function settleHidden(
  hidden: Set<string>,
  committed: Map<string, number>,
  serverKeys: Set<string>,
  pendingKey: string | undefined,
  seq: number,
): Set<string> {
  const next = new Set<string>()
  for (const k of hidden) {
    const doneSeq = committed.get(k)
    if (doneSeq !== undefined && doneSeq < seq) {
      committed.delete(k)
      continue
    }
    if (serverKeys.has(k) || k === pendingKey) next.add(k)
  }
  for (const k of [...committed.keys()]) {
    if (!next.has(k)) committed.delete(k)
  }
  return next
}

// 「自上次打开」水位只在看过首页之后推进：可见满这么久才算看过。
export const TODAY_SEEN_SEC = 10

// 会在 30 秒内超时的卡不等撤销窗口，立即发送。
export function sendImmediately(card: TodayCard, nowSec: number): boolean {
  return !!card.expires_at && card.expires_at - nowSec < SEND_NOW_WITHIN_SEC
}

// 「g」再「d」全局快捷键（800ms 内），输入框里不触发。
export function createChordDetector(windowMs = 800, now: () => number = () => Date.now()) {
  let gAt: number | null = null
  return (key: string, typing: boolean): boolean => {
    if (typing) return false
    if (key === 'g') {
      gAt = now()
      return false
    }
    const hit = key === 'd' && gAt !== null && now() - gAt <= windowMs
    gAt = null
    return hit
  }
}

export function isTypingTarget(t: EventTarget | null): boolean {
  const el = t as { tagName?: string; isContentEditable?: boolean } | null
  if (!el || !el.tagName) return false
  const tag = el.tagName.toUpperCase()
  return tag === 'INPUT' || tag === 'TEXTAREA' || tag === 'SELECT' || !!el.isContentEditable
}

// 「自上次打开」的时间水位：localStorage 记上一次打开首页的时间。
export const LAST_OPEN_KEY = 'gofer.today.lastOpen'
export const INCLUDE_EXEC_KEY = 'gofer.today.includeExec'

export function fmtTokens(n: number): string {
  if (!n) return '0'
  if (n >= 1_000_000) return `${(n / 1_000_000).toFixed(1)}M`
  if (n >= 1_000) return `${(n / 1_000).toFixed(1)}k`
  return String(n)
}
